// Package web embeds the compiled frontend (web/build) into the Go binary.
// Run `make web` (or `npm run build` in this directory) before building Go code.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// Dist returns the compiled frontend files, rooted at the build directory.
func Dist() (fs.FS, error) {
	return fs.Sub(build, "build")
}
