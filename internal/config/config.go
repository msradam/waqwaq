// Package config loads optional per-wiki settings from .waqwaq/config.json. A
// missing file yields built-in defaults.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/msradam/waqwaq/internal/lint"
)

type Config struct {
	Title   string     `json:"title"`   // brand and page-title suffix
	Accent  string     `json:"accent"`  // a lokta pigment name or any CSS color
	Theme   string     `json:"theme"`   // auto, a lokta stock name, or light/dark (paper/ink)
	Addr    string     `json:"addr"`    // default listen address
	Review  bool       `json:"review"`  // default to queuing writes for review
	Webhook string     `json:"webhook"` // URL notified when a write is queued for review
	Web     Web        `json:"web"`     // web-UI access control
	Lint    lint.Rules `json:"lint"`
}

// accents are the lokta pigment names the accent setting accepts, each paired
// with whether an accent ground in that pigment needs light text to stay
// readable. The *-light variants are the dial options lokta provides for the
// dark stocks.
var accents = map[string]struct {
	color     string
	lightText bool
}{
	"marigold":       {"#FBBC0E", false},
	"peach":          {"#E7A079", false},
	"lavender":       {"#A99CB3", false},
	"madder":         {"#8E3B30", true},
	"walnut":         {"#4E3B29", true},
	"turmeric":       {"#7A5A12", true},
	"lac":            {"#9B2D4D", true},
	"aubergine":      {"#6B4E8E", true},
	"cinnabar":       {"#C23A26", true},
	"celadon":        {"#6E8B6F", true},
	"indigo":         {"#2E3E5C", true},
	"madder-light":   {"#DE9684", false},
	"walnut-light":   {"#C9A982", false},
	"turmeric-light": {"#E0B452", false},
	"lac-light":      {"#D98098", false},
}

// ResolveAccent maps a named lokta pigment to its color and the on-accent text
// color override ("" keeps the stock's dark ink). Any other non-empty value
// passes through as a CSS color with the default dark text.
func ResolveAccent(v string) (color, textOn string) {
	a, ok := accents[strings.ToLower(strings.TrimSpace(v))]
	if !ok {
		return v, ""
	}
	if a.lightText {
		return a.color, "#FAF8EA"
	}
	return a.color, ""
}

// Web configures web-UI access control. When ProxyHeader is set, identity comes
// from that reverse-proxy header (delegate SSO to the proxy); otherwise the UI
// is open and trusts local/loopback access.
type Web struct {
	ProxyHeader string    `json:"proxy_header"`
	DefaultRole string    `json:"default_role"` // viewer | editor | admin
	Admins      []string  `json:"admins"`
	Editors     []string  `json:"editors"`
	Users       []WebUser `json:"users"` // built-in login, when no reverse proxy
}

type WebUser struct {
	Name     string `json:"name"`
	Password string `json:"password"` // bcrypt hash, generate with `waqwaq passwd`
	Role     string `json:"role"`     // viewer | editor | admin
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
