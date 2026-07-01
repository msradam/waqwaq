package server

import (
	"embed"
	"io/fs"
)

//go:embed web/static web/templates
var assets embed.FS

// staticFS returns the embedded static assets (CSS, fonts, favicon).
func staticFS() (fs.FS, error) { return fs.Sub(assets, "web/static") }
