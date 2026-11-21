package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tool"
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
		"An **un**usual release with [the intro](https://example.com/docs/intro) and `go test`.\n\n" +
		"- Faster builds\n- Fewer *bugs*\n\n" +
		"```\nline one\n  line two\n```\n\n" +
		"| Name | Size |\n|------|------|\n| app  | 2 MB |"
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
	if _, err := fetch(context.Background(), http.DefaultTransport, srv.URL); err == nil || !strings.Contains(err.Error(), "local or private") {
		t.Errorf("fetching %s: %v, want a refusal", srv.URL, err)
	}
	for _, raw := range []string{"ftp://example.com/x", "https://user:pw@example.com/", "example.com"} {
		if _, err := fetch(context.Background(), http.DefaultTransport, raw); err == nil {
			t.Errorf("%s was fetched", raw)
		}
	}
	c, err := permissionContent(context.Background(), tool.Call{Arguments: map[string]any{"url": "https://Docs.Example.com:8443/a?q=1#top"}})
	if err != nil || c.Match != "docs.example.com" || c.Text != "https://Docs.Example.com:8443/a?q=1" {
		t.Errorf("permission content %+v, %v", c, err)
	}
}

type transportBox struct {
	sandbox.Box
	transport http.RoundTripper
}

func (b transportBox) Transport() http.RoundTripper { return b.transport }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAFetchGoesOutThroughTheCallersBox(t *testing.T) {
	var sent []string
	box := transportBox{transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		sent = append(sent, r.URL.String())
		page := httptest.NewRecorder()
		page.Header().Set("Content-Type", "text/html")
		page.WriteString("<html><head><title>Hi</title></head><body><p>through the box</p></body></html>")
		resp := page.Result()
		resp.Request = r
		return resp, nil
	})}
	var asked []string
	p := New(func(user string) (sandbox.Box, error) { asked = append(asked, user); return box, nil })
	res, err := p.Invoke(context.Background(), tool.Call{Caller: tool.Caller{User: "u"}, Arguments: map[string]any{"url": "https://ollupi-test.invalid/page"}})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Output.(fetchOutput)
	if strings.Join(asked, ",") != "u" || strings.Join(sent, ",") != "https://ollupi-test.invalid/page" || out.Title != "Hi" || out.Text != "through the box" {
		t.Errorf("asked %v, sent %v, got %+v", asked, sent, out)
	}
}
