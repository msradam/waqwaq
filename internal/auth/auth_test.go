package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenModeWhenNoFile(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Enabled() {
		t.Fatal("expected a disabled registry in open mode")
	}
	p, ok := r.Resolve("")
	if !ok || !p.Trusted || !p.Anon {
		t.Fatalf("open mode should resolve to a trusted anonymous principal, got %+v ok=%v", p, ok)
	}
}

func TestResolveTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"tokens":[
		{"token":"t-ci","name":"ci-bot","trusted":false},
		{"token":"t-adam","name":"adam","trusted":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Enabled() {
		t.Fatal("expected an enabled registry")
	}
	if p, ok := r.Resolve("Bearer t-ci"); !ok || p.Name != "ci-bot" || p.Trusted {
		t.Errorf("ci-bot: got %+v ok=%v", p, ok)
	}
	if p, ok := r.Resolve("Bearer t-adam"); !ok || p.Name != "adam" || !p.Trusted {
		t.Errorf("adam: got %+v ok=%v", p, ok)
	}
	if _, ok := r.Resolve("Bearer nope"); ok {
		t.Error("unknown token should not resolve")
	}
	if _, ok := r.Resolve(""); ok {
		t.Error("missing token should not resolve when registry is enabled")
	}
}
