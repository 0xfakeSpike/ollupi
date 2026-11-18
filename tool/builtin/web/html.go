package web

import (
	"io"
	"net/url"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var markdown = func() *converter.Converter {
	c := converter.NewConverter(converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin()))
	for _, tag := range []string{"script", "style", "noscript", "template", "svg", "iframe", "head", "nav", "footer", "aside", "form", "button", "select"} {
		c.Register.TagType(tag, converter.TagTypeRemove, converter.PriorityStandard)
	}
	return c
}()

func htmlText(r io.Reader, page *url.URL) (title, text string, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	if t := find(doc, atom.Title); t != nil && t.FirstChild != nil {
		title = strings.TrimSpace(t.FirstChild.Data)
	}
	out, err := markdown.ConvertNode(doc, converter.WithDomain(page.String()))
	return title, strings.TrimSpace(string(out)), err
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := find(c, a); found != nil {
			return found
		}
	}
	return nil
}
