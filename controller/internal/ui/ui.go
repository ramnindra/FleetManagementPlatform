// Package ui embeds the same-origin web UI served at /ui/.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static
var content embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(content, "static")
	return sub
}
