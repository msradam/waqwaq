// Package render turns wiki markdown into HTML: GFM, syntax highlighting,
// client-side Mermaid diagrams, and [[wikilinks]] resolved to /wiki/<slug>.
package render

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	mermaid "go.abhg.dev/goldmark/mermaid"
	"go.abhg.dev/goldmark/wikilink"
)

type Renderer struct {
	md goldmark.Markdown
}

func New() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(highlighting.WithStyle("github")),
			&mermaid.Extender{RenderMode: mermaid.RenderModeClient, NoScript: true},
			&wikilink.Extender{Resolver: wikiResolver{}},
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	return &Renderer{md: md}
}

func (r *Renderer) Render(markdown string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // wiki content is trusted + lint-gated
}

type wikiResolver struct{}

func (wikiResolver) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	dest := append([]byte("/wiki/"), n.Target...)
	if len(n.Fragment) > 0 {
		dest = append(dest, '#')
		dest = append(dest, n.Fragment...)
	}
	return dest, nil
}
