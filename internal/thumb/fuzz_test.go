package thumb

import (
	"bytes"
	"testing"
)

// FuzzExifOrientation feeds arbitrary bytes to the EXIF parser, which reads
// untrusted JPEGs: it must not panic and must always return 1–8.
func FuzzExifOrientation(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF, 0xD8})
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01})
	for _, o := range []uint16{1, 6, 8, 9} {
		f.Add(append([]byte{0xFF, 0xD8}, exifApp1(o)...))
		f.Add(exifApp1(o)[10:]) // the bare TIFF header, past marker, length and "Exif\0\0"
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if o := exifOrientation(bytes.NewReader(b)); o < 1 || o > 8 {
			t.Fatalf("exifOrientation = %d", o)
		}
		// Straight at the TIFF parser too, so the fuzzer needn't find a valid
		// JPEG and APP1 header first.
		if o := tiffOrientation(b); o < 1 || o > 8 {
			t.Fatalf("tiffOrientation = %d", o)
		}
	})
}
