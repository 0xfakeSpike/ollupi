package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	xproxy "golang.org/x/net/proxy"
)

func testProxy(t *testing.T) (*proxy, string) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "reached %s %s", r.Host, r.URL.Path)
	}))
	t.Cleanup(backend.Close)
	rules, err := compileRules(DomainRules{Allowed: []string{"allowed.test", "*.wild.test"}, Denied: []string{"no.wild.test"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(rules)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	p.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())
	return p, port
}

func TestTheProxyForwardsOnlyAllowedHosts(t *testing.T) {
	p, port := testProxy(t)
	proxyURL, _ := url.Parse(p.env()["HTTP_PROXY"])
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	for host, want := range map[string]int{"allowed.test": 200, "other.test": 403, "a.wild.test": 403, "no.wild.test": 403} {
		resp, err := client.Get("http://" + host + ":" + port + "/x")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want || want == 200 && string(body) != "reached allowed.test:"+port+" /x" {
			t.Errorf("%s: %d %q, want %d", host, resp.StatusCode, body, want)
		}
	}
	if blocked := strings.Join(p.blockedHosts(), "\n"); !strings.Contains(blocked, "other.test") || !strings.Contains(blocked, "a.wild.test:"+port+" (resolves to a local") {
		t.Errorf("blocked: %s", blocked)
	}
}

func TestTheProxyTunnelsOnlyAllowedHosts(t *testing.T) {
	p, port := testProxy(t)
	for host, want := range map[string]string{"allowed.test": "200", "other.test": "403"} {
		c, err := net.Dial("tcp", p.ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\nGET /t HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host, port, host, port, host)
		out, _ := io.ReadAll(c)
		c.Close()
		status := strings.Fields(string(out) + "  ")[1]
		if status != want || want == "200" && !strings.HasSuffix(string(out), "reached allowed.test /t") {
			t.Errorf("CONNECT %s: %q", host, out)
		}
	}
}

func TestTheProxySpeaksSOCKS5ByName(t *testing.T) {
	p, port := testProxy(t)
	dialer, err := xproxy.SOCKS5("tcp", p.ln.Addr().String(), nil, xproxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialer.Dial("tcp", "allowed.test:"+port)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "GET /s HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	c.Close()
	if string(body) != "reached allowed.test /s" {
		t.Errorf("through SOCKS5: %q", body)
	}
	for _, host := range []string{"other.test", "a.wild.test"} {
		if c, err := dialer.Dial("tcp", host+":"+port); err == nil {
			c.Close()
			t.Errorf("SOCKS5 reached %s", host)
		}
	}
}
