package sandbox

import (
	"net/http"
	"net/url"
	"sync"

	"github.com/mattn/go-ieproxy"
	"golang.org/x/net/http/httpproxy"
)

var machineTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = systemProxy
	return t
}()

var proxyConf sync.Mutex

func systemProxy(r *http.Request) (*url.URL, error) {
	env := httpproxy.FromEnvironment()
	if env.HTTPProxy != "" || env.HTTPSProxy != "" {
		return env.ProxyFunc()(r.URL)
	}
	proxyConf.Lock()
	conf := ieproxy.ReloadConf()
	proxyConf.Unlock()
	if conf.Automatic.Active {
		if host := conf.Automatic.FindProxyForURL(r.URL.String()); host != "" {
			return &url.URL{Host: host}, nil
		}
	}
	if !conf.Static.Active {
		return nil, nil
	}
	static := httpproxy.Config{HTTPProxy: conf.Static.Protocols["http"], HTTPSProxy: conf.Static.Protocols["https"], NoProxy: conf.Static.NoProxy}
	return static.ProxyFunc()(r.URL)
}
