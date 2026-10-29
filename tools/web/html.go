package web

import (
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipped elements hold no reading text: scripts and styles, page chrome, and forms.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Iframe: true,
	atom.Head: true, atom.Nav: true, atom.Footer: true, atom.Aside: true, atom.Form: true, atom.Button: true, atom.Select: true,
}

var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Main: true, atom.Header: true,
	atom.Ul: true, atom.Ol: true, atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Table: true,
	atom.Blockquote: true, atom.Figure: true, atom.Figcaption: true, atom.Details: true, atom.Summary: true, atom.Address: true,
}

var headings = map[atom.Atom]string{atom.H1: "# ", atom.H2: "## ", atom.H3: "### ", atom.H4: "#### ", atom.H5: "##### ", atom.H6: "###### "}

var blankLines = regexp.MustCompile(`\n{3,}`)

// htmlText renders a page as Markdown-like text: headings, paragraphs, lists, links, code and table rows.
func htmlText(r io.Reader, base *url.URL) (title, text string, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	w := &textWriter{base: base}
	w.walk(doc)
	lines := strings.Split(w.b.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(w.title), strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")), nil
}

type textWriter struct {
	b     strings.Builder
	base  *url.URL
	title string
	space bool
}

// text writes the words of a text node; whitespace between nodes becomes one space.
func (w *textWriter) text(s string) {
	if strings.TrimSpace(s) == "" {
		w.space = w.space || s != ""
		return
	}
	w.space = w.space || unicode.IsSpace(rune(s[0]))
	for i, word := range strings.Fields(s) {
		w.space = w.space || i > 0
		w.flush()
		w.b.WriteString(word)
	}
	w.space = unicode.IsSpace(rune(s[len(s)-1]))
}

// flush writes the space left pending before the next word, unless a line or a space already separates them.
func (w *textWriter) flush() {
	if out := w.b.String(); w.space && out != "" && !strings.HasSuffix(out, "\n") && !strings.HasSuffix(out, " ") {
		w.b.WriteByte(' ')
	}
	w.space = false
}

func (w *textWriter) raw(s string) {
	w.b.WriteString(s)
	w.space = false
}

func (w *textWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
}

func (w *textWriter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.ElementNode:
	default:
		w.children(n)
		return
	}
	if n.DataAtom == atom.Head {
		for c := n.FirstChild; c != nil && w.title == ""; c = c.NextSibling {
			if c.DataAtom == atom.Title {
				w.title = nodeText(c)
			}
		}
	}
	if skipped[n.DataAtom] {
		return
	}
	switch {
	case headings[n.DataAtom] != "":
		w.raw("\n\n" + headings[n.DataAtom])
		w.children(n)
		w.raw("\n\n")
	case n.DataAtom == atom.Pre:
		w.raw("\n\n```\n" + strings.Trim(nodeText(n), "\n") + "\n```\n\n")
	case n.DataAtom == atom.Code:
		w.flush()
		w.raw("`" + nodeText(n) + "`")
	case n.DataAtom == atom.Li:
		w.raw("\n- ")
		w.children(n)
	case n.DataAtom == atom.Br:
		w.raw("\n")
	case n.DataAtom == atom.Hr:
		w.raw("\n\n---\n\n")
	case n.DataAtom == atom.Tr:
		w.raw("\n")
		w.children(n)
		w.raw("|")
	case n.DataAtom == atom.Td || n.DataAtom == atom.Th:
		w.raw("| ")
		w.children(n)
		w.raw(" ")
	case n.DataAtom == atom.A:
		w.link(n)
	case n.DataAtom == atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			w.text(" [image: " + alt + "] ")
		}
	case blocks[n.DataAtom]:
		w.raw("\n\n")
		w.children(n)
		w.raw("\n\n")
	default:
		w.children(n)
	}
}

func (w *textWriter) link(n *html.Node) {
	label := strings.Join(strings.Fields(nodeText(n)), " ")
	href := strings.TrimSpace(attr(n, "href"))
	target, err := w.base.Parse(href)
	if label == "" || href == "" || strings.HasPrefix(href, "#") || err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		w.children(n)
		return
	}
	w.flush()
	w.raw("[" + label + "](" + target.String() + ")")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && skipped[n.DataAtom] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
