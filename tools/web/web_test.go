package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/tools"
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

func TestFetchRefusesLocalAddressesAndOddURLs(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) })
	p := New()
	if _, err := p.fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "local or private") {
		t.Errorf("fetching %s: %v, want a refusal", srv.URL, err)
	}
	for _, raw := range []string{"ftp://example.com/x", "https://user:pw@example.com/", "example.com"} {
		if _, err := p.fetch(context.Background(), raw); err == nil {
			t.Errorf("%s was fetched", raw)
		}
	}
	c, err := permissionContent(context.Background(), tools.Caller{}, map[string]any{"url": "https://Docs.Example.com:8443/a?q=1#top"})
	if err != nil || c.Match != "docs.example.com" || c.Text != "https://Docs.Example.com:8443/a?q=1" {
		t.Errorf("permission content %+v, %v", c, err)
	}
}
