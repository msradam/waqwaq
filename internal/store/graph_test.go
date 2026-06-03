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
