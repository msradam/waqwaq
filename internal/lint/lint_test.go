package lint

import "testing"

func TestMissingTitleIsError(t *testing.T) {
	issues := Check(map[string]any{}, "# Body\n", map[string]bool{}, Rules{})
	if !HasErrors(issues) {
		t.Fatalf("expected an error for missing title, got %v", issues)
	}
}

func TestValidPageHasNoIssues(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[known]]\n", map[string]bool{"known": true}, Rules{})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
}

func TestBrokenWikilinkIsWarning(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[ghost]] and [[ghost|alias]]\n", map[string]bool{}, Rules{})
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
	if !HasErrors(Check(fm, "", map[string]bool{}, rules)) {
		t.Fatal("missing required field should error")
	}
	fm["owner"] = "adam"
	if HasErrors(Check(fm, "", map[string]bool{}, rules)) {
		t.Fatal("present required field should not error")
	}
}

func TestBannedTerm(t *testing.T) {
	rules := Rules{BannedTerms: []BannedTerm{{Term: "lorem ipsum", Message: "no placeholder text", Severity: "error"}}}
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "this has Lorem Ipsum in it\n", map[string]bool{}, rules)
	if !HasErrors(issues) {
		t.Fatalf("banned term (error severity, case-insensitive) should error: %v", issues)
	}
	if HasErrors(Check(fm, "clean body\n", map[string]bool{}, rules)) {
		t.Fatal("clean body should not trigger banned term")
	}
}
