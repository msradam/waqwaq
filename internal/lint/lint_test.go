package lint

import (
	"strings"
	"testing"
)

func TestTitleErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		fm   map[string]any
		body string
		want string
	}{
		{"int", map[string]any{"title": 42}, "", "not a number"},
		{"list", map[string]any{"title": []any{"a", "b"}}, "", "not a list"},
		{"malformed", nil, "---\nbad: [\n---\n", "could not be parsed"},
		{"none", nil, "# H1 only\n", "no frontmatter"},
		{"empty", map[string]any{"title": "  "}, "", "is empty"},
	}
	for _, c := range cases {
		got := Check(c.fm, c.body, knows(), Rules{})
		if len(got) != 1 || !strings.Contains(got[0].Message, c.want) {
			t.Errorf("%s: got %v, want a message containing %q", c.name, got, c.want)
		}
	}
}

// knows builds a resolves predicate that accepts exactly the given targets.
func knows(targets ...string) func(string) bool {
	set := make(map[string]bool, len(targets))
	for _, t := range targets {
		set[t] = true
	}
	return func(t string) bool { return set[t] }
}

func TestMissingTitleIsError(t *testing.T) {
	issues := Check(map[string]any{}, "# Body\n", knows(), Rules{})
	if !HasErrors(issues) {
		t.Fatalf("expected an error for missing title, got %v", issues)
	}
}

func TestValidPageHasNoIssues(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[known]]\n", knows("known"), Rules{})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
}

func TestBrokenWikilinkIsWarning(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[ghost]] and [[ghost|alias]]\n", knows(), Rules{})
	if HasErrors(issues) {
		t.Fatalf("a broken link should warn, not error: %v", issues)
	}
	if len(issues) != 1 || issues[0].Severity != "warning" {
		t.Fatalf("expected one warning (deduped), got %v", issues)
	}
}

func TestRequiredFrontmatterField(t *testing.T) {
	rules := Rules{RequireFrontmatter: []string{"owner"}}
	fm := map[string]any{"title": "Hello"}
	if !HasErrors(Check(fm, "", knows(), rules)) {
		t.Fatal("missing required field should error")
	}
	fm["owner"] = "adam"
	if HasErrors(Check(fm, "", knows(), rules)) {
		t.Fatal("present required field should not error")
	}
}

func TestBannedTerm(t *testing.T) {
	rules := Rules{BannedTerms: []BannedTerm{{Term: "lorem ipsum", Message: "no placeholder text", Severity: "error"}}}
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "this has Lorem Ipsum in it\n", knows(), rules)
	if !HasErrors(issues) {
		t.Fatalf("banned term (error severity, case-insensitive) should error: %v", issues)
	}
	if HasErrors(Check(fm, "clean body\n", knows(), rules)) {
		t.Fatal("clean body should not trigger banned term")
	}
}
