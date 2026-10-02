// Package ui embeds the browser frontend into the binary.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS returns the frontend files rooted at static/.
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
