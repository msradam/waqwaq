// Package config loads optional per-wiki settings from .waqwaq/config.json.
// A missing file yields built-in defaults. This baseline is read-only, so there
// is no auth, theme, lint, webhook, or review configuration: just identity.
//
// waqwaq is an OKF server and enforces OKF compliance by default. `"lenient":
// true` opts a wiki out of enforcement, to serve plain markdown that is not an
// OKF bundle.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	Title       string `json:"title"`           // display name and MCP server identity
	Description string `json:"mcp_description"` // one-liner for MCP instructions
	Addr        string `json:"addr"`            // default listen address for serve
	Lenient     bool   `json:"lenient"`         // serve non-OKF-compliant markdown (opt out of enforcement)
}

// Load reads <dir>/.waqwaq/config.json. A missing file is not an error.
func Load(dir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(dir, ".waqwaq", "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}
