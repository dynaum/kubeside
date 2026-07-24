// Package web embeds the built frontend so kubeside ships as one binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist, or false when the frontend has not
// been built into this binary (only the placeholder is present).
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	// A build with real assets has an assets/ directory; the committed
	// placeholder does not.
	if _, err := fs.Stat(sub, "assets"); err != nil {
		return sub, false
	}
	return sub, true
}
