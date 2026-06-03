package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const assetsDirName = "assets"

// allowedAssetExt is a conservative whitelist. SVG is excluded on purpose: it
// can carry scripts and would be an XSS vector when served from our origin.
var allowedAssetExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".pdf": true,
}

// AddAsset stores an uploaded file under assets/ with a content-addressed name
// (so identical uploads dedupe), commits it, and returns the stored file name.
func (s *Store) AddAsset(data []byte, originalName string) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	if !allowedAssetExt[ext] {
		return "", fmt.Errorf("unsupported file type %q", ext)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:6]) + ext

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.gitRoot, assetsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	if err := s.commit("waqwaq: add asset "+name, "", filepath.Join(assetsDirName, name)); err != nil {
		return "", err
	}
	return name, nil
}

// AssetPath returns the on-disk path for a stored asset, validating the name.
func (s *Store) AssetPath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid asset name %q", name)
	}
	return filepath.Join(s.gitRoot, assetsDirName, name), nil
}
