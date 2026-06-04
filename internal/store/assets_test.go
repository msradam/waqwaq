package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVaultAsset(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("index", "---\ntitle: Home\n---\nsee ![[pic.png]]\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	// An image filed in a subdir, the way an Obsidian vault keeps attachments.
	if err := os.MkdirAll(filepath.Join(st.Root(), "Attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "Attachments", "pic.png"), []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}

	if p, ok := st.VaultAsset("pic.png"); !ok || filepath.Base(p) != "pic.png" {
		t.Fatalf("VaultAsset(pic.png) = %q,%v, want the file by basename", p, ok)
	}
	if _, ok := st.VaultAsset("PIC.PNG"); !ok {
		t.Error("asset lookup should be case-insensitive")
	}
	if _, ok := st.VaultAsset("evil.svg"); ok {
		t.Error("svg must not be served (XSS vector)")
	}
	if _, ok := st.VaultAsset("missing.png"); ok {
		t.Error("a missing asset must not resolve")
	}
}
