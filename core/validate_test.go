package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func validateFixture(t *testing.T, files map[string]string) Core {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, "wiki", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidateCompliant(t *testing.T) {
	c := validateFixture(t, map[string]string{
		"index.md":    "# Home\n[a](tables/a.md)\n", // reserved, no type: fine
		"tables/a.md": "---\ntitle: A\ntype: BigQuery Table\n---\nlinks [b](../tables/b.md)\n",
		"tables/b.md": "---\ntitle: B\ntype: BigQuery Table\n---\nbody\n",
	})
	rep, err := Validate(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("should be compliant, got violations: %v", rep.Compliance)
	}
	if len(rep.BrokenLinks) != 0 {
		t.Fatalf("no broken links expected, got %v", rep.BrokenLinks)
	}
	if rep.Pages != 2 { // index is reserved, not a concept
		t.Fatalf("want 2 concept pages, got %d", rep.Pages)
	}
}

func TestValidateMissingType(t *testing.T) {
	c := validateFixture(t, map[string]string{
		"tables/a.md": "---\ntitle: A\ntype: BigQuery Table\n---\nok\n",
		"orphan.md":   "---\ntitle: Orphan\n---\nno type\n",
	})
	rep, err := Validate(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("bundle with a typeless concept page must not be compliant")
	}
	if len(rep.Compliance) != 1 || rep.Compliance[0] != "orphan: missing OKF type" {
		t.Fatalf("expected orphan flagged, got %v", rep.Compliance)
	}
}

func TestValidateBrokenLinkIsWarningNotViolation(t *testing.T) {
	c := validateFixture(t, map[string]string{
		"tables/a.md": "---\ntitle: A\ntype: BigQuery Table\n---\nlinks [gone](../tables/missing.md)\n",
	})
	rep, err := Validate(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	// A broken link does not break compliance (SPEC §9 forbids rejecting for it).
	if !rep.OK() {
		t.Fatalf("broken link must not be a compliance violation: %v", rep.Compliance)
	}
	if len(rep.BrokenLinks) != 1 {
		t.Fatalf("expected 1 broken link warning, got %v", rep.BrokenLinks)
	}
}

func TestValidateStrict(t *testing.T) {
	c := validateFixture(t, map[string]string{
		"tables/a.md": "---\ntype: BigQuery Table\n---\nno title/desc/timestamp\n",
	})
	rep, err := Validate(context.Background(), c, true)
	if err != nil {
		t.Fatal(err)
	}
	// Compliant (has type) but strict flags the missing recommended fields.
	// Title is not flagged: it always resolves via the slug fallback.
	if !rep.OK() {
		t.Fatal("has type, should be compliant")
	}
	if len(rep.StrictMisses) != 2 {
		t.Fatalf("strict should flag description and timestamp; got %v", rep.StrictMisses)
	}
}
