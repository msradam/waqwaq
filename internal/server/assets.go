package server

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var assets embed.FS

// StaticFS returns the embedded static assets (CSS, favicon), for tools such as
// static export that copy them out of the binary.
func StaticFS() (fs.FS, error) { return fs.Sub(assets, "web/static") }
