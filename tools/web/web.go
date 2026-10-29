// Package web gives the model web_fetch: read a public web page as text.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"golang.org/x/net/html/charset"
)

const (
	Tool = "web_fetch"

	fetchTimeout  = 30 * time.Second
	maxBodyBytes  = 10 << 20
	maxTextRunes  = 200_000
	maxRedirects  = 10
	fetchAccepted = "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.9,*/*;q=0.1"
)

type Fetch struct {
	URL string `json:"url" jsonschema:"The http or https URL to read."`
}

func (Fetch) Constrain(s *jsonschema.Schema) {
	s.Properties["url"].MinLength = jsonschema.Ptr(1)
}

type FetchOutput struct {
	URL          string `json:"url"`
	RedirectedTo string `json:"redirectedTo,omitempty"`
	Title        string `json:"title,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type Options struct {
	// Transport replaces the transport that refuses local and private addresses; tests use it to reach a local server.
	Transport http.RoundTripper
}

type Provider struct {
	client *http.Client
}

func New(opts Options) *Provider {
	transport := opts.Transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport = &http.Transport{
			// No proxy from the environment: a proxy would make the dial below check the proxy instead of the site.
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialPublic(ctx, dialer, network, addr)
			},
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		}
	}
	return &Provider{client: &http.Client{Transport: transport, Timeout: fetchTimeout, CheckRedirect: sameHost}}
}

// dialPublic connects only to public addresses. It checks the addresses the name resolves to at dial time,
// so a name cannot point at the local network after it was approved.
func dialPublic(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var lastErr error = fmt.Errorf("web: %s has no address", host)
	for _, a := range addrs {
		if sandbox.SensitiveAddr(a) {
			lastErr = fmt.Errorf("web: %s resolves to a local or private address; use run_command to reach it", host)
			continue
		}
		c, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// sameHost follows redirects within the host the call was approved for. A redirect elsewhere comes back
// to the model, so the new host goes through the permission check as its own call.
func sameHost(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= maxRedirects:
		return fmt.Errorf("web: stopped after %d redirects", maxRedirects)
	case req.URL.Scheme != "http" && req.URL.Scheme != "https":
		return fmt.Errorf("web: redirect to unsupported URL %s", req.URL.Redacted())
	case !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()):
		return http.ErrUseLastResponse
	}
	return nil
}

func (*Provider) Name() (string, string) { return "web", "Reads public web pages." }

func (p *Provider) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{{
		Name: Tool,
		Description: "Read a public web page and return its text; HTML comes back as Markdown-like text with its links. " +
			"Only http and https on public addresses. A redirect to another host is reported, not followed: fetch the new URL to follow it. " +
			"For downloads, APIs that need headers or methods other than GET, or local servers, use run_command.",
		InputSchema: tools.InputSchema[Fetch](),
		// Not read-only: a URL can carry data to the site, so each new site is approved unless a rule allows it.
		Annotations:       &tools.Annotations{Title: "Fetch web page", DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(true)},
		PermissionContent: permissionContent,
	}}, nil
}

func permissionContent(_ context.Context, arguments map[string]any) (tools.Content, error) {
	f, err := tools.DecodeArguments[Fetch](arguments)
	if err != nil {
		return tools.Content{}, err
	}
	u, err := parseURL(f.URL)
	if err != nil {
		return tools.Content{}, err
	}
	return tools.Content{Text: u.String(), Match: strings.ToLower(u.Hostname())}, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("web: invalid URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("web: %q is not an http or https URL", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("web: URLs with credentials are not fetched")
	}
	u.Fragment = ""
	return u, nil
}

func (p *Provider) Invoke(ctx context.Context, _ string, arguments map[string]any) (tools.Result, error) {
	f, err := tools.DecodeArguments[Fetch](arguments)
	if err != nil {
		return tools.Result{}, err
	}
	out, err := p.fetch(ctx, f.URL)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Output: out}, nil
}

func (p *Provider) fetch(ctx context.Context, raw string) (FetchOutput, error) {
	u, err := parseURL(raw)
	if err != nil {
		return FetchOutput{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return FetchOutput{}, fmt.Errorf("web: %w", err)
	}
	req.Header.Set("User-Agent", "ollupi")
	req.Header.Set("Accept", fetchAccepted)
	resp, err := p.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return FetchOutput{}, fmt.Errorf("web: fetch %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()
	out := FetchOutput{URL: resp.Request.URL.String()}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		next, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		if err != nil || resp.Header.Get("Location") == "" {
			return FetchOutput{}, fmt.Errorf("web: %s answered HTTP %d without a usable Location", out.URL, resp.StatusCode)
		}
		out.RedirectedTo = next.String()
		out.Text = fmt.Sprintf("The page redirects to %s on another host. Fetch that URL to follow it.", out.RedirectedTo)
		return out, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return FetchOutput{}, fmt.Errorf("web: %s answered HTTP %d", out.URL, resp.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	out.ContentType = mediaType
	isHTML := mediaType == "text/html" || mediaType == "application/xhtml+xml"
	textual := isHTML || strings.HasPrefix(mediaType, "text/") || mediaType == "" ||
		mediaType == "application/json" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
	if !textual {
		return FetchOutput{}, fmt.Errorf("web: %s is %s, which web_fetch does not read; use run_command to download it", out.URL, mediaType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return FetchOutput{}, fmt.Errorf("web: read %s: %w", out.URL, err)
	}
	if len(body) > maxBodyBytes {
		return FetchOutput{}, fmt.Errorf("web: %s is larger than %d MB; use run_command to download it", out.URL, maxBodyBytes>>20)
	}
	decoded, err := charset.NewReader(strings.NewReader(string(body)), resp.Header.Get("Content-Type"))
	if err != nil {
		return FetchOutput{}, fmt.Errorf("web: decode %s: %w", out.URL, err)
	}
	if isHTML {
		out.Title, out.Text, err = htmlText(decoded, resp.Request.URL)
	} else {
		var b []byte
		b, err = io.ReadAll(decoded)
		out.Text = strings.TrimSpace(strings.ToValidUTF8(string(b), "�"))
	}
	if err != nil {
		return FetchOutput{}, fmt.Errorf("web: read %s: %w", out.URL, err)
	}
	if utf8.RuneCountInString(out.Text) > maxTextRunes {
		out.Text, out.Truncated = string([]rune(out.Text)[:maxTextRunes]), true
	}
	return out, nil
}
