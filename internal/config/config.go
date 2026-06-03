// Package config loads optional per-wiki settings from .waqwaq/config.json. A
// missing file yields built-in defaults, so zero-config use is unchanged. The
// file is the whole tuning surface: appearance, defaults, and lint rules.
package config

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/msradam/waqwaq/internal/lint"
)

type Config struct {
	Title   string     `json:"title"`   // brand and page-title suffix
	Accent  string     `json:"accent"`  // hex color for links and highlights
	Theme   string     `json:"theme"`   // auto | light | dark
	Addr    string     `json:"addr"`    // default listen address
	Review  bool       `json:"review"`  // default to queuing writes for review
	Webhook string     `json:"webhook"` // URL notified when a write is queued for review
	Web     Web        `json:"web"`     // web-UI access control
	Lint    lint.Rules `json:"lint"`
}

// Web configures web-UI access control. When ProxyHeader is set, identity comes
// from that reverse-proxy header (delegate SSO to the proxy); otherwise the UI
// is open and trusts local/loopback access.
type Web struct {
	ProxyHeader string   `json:"proxy_header"`
	DefaultRole string   `json:"default_role"` // viewer | editor | admin
	Admins      []string `json:"admins"`
	Editors     []string `json:"editors"`
}

func Default() Config {
	return Config{Theme: "auto"}
}

// Load reads a config file. A missing file returns defaults.
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.Theme == "" {
		c.Theme = "auto"
	}
	return c, nil
}
