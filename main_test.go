package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/store"
)

// The most important doctor check is the security posture: a wiki whose web UI
// requires login but whose MCP endpoint is left open (no tokens.json).
func TestDoctorFlagsOpenMCPBehindWebAuth(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("wiki/index.md", "---\ntitle: Home\n---\nx\n")
	mk(".waqwaq/config.json", `{"web":{"users":[{"name":"a","password":"x","role":"admin"}]}}`)

	out := runDoctor(dir)
	found := false
	for _, d := range out {
		if d.label == "posture" && d.level == "fail" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a posture failure for web-auth-on + open-MCP, got %+v", out)
	}
}

func TestCheckFindsContentProblems(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(slug, content string) {
		if err := st.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	w("good", "---\ntitle: Good\n---\nclean page\n")
	w("notitle", "---\ntags: [x]\n---\nthis page has no title\n")
	w("dangling", "---\ntitle: Dangling\n---\nsee [[no-such-page]]\n")

	findings := runCheck(st, lint.Rules{})
	hasError := func(slug, substr string) bool {
		for _, f := range findings {
			if f.Slug == slug && f.Severity == "error" && strings.Contains(f.Message, substr) {
				return true
			}
		}
		return false
	}
	if !hasError("notitle", "title") {
		t.Errorf("expected a title error for the title-less page, got %+v", findings)
	}
	if !hasError("dangling", "broken wikilink") {
		t.Errorf("expected a broken-link error, got %+v", findings)
	}
	for _, f := range findings {
		if f.Slug == "good" && f.Severity == "error" {
			t.Errorf("the clean page should have no errors, got %+v", f)
		}
	}
}
