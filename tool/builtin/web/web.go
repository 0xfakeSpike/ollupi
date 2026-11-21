package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
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

type fetchArgs struct {
	URL string `json:"url" jsonschema:"The http or https URL to read."`
}

func (fetchArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["url"].MinLength = jsonschema.Ptr(1)
}

type fetchOutput struct {
	URL          string `json:"url"`
	RedirectedTo string `json:"redirectedTo,omitempty"`
	Title        string `json:"title,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type Provider struct {
	boxes func(user string) (sandbox.Box, error)
}

func New(boxes func(user string) (sandbox.Box, error)) *Provider {
	return &Provider{boxes: boxes}
}

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

func (p *Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{{
		Name: Tool,
		Description: "Read a public web page and return its text; HTML comes back as Markdown-like text with its links. " +
			"Only http and https on public addresses. A redirect to another host is reported, not followed: fetch the new URL to follow it. " +
			"For downloads, APIs that need headers or methods other than GET, or local servers, use run_command.",
		InputSchema: kit.InputSchema[fetchArgs](),
		Annotations: tool.Annotations{Title: "Fetch web page", DestructiveHint: new(false), OpenWorldHint: new(true)},
		Content:     permissionContent,
	}}, nil
}

func permissionContent(_ context.Context, c tool.Call) (policy.Content, error) {
	f, _ := kit.DecodeArguments[fetchArgs](c.Arguments)
	u, err := parseURL(f.URL)
	if err != nil {
		return policy.Content{}, err
	}
	return policy.Content{Text: u.String(), Match: strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), Folded: true}, nil
}

func isHTML(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func textual(mediaType string) bool {
	return isHTML(mediaType) || strings.HasPrefix(mediaType, "text/") || mediaType == "" ||
		mediaType == "application/json" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

func bodyText(resp *http.Response, mediaType string) (title, text string, err error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("read: %w", err)
	}
	if len(body) > maxBodyBytes {
		return "", "", fmt.Errorf("larger than %d MB; use run_command to download it", maxBodyBytes>>20)
	}
	decoded, err := charset.NewReader(bytes.NewReader(body), resp.Header.Get("Content-Type"))
	if err != nil {
		return "", "", fmt.Errorf("decode: %w", err)
	}
	if isHTML(mediaType) {
		return htmlText(decoded, resp.Request.URL)
	}
	b, err := io.ReadAll(decoded)
	return "", strings.TrimSpace(strings.ToValidUTF8(string(b), "\uFFFD")), err
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

func (p *Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	f, _ := kit.DecodeArguments[fetchArgs](c.Arguments)
	box, err := p.boxes(c.Caller.User)
	if err != nil {
		return kit.Result{}, fmt.Errorf("web: the sandbox of the user: %w", err)
	}
	out, err := fetch(ctx, box.Transport(), f.URL)
	if err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: out}, nil
}

func fetch(ctx context.Context, transport http.RoundTripper, raw string) (fetchOutput, error) {
	u, err := parseURL(raw)
	if err != nil {
		return fetchOutput{}, err
	}
	if err := sandbox.CheckPublic(ctx, u.Hostname()); err != nil {
		return fetchOutput{}, fmt.Errorf("web: fetch %s: %w; use run_command to reach it", u.Redacted(), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fetchOutput{}, fmt.Errorf("web: %w", err)
	}
	req.Header.Set("User-Agent", "ollupi")
	req.Header.Set("Accept", fetchAccepted)
	client := &http.Client{Transport: transport, Timeout: fetchTimeout, CheckRedirect: sameHost}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		if errors.Is(err, sandbox.ErrLocalAddress) {
			return fetchOutput{}, fmt.Errorf("web: fetch %s: %w; use run_command to reach it", u.Redacted(), err)
		}
		return fetchOutput{}, fmt.Errorf("web: fetch %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()
	out := fetchOutput{URL: resp.Request.URL.String()}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		next, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		if err != nil || resp.Header.Get("Location") == "" {
			return fetchOutput{}, fmt.Errorf("web: %s answered HTTP %d without a usable Location", out.URL, resp.StatusCode)
		}
		out.RedirectedTo = next.String()
		out.Text = fmt.Sprintf("The page redirects to %s on another host. Fetch that URL to follow it.", out.RedirectedTo)
		return out, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fetchOutput{}, fmt.Errorf("web: %s answered HTTP %d", out.URL, resp.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	out.ContentType = mediaType
	if !textual(mediaType) {
		return fetchOutput{}, fmt.Errorf("web: %s is %s, which web_fetch does not read; use run_command to download it", out.URL, mediaType)
	}
	if out.Title, out.Text, err = bodyText(resp, mediaType); err != nil {
		return fetchOutput{}, fmt.Errorf("web: %s: %w", out.URL, err)
	}
	if utf8.RuneCountInString(out.Text) > maxTextRunes {
		out.Text, out.Truncated = string([]rune(out.Text)[:maxTextRunes]), true
	}
	return out, nil
}
