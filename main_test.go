package main

import (
	"os"
	"path/filepath"
	"testing"
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
