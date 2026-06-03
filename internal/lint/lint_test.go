package lint

import "testing"

func TestMissingTitleIsError(t *testing.T) {
	issues := Check(map[string]any{}, "# Body\n", map[string]bool{})
	if !HasErrors(issues) {
		t.Fatalf("expected an error for missing title, got %v", issues)
	}
}

func TestValidPageHasNoIssues(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[known]]\n", map[string]bool{"known": true})
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
}

func TestBrokenWikilinkIsWarning(t *testing.T) {
	fm := map[string]any{"title": "Hello"}
	issues := Check(fm, "see [[ghost]] and [[ghost|alias]]\n", map[string]bool{})
	if HasErrors(issues) {
		t.Fatalf("a broken link should warn, not error: %v", issues)
	}
	if len(issues) != 1 || issues[0].Severity != "warning" {
		t.Fatalf("expected one warning (deduped), got %v", issues)
	}
}
