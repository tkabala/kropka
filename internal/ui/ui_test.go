package ui

import (
	"io/fs"
	"testing"
)

func TestFS(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "style.css", "favicon.svg"} {
		b, err := fs.ReadFile(FS(), name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if len(b) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}
