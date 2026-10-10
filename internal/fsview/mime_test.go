package fsview

import (
	"strings"
	"testing"
)

func TestMIMEAndKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		mime string // prefix
		kind Kind
	}{
		{"a.JPG", "image/jpeg", KindImage},
		{"a.webp", "image/webp", KindImage},
		{"a.svg", "image/svg+xml", KindImage},
		{"a.mov", "video/quicktime", KindVideo},
		{"a.mkv", "video/x-matroska", KindVideo},
		{"a.flac", "audio/flac", KindAudio},
		{"a.pdf", "application/pdf", KindPDF},
		{"README.md", "text/markdown", KindText},
		{"a.yml", "text/yaml", KindText},
		{"a.toml", "text/plain", KindText},
		{"a.zig", "text/plain", KindText},
		{"a.json", "application/json", KindText}, // text by extension, not MIME
		{"a.bin", "application/octet-stream", KindOther},
		{"Makefile", "application/octet-stream", KindOther},
		{"a.unknownext", "application/octet-stream", KindOther},
	} {
		if m := MIMEOf(tc.name); !strings.HasPrefix(m, tc.mime) {
			t.Errorf("MIMEOf(%q) = %q; want %s…", tc.name, m, tc.mime)
		}
		if k := KindOf(tc.name); k != tc.kind {
			t.Errorf("KindOf(%q) = %q; want %q", tc.name, k, tc.kind)
		}
	}
}
