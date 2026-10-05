package thumb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	"io"
)

// exifOrientation returns the EXIF orientation (1–8) of a JPEG, or 1 when it
// has none. Browsers apply it to the original, so the thumbnail must too.
func exifOrientation(r io.Reader) int {
	// EXIF lives in an APP1 segment near the start; never read the whole image.
	br := bufio.NewReader(io.LimitReader(r, 256<<10))
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		b, err := br.ReadByte()
		if err != nil || b != 0xFF {
			return 1
		}
		m, err := br.ReadByte()
		for err == nil && m == 0xFF { // fill bytes
			m, err = br.ReadByte()
		}
		if err != nil {
			return 1
		}
		switch {
		case m == 0xDA || m == 0xD9: // start of scan, end of image: no EXIF
			return 1
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // markers without a length
			continue
		}
		var l [2]byte
		if _, err := io.ReadFull(br, l[:]); err != nil {
			return 1
		}
		n := int(binary.BigEndian.Uint16(l[:])) - 2
		if n < 0 {
			return 1
		}
		if m != 0xE1 {
			if _, err := br.Discard(n); err != nil {
				return 1
			}
			continue
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(br, data); err != nil {
			return 1
		}
		if tiff, ok := bytes.CutPrefix(data, []byte("Exif\x00\x00")); ok {
			return tiffOrientation(tiff)
		}
	}
}

// tiffOrientation reads tag 0x0112 from IFD0 of a TIFF header.
func tiffOrientation(b []byte) int {
	if len(b) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(b[2:]) != 42 {
		return 1
	}
	size := uint64(len(b))
	off := uint64(bo.Uint32(b[4:]))
	if off+2 > size {
		return 1
	}
	n := uint64(bo.Uint16(b[off:]))
	for i := range n {
		e := off + 2 + i*12
		if e+12 > size {
			return 1
		}
		if bo.Uint16(b[e:]) != 0x0112 {
			continue
		}
		if bo.Uint16(b[e+2:]) != 3 { // SHORT
			return 1
		}
		if v := int(bo.Uint16(b[e+8:])); v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

// orient applies an EXIF orientation so the image displays upright.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			var sx, sy int
			switch o {
			case 2: // mirrored
				sx, sy = w-1-x, y
			case 3: // upside down
				sx, sy = w-1-x, h-1-y
			case 4: // mirrored, upside down
				sx, sy = x, h-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // rotate 90° clockwise to display
				sx, sy = y, h-1-x
			case 7: // transversed
				sx, sy = w-1-y, h-1-x
			case 8: // rotate 90° counter-clockwise to display
				sx, sy = w-1-y, x
			}
			si := src.PixOffset(b.Min.X+sx, b.Min.Y+sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
