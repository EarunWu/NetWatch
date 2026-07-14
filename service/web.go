package main

import (
	"embed"
	"io/fs"
)

// staticFiles is replaced at build time by copying the exported dashboard to
// service/web. Keeping it embedded lets the service remain a single executable.
//
//go:embed all:web
var staticFiles embed.FS

func dashboardFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "web")
}
