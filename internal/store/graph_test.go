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
