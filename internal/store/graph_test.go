package store

import "testing"

func seed(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	write("index", "---\ntitle: Home\n---\nsee [[guide]] and [[missing]]\n")
	write("guide", "---\ntitle: Guide\ntags: [ops, onboarding]\n---\nback to [[index]]\n")
	write("lonely", "---\ntitle: Lonely\ntags: ops\n---\nno links here\n")
	return s
}

func TestBacklinks(t *testing.T) {
	s := seed(t)
	in, err := s.Backlinks("index")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Slug != "guide" {
		t.Fatalf("backlinks(index) = %+v, want [guide]", in)
	}
	if in, _ := s.Backlinks("lonely"); len(in) != 0 {
		t.Errorf("lonely should have no backlinks, got %+v", in)
	}
}

func TestHealth(t *testing.T) {
	s := seed(t)
	h, err := s.Health()
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Broken) != 1 || h.Broken[0].To != "missing" {
		t.Errorf("broken = %+v, want one link to missing", h.Broken)
	}
	orphans := map[string]bool{}
	for _, o := range h.Orphans {
		orphans[o.Slug] = true
	}
	if !orphans["lonely"] {
		t.Errorf("lonely should be an orphan, orphans = %+v", h.Orphans)
	}
	if orphans["index"] {
		t.Error("index is a root and should not be an orphan")
	}
	if len(h.Stale) != 0 {
		t.Errorf("freshly written pages should not be stale, got %+v", h.Stale)
	}
}

func TestBasenameResolution(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	// A nested page referenced by bare basename, the way Obsidian links it.
	write("Plugins/Command palette", "---\ntitle: Command palette\n---\nthe palette\n")
	write("Editing/Callouts", "---\ntitle: Callouts\n---\nuse the [[Command palette]] to insert\n")

	if canon, ok := s.ResolveLink("Command palette"); !ok || canon != "Plugins/Command palette" {
		t.Fatalf("ResolveLink(bare basename) = %q,%v want Plugins/Command palette", canon, ok)
	}
	if canon, ok := s.ResolveLink("Command palette|the palette"); !ok || canon != "Plugins/Command palette" {
		t.Fatalf("ResolveLink with alias = %q,%v", canon, ok)
	}
	if _, ok := s.ResolveLink("nope"); ok {
		t.Error("unknown target should not resolve")
	}
	// The bare link must produce a real backlink edge, not a broken one.
	in, err := s.Backlinks("Plugins/Command palette")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Slug != "Editing/Callouts" {
		t.Fatalf("backlinks via basename = %+v, want [Editing/Callouts]", in)
	}
	if h, _ := s.Health(); len(h.Broken) != 0 {
		t.Errorf("basename link should not be broken, got %+v", h.Broken)
	}
}

func TestLinkHygiene(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	w("authoring-content", "---\ntitle: Authoring\n---\nbase page\n")
	w("guide/setup", "---\ntitle: Setup\n---\nx\n")
	// A page exercising every supported link convention.
	w("index", "---\ntitle: Home\n---\n"+
		"Obsidian: [[guide/setup]]\n"+ // normal
		"Space fold: [[Authoring Content]]\n"+ // space -> hyphen, case
		"Dendron/GitHub pipe: [[the label|authoring-content]]\n"+ // target on the right
		"Image embed: ![[diagram.png]]\n"+ // embed, not a page edge
		"Asset link: [[diagram.png]]\n"+ // asset, not broken
		"Code example: `[[not-a-real-link]]` and\n```\n[[also-not-real]]\n```\n"+ // in code, ignored
		"External: [[https://example.com/x]]\n"+ // url, not broken
		"In-page anchor: [[#some-heading]]\n"+ // same-page anchor, not broken
		"Dead: [[genuinely-missing]]\n")

	h, err := s.Health()
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Broken) != 1 || h.Broken[0].To != "genuinely-missing" {
		t.Fatalf("broken = %+v, want exactly [genuinely-missing]", h.Broken)
	}
	// index should link to guide/setup and authoring-content (3 link forms, 2 distinct targets).
	in, err := s.Backlinks("authoring-content")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Slug != "index" {
		t.Fatalf("authoring-content backlinks = %+v, want [index]", in)
	}
	if _, err := s.Backlinks("guide/setup"); err != nil {
		t.Fatal(err)
	}
	if canon, ok := s.ResolveLink("the label|authoring-content"); !ok || canon != "authoring-content" {
		t.Fatalf("piped ResolveLink = %q,%v want authoring-content", canon, ok)
	}
	if canon, ok := s.ResolveLink("Authoring Content"); !ok || canon != "authoring-content" {
		t.Fatalf("space-folded ResolveLink = %q,%v want authoring-content", canon, ok)
	}
}

func TestEscapedPipeLinks(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	w("right-to-left", "---\ntitle: RTL\n---\nx\n")
	// A table cell escapes its pipe as \| ; an HTML-entity pipe also appears.
	w("tbl", "---\ntitle: T\n---\n[[Right-to-left\\|LTR]] and [[no-such&#124;Label]]\n")

	if in, _ := s.Backlinks("right-to-left"); len(in) != 1 || in[0].Slug != "tbl" {
		t.Fatalf("escaped-pipe link should resolve to right-to-left, backlinks=%+v", in)
	}
	h, err := s.Health()
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Broken) != 1 || h.Broken[0].To != "no-such" {
		t.Fatalf("broken should report the target side 'no-such', got %+v", h.Broken)
	}
}

func TestTaxonomyTags(t *testing.T) {
	tags := FrontmatterTags(map[string]any{
		"taxonomies": map[string]any{"tags": []any{"go", "wiki"}, "categories": []any{"dev"}},
	})
	want := map[string]bool{"go": true, "wiki": true, "dev": true}
	if len(tags) != 3 {
		t.Fatalf("taxonomy tags = %v, want go/wiki/dev", tags)
	}
	for _, x := range tags {
		if !want[x] {
			t.Errorf("unexpected tag %q in %v", x, tags)
		}
	}
}

func TestGraphPrimitives(t *testing.T) {
	s := seed(t)
	nb, err := s.Neighbors("index", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb) != 1 || nb[0].Slug != "guide" || nb[0].Distance != 1 {
		t.Fatalf("neighbors(index,1) = %+v, want [guide@1]", nb)
	}
	path, err := s.Path("guide", "index")
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].Slug != "guide" || path[1].Slug != "index" {
		t.Fatalf("path(guide,index) = %+v, want [guide index]", path)
	}
	if p, _ := s.Path("index", "lonely"); p != nil {
		t.Errorf("path to disconnected page should be nil, got %+v", p)
	}
	hubs, err := s.Hubs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 3 || hubs[len(hubs)-1].Slug != "lonely" || hubs[len(hubs)-1].Degree != 0 {
		t.Fatalf("hubs = %+v, want lonely last with degree 0", hubs)
	}
}

func TestTags(t *testing.T) {
	s := seed(t)
	tags, err := s.Tags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags["ops"]) != 2 {
		t.Errorf("tag ops = %+v, want 2 pages (guide, lonely)", tags["ops"])
	}
	if len(tags["onboarding"]) != 1 {
		t.Errorf("tag onboarding = %+v, want 1 page", tags["onboarding"])
	}
}
