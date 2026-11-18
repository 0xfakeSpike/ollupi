package sandbox

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxyDialTimeout = 15 * time.Second
	maxBlockedHosts  = 20
)

type proxy struct {
	ln      net.Listener
	rules   compiledRules
	ctx     context.Context
	cancel  context.CancelFunc
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	blocked []string
	seen    map[string]bool
	closed  bool
	wg      sync.WaitGroup
}

func startProxy(rules DomainRules) (*proxy, error) {
	compiled, err := compileRules(rules)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sandbox: start network proxy: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proxy{
		ln: ln, rules: compiled, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}, seen: map[string]bool{},
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

func (p *proxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *proxy) env() map[string]string {
	httpURL := "http://127.0.0.1:" + strconv.Itoa(p.port())
	socksURL := "socks5h://127.0.0.1:" + strconv.Itoa(p.port())
	return map[string]string{
		"HTTP_PROXY": httpURL, "HTTPS_PROXY": httpURL, "http_proxy": httpURL, "https_proxy": httpURL,
		"ALL_PROXY": socksURL, "all_proxy": socksURL,

		"NODE_USE_ENV_PROXY": "1",

		"GRPC_PROXY": httpURL, "grpc_proxy": httpURL,

		"JAVA_TOOL_OPTIONS": "-Dhttp.proxyHost=127.0.0.1 -Dhttp.proxyPort=" + strconv.Itoa(p.port()) +
			" -Dhttps.proxyHost=127.0.0.1 -Dhttps.proxyPort=" + strconv.Itoa(p.port()) +

			" -Dhttp.nonProxyHosts= -Djava.net.preferIPv4Stack=true",

		"NO_PROXY": "", "no_proxy": "",
	}
}

func (p *proxy) close() {
	p.cancel()
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.ln.Close()
	p.wg.Wait()
}

func (p *proxy) blockedHosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.blocked...)
}

func (p *proxy) block(target, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := "network: blocked " + target + " (" + why + ")"
	if p.seen[line] || len(p.blocked) >= maxBlockedHosts {
		return
	}
	p.seen[line] = true
	p.blocked = append(p.blocked, line)
}

func (p *proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		c.Close()
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
	c.Close()
}

func (p *proxy) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			defer func() { recover() }()
			br := bufio.NewReader(c)
			first, err := br.Peek(1)
			if err != nil {
				return
			}
			if first[0] == 0x05 {
				p.serveSOCKS(c, br)
				return
			}
			p.serveHTTP(c, br)
		}()
	}
}

func (p *proxy) dial(host string, port int) (net.Conn, error) {
	target := net.JoinHostPort(host, strconv.Itoa(port))
	allowed, literal := p.rules.decide(host, port)
	if !allowed {
		p.block(target, "not in allowed_domains")
		return nil, errBlocked
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(p.ctx, proxyDialTimeout)
		defer cancel()
		if addrs, err = p.resolve(ctx, host); err != nil {
			return nil, err
		}
	}
	var lastErr error = fmt.Errorf("no address for %s", host)
	for _, a := range addrs {
		if SensitiveAddr(a) && !literal {
			p.block(target, "resolves to a local, private or link-local address; allow it by name or address to reach it")
			lastErr = errBlocked
			continue
		}
		d := net.Dialer{Timeout: proxyDialTimeout}
		c, err := d.DialContext(p.ctx, "tcp", netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

var errBlocked = errors.New("blocked by the sandbox's network allowlist")

func splitTarget(hostport, defaultPort string) (string, int, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, defaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 || host == "" {
		return "", 0, fmt.Errorf("invalid target %q", hostport)
	}
	return host, n, nil
}

func (p *proxy) serveHTTP(c net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method == http.MethodConnect {
		p.serveConnect(c, br, req)
		return
	}
	p.forward(c, req)
}

func (p *proxy) refuseHTTP(c net.Conn, err error) {
	status, reason := http.StatusBadGateway, err.Error()
	if errors.Is(err, errBlocked) {
		status = http.StatusForbidden
	}
	body := "sandbox: " + reason + "\n"
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(body), body)
}

func (p *proxy) serveConnect(c net.Conn, br *bufio.Reader, req *http.Request) {
	host, port, err := splitTarget(req.Host, "443")
	if err != nil {
		p.refuseHTTP(c, err)
		return
	}
	up, err := p.dial(host, port)
	if err != nil {
		p.refuseHTTP(c, err)
		return
	}
	if !p.track(up) {
		return
	}
	defer p.untrack(up)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	if n := br.Buffered(); n > 0 {
		buf, _ := br.Peek(n)
		if _, err := up.Write(buf); err != nil {
			return
		}
	}
	tunnel(c, up)
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *proxy) forward(c net.Conn, req *http.Request) {
	if req.URL.Scheme != "http" || req.URL.Host == "" {
		p.refuseHTTP(c, fmt.Errorf("a proxy request needs an absolute http:// URL; use CONNECT for https"))
		return
	}
	host, port, err := splitTarget(req.URL.Host, "80")
	if err != nil {
		p.refuseHTTP(c, err)
		return
	}
	up, err := p.dial(host, port)
	if err != nil {
		p.refuseHTTP(c, err)
		return
	}
	if !p.track(up) {
		return
	}
	defer p.untrack(up)
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	req.RequestURI = ""
	req.Close = true
	if err := req.Write(up); err != nil {
		p.refuseHTTP(c, err)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), req)
	if err != nil {
		p.refuseHTTP(c, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		if h != "Transfer-Encoding" {
			resp.Header.Del(h)
		}
	}
	resp.Close = true
	resp.Write(c)
}

func (p *proxy) serveSOCKS(c net.Conn, br *bufio.Reader) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if !strings.ContainsRune(string(methods), 0x00) {
		c.Write([]byte{0x05, 0xff})
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	reply := func(code byte) { c.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) }
	if req[0] != 0x05 {
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = netip.AddrFrom4([4]byte(b)).String()
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = netip.AddrFrom16([16]byte(b)).String()
	case 0x03:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	default:
		reply(0x08)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	if req[1] != 0x01 {
		reply(0x07)
		return
	}
	port := int(binary.BigEndian.Uint16(pb))
	up, err := p.dial(host, port)
	if err != nil {
		if errors.Is(err, errBlocked) {
			reply(0x02)
		} else {
			reply(0x05)
		}
		return
	}
	if !p.track(up) {
		return
	}
	defer p.untrack(up)
	reply(0x00)
	if n := br.Buffered(); n > 0 {
		buf, _ := br.Peek(n)
		if _, err := up.Write(buf); err != nil {
			return
		}
	}
	tunnel(c, up)
}

func tunnel(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
