// Package render turns wiki markdown into HTML: GFM, syntax highlighting,
// client-side Mermaid diagrams, [[wikilinks]], heading anchors, and GitHub or
// Obsidian style callouts ([!NOTE] blockquotes). It also extracts a heading
// outline for the right-rail table of contents, using a separate clean parse so
// anchor glyphs never leak into it.
package render

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/anchor"
	mermaid "go.abhg.dev/goldmark/mermaid"
	"go.abhg.dev/goldmark/wikilink"
)

type TOCEntry struct {
	Level int
	Text  string
	ID    string
}

type Renderer struct {
	md  goldmark.Markdown
	toc goldmark.Markdown
}

// New builds a renderer. base is the URL prefix wikilinks resolve against:
// "" for a single wiki at the root, "/w/<name>" for a wiki in farm mode.
func New(base string) *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(highlighting.WithStyle("github")),
			&mermaid.Extender{RenderMode: mermaid.RenderModeClient, NoScript: true},
			&wikilink.Extender{Resolver: wikiResolver{base: base}},
			&anchor.Extender{},
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(calloutTransformer{}, 100)),
		),
	)
	toc := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	return &Renderer{md: md, toc: toc}
}

func (r *Renderer) Render(markdown string) (template.HTML, []TOCEntry, error) {
	src := []byte(markdown)
	var buf bytes.Buffer
	if err := r.md.Convert(src, &buf); err != nil {
		return "", nil, err
	}
	return template.HTML(buf.String()), r.extractTOC(src), nil //nolint:gosec // trusted, lint-gated content
}

func (r *Renderer) extractTOC(src []byte) []TOCEntry {
	doc := r.toc.Parser().Parse(text.NewReader(src))
	var toc []TOCEntry
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		h, ok := n.(*ast.Heading)
		if !ok || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		id, _ := h.AttributeString("id")
		idBytes, _ := id.([]byte)
		toc = append(toc, TOCEntry{Level: h.Level, Text: nodeText(h, src), ID: string(idBytes)})
		return ast.WalkContinue, nil
	})
	return toc
}

func nodeText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch t := c.(type) {
			case *ast.Text:
				b.Write(t.Segment.Value(src))
			case *ast.String:
				b.Write(t.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// calloutTransformer turns a blockquote whose first line is `[!TYPE]` into a
// styled admonition, the GitHub and Obsidian callout convention. It only sets a
// class and trims the marker, reusing goldmark's blockquote parser, so it cannot
// crash on malformed input.
type calloutTransformer struct{}

var calloutRe = regexp.MustCompile(`^\s*\[!([A-Za-z]+)\]`)

func (calloutTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		bq, ok := n.(*ast.Blockquote)
		if !ok {
			return ast.WalkContinue, nil
		}
		para, ok := bq.FirstChild().(*ast.Paragraph)
		if !ok || para.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		seg := para.Lines().At(0)
		firstLine := seg.Value(src)
		loc := calloutRe.FindSubmatchIndex(firstLine)
		if loc == nil {
			return ast.WalkContinue, nil
		}
		kind := strings.ToLower(string(firstLine[loc[2]:loc[3]]))
		bq.SetAttributeString("class", []byte("admonition adm-"+kind))
		stripLeading(para, loc[1])
		return ast.WalkSkipChildren, nil
	})
}

// stripLeading removes the first n source bytes of inline content from a
// paragraph, trimming or dropping the leading text nodes that the callout marker
// was split across.
func stripLeading(para ast.Node, n int) {
	for c := para.FirstChild(); c != nil && n > 0; {
		next := c.NextSibling()
		t, ok := c.(*ast.Text)
		if !ok {
			return
		}
		segLen := t.Segment.Stop - t.Segment.Start
		if segLen <= n {
			para.RemoveChild(para, c)
			n -= segLen
		} else {
			t.Segment = text.NewSegment(t.Segment.Start+n, t.Segment.Stop)
			return
		}
		c = next
	}
}

type wikiResolver struct{ base string }

func (w wikiResolver) ResolveWikilink(n *wikilink.Node) ([]byte, error) {
	if len(n.Target) == 0 && len(n.Fragment) > 0 { // same-page [[#heading]]
		return append([]byte("#"), slugFragment(string(n.Fragment))...), nil
	}
	dest := append([]byte(w.base+"/wiki/"), n.Target...)
	if len(n.Fragment) > 0 {
		dest = append(dest, '#')
		dest = append(dest, slugFragment(string(n.Fragment))...)
	}
	return dest, nil
}

// slugFragment turns a wikilink heading fragment ([[Page#Some Heading]]) into the
// id goldmark generates for that heading (lowercase, spaces to hyphens, other
// punctuation dropped without collapsing), so the anchor actually lands.
func slugFragment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-':
			b.WriteByte('-')
		}
	}
	return b.String()
}
