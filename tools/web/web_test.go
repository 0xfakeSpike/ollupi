package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const page = `<!doctype html><html><head><title> Release notes </title><style>p{}</style></head><body>
<nav><a href="/">Home</a></nav>
<main>
<h1>Version 2</h1>
<p>An <b>un</b>usual   release with <a href="/docs/intro">the intro</a> and <code>go test</code>.</p>
<ul><li>Faster builds</li><li>Fewer <em>bugs</em></li></ul>
<pre>line one
  line two</pre>
<table><tr><th>Name</th><th>Size</th></tr><tr><td>app</td><td>2 MB</td></tr></table>
<script>alert("no")</script>
</main>
<footer>Copyright</footer>
</body></html>`

func TestHTMLText(t *testing.T) {
	base, _ := url.Parse("https://example.com/notes/v2")
	title, text, err := htmlText(strings.NewReader(page), base)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Release notes" {
		t.Errorf("title %q", title)
	}
	want := "# Version 2\n\n" +
		"An unusual release with [the intro](https://example.com/docs/intro) and `go test`.\n\n" +
		"- Faster builds\n- Fewer bugs\n\n" +
		"```\nline one\n  line two\n```\n\n" +
		"| Name | Size |\n| app | 2 MB |"
	if text != want {
		t.Errorf("text:\n%s\n--- want:\n%s", text, want)
	}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch(t *testing.T) {
	var other *httptest.Server
	other = serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("elsewhere")) })
	gbk, _ := simplifiedchinese.GBK.NewEncoder().String("<html><head><meta charset=\"gbk\"><title>标题</title></head><body><p>中文正文</p></body></html>")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(page))
		case "/moved":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/away":
			http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/x", http.StatusMovedPermanently)
		case "/gbk":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(gbk))
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("png"))
		default:
			http.NotFound(w, r)
		}
	})
	p := New(Options{Transport: http.DefaultTransport})
	fetch := func(path string) (FetchOutput, error) { return p.fetch(context.Background(), srv.URL+path) }

	out, err := fetch("/moved")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != srv.URL+"/page" || out.Title != "Release notes" || out.ContentType != "text/html" || !strings.HasPrefix(out.Text, "# Version 2") {
		t.Errorf("same-host redirect: %+v", out)
	}
	if out, err = fetch("/away"); err != nil || !strings.Contains(out.RedirectedTo, "localhost") || out.Text == "" {
		t.Errorf("a redirect to another host is reported, not followed: %+v, %v", out, err)
	}
	if out, err = fetch("/gbk"); err != nil || out.Title != "标题" || out.Text != "中文正文" {
		t.Errorf("gbk page: %+v, %v", out, err)
	}
	if out, err = fetch("/data.json"); err != nil || out.Text != `{"ok":true}` {
		t.Errorf("json: %+v, %v", out, err)
	}
	for path, want := range map[string]string{"/logo.png": "image/png", "/missing": "HTTP 404"} {
		if _, err := fetch(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want it to mention %s", path, err, want)
		}
	}
}

func TestFetchRefusesLocalAddressesAndOddURLs(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) })
	p := New(Options{})
	if _, err := p.fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "local or private") {
		t.Errorf("fetching %s: %v, want a refusal", srv.URL, err)
	}
	for _, raw := range []string{"ftp://example.com/x", "https://user:pw@example.com/", "example.com"} {
		if _, err := p.fetch(context.Background(), raw); err == nil {
			t.Errorf("%s was fetched", raw)
		}
	}
	c, err := permissionContent(context.Background(), map[string]any{"url": "https://Docs.Example.com:8443/a?q=1#top"})
	if err != nil || c.Match != "docs.example.com" || c.Text != "https://Docs.Example.com:8443/a?q=1" {
		t.Errorf("permission content %+v, %v", c, err)
	}
}
