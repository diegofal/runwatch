// Package web holds the dashboard and the tray flyout: static files, built
// into the binary and served by internal/server.
package web

import "embed"

// Files is the web folder.
//
//go:embed *.html *.js *.css
var Files embed.FS
