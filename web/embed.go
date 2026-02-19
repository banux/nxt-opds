// Package web embeds the static frontend assets.
package web

import "embed"

// FS holds the embedded web directory contents.
//
//go:embed index.html
var FS embed.FS
