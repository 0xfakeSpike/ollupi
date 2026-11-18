package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
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
	web     *http.Server
	socks   *socks5.Server
	plain   *handoff

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	blocked []string
	seen    map[string]bool
	closed  bool
	wg      sync.WaitGroup
}

func startProxy(compiled compiledRules) (*proxy, error) {
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
		plain: &handoff{addr: ln.Addr(), conns: make(chan net.Conn), done: make(chan struct{})},
	}
	forward := &httputil.ReverseProxy{
		Rewrite: func(*httputil.ProxyRequest) {},
		Transport: &http.Transport{DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			host, port, err := splitTarget(addr, "80")
			if err != nil {
				return nil, err
			}
			return p.dial(host, port)
		}},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) { refuseHTTP(w, err) },
	}
	p.web = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodConnect:
			p.serveConnect(w, r)
		case r.URL.Scheme != "http" || r.URL.Host == "":
			refuseHTTP(w, fmt.Errorf("a proxy request needs an absolute http:// URL; use CONNECT for https"))
		default:
			forward.ServeHTTP(w, r)
		}
	}), ErrorLog: log.New(io.Discard, "", 0)}
	p.socks = socks5.NewServer(
		socks5.WithResolver(byName{}),
		socks5.WithRule(&socks5.PermitCommand{EnableConnect: true}),
		socks5.WithConnectHandle(p.serveSOCKS),
		socks5.WithLogger(socks5.NewLogger(log.New(io.Discard, "", 0))),
	)
	p.wg.Add(2)
	go p.serve()
	go func() {
		defer p.wg.Done()
		p.web.Serve(p.plain)
	}()
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
	p.web.Close()
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
			br := bufio.NewReader(c)
			first, err := br.Peek(1)
			socks := err == nil && first[0] == 0x05
			if !socks {
				p.mu.Lock()
				delete(p.conns, c)
				p.mu.Unlock()
			}
			switch conn := (peeked{Conn: c, r: br}); {
			case socks:
				p.socks.ServeConn(conn)
				p.untrack(c)
			case err != nil || !p.plain.give(conn):
				c.Close()
			}
		}()
	}
}

type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (c peeked) Read(b []byte) (int, error) { return c.r.Read(b) }

type handoff struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (h *handoff) give(c net.Conn) bool {
	select {
	case h.conns <- c:
		return true
	case <-h.done:
		return false
	}
}

func (h *handoff) Accept() (net.Conn, error) {
	select {
	case c := <-h.conns:
		return c, nil
	case <-h.done:
		return nil, net.ErrClosed
	}
}

func (h *handoff) Close() error {
	h.once.Do(func() { close(h.done) })
	return nil
}

func (h *handoff) Addr() net.Addr { return h.addr }

type byName struct{}

func (byName) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
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
		if sensitiveAddr(a) && !literal {
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

func refuseHTTP(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, errBlocked) {
		status = http.StatusForbidden
	}
	http.Error(w, "sandbox: "+err.Error(), status)
}

func (p *proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitTarget(r.Host, "443")
	if err != nil {
		refuseHTTP(w, err)
		return
	}
	up, err := p.dial(host, port)
	if err != nil {
		refuseHTTP(w, err)
		return
	}
	if !p.track(up) {
		return
	}
	defer p.untrack(up)
	c, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	if !p.track(c) {
		return
	}
	defer p.untrack(c)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	tunnel(c, brw.Reader, up)
}

func (p *proxy) serveSOCKS(_ context.Context, w io.Writer, req *socks5.Request) error {
	host := req.DestAddr.FQDN
	if host == "" {
		host = req.DestAddr.IP.String()
	}
	up, err := p.dial(host, req.DestAddr.Port)
	if err != nil {
		reply := statute.RepConnectionRefused
		if errors.Is(err, errBlocked) {
			reply = statute.RepRuleFailure
		}
		return socks5.SendReply(w, reply, nil)
	}
	if !p.track(up) {
		return nil
	}
	defer p.untrack(up)
	if err := socks5.SendReply(w, statute.RepSuccess, up.LocalAddr()); err != nil {
		return err
	}
	tunnel(w.(net.Conn), req.Reader, up)
	return nil
}

func closeWrite(c net.Conn) {
	if p, ok := c.(peeked); ok {
		c = p.Conn
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}

func tunnel(c net.Conn, from io.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst net.Conn, src io.Reader) {
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
		done <- struct{}{}
	}
	go cp(up, from)
	go cp(c, up)
	<-done
	<-done
}
