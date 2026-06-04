package server

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var assets embed.FS

// StaticFS returns the embedded static assets (CSS, favicon).
func StaticFS() (fs.FS, error) { return fs.Sub(assets, "web/static") }
