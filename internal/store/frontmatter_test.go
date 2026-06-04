package store

import (
	"strings"
	"testing"
)

func TestTOMLFrontmatter(t *testing.T) {
	fm, body := SplitFrontmatter("+++\ntitle = \"Deploy Runbook\"\ntags = [\"ops\", \"runbook\"]\n+++\n# Body\nhi\n")
	if fm == nil || fm["title"] != "Deploy Runbook" {
		t.Fatalf("title = %v, want Deploy Runbook", fm["title"])
	}
	tags, _ := fm["tags"].([]any)
	if len(tags) != 2 || tags[0] != "ops" || tags[1] != "runbook" {
		t.Errorf("tags = %v, want [ops runbook]", fm["tags"])
	}
	if !strings.HasPrefix(body, "# Body") {
		t.Errorf("body = %q, want the +++ block stripped", body)
	}
}

func TestMarkdownLinkEdges(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	w("a", "---\ntitle: A\n---\nsee [the setup](b), an [external](https://x.com), an [image](pic.png)\n")
	w("b", "---\ntitle: B\n---\nx\n")

	in, err := s.Backlinks("b")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Slug != "a" {
		t.Fatalf("backlinks(b) via plain markdown link = %+v, want [a]", in)
	}
	// External and image markdown links must not create broken-link noise.
	if h, _ := s.Health(); len(h.Broken) != 0 {
		t.Errorf("markdown links should not produce broken links, got %+v", h.Broken)
	}
}
