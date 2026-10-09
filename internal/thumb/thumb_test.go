package thumb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/tkabala/kropka/internal/fsview"
)

func setup(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := fsview.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	s, err := New(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// noisy returns an opaque w×h image with random pixels, so its encoding is
// large enough not to be passed through as a small file.
func noisy(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(r.IntN(256))
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	return img
}

func writeJPEG(t *testing.T, p string, img image.Image, app1 []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if app1 != nil { // insert right after SOI
		b = append(append(append([]byte{}, b[:2]...), app1...), b[2:]...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// exifApp1 builds an APP1 segment holding only an orientation tag.
func exifApp1(o uint16) []byte {
	tiff := []byte("II*\x00\x08\x00\x00\x00")
	tiff = binary.LittleEndian.AppendUint16(tiff, 1)      // one entry
	tiff = binary.LittleEndian.AppendUint16(tiff, 0x0112) // orientation
	tiff = binary.LittleEndian.AppendUint16(tiff, 3)      // SHORT
	tiff = binary.LittleEndian.AppendUint32(tiff, 1)      // count
	tiff = binary.LittleEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0) // value padding, next IFD
	data := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(data)+2))
	return append(seg, data...)
}

func decodeFile(t *testing.T, p string) (image.Image, string) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img, format
}

func TestGenerateJPEG(t *testing.T) {
	s, dir := setup(t)
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(720, 540), nil)

	p, err := s.Get(context.Background(), "a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	img, format := decodeFile(t, p)
	if format != "jpeg" || img.Bounds().Dx() != 640 || img.Bounds().Dy() != Size {
		t.Fatalf("got %s %v; want jpeg 640x%d", format, img.Bounds().Size(), Size)
	}

	// Cached: same file, no second generation.
	p2, err := s.Get(context.Background(), "a.jpg")
	if err != nil || p2 != p || s.generated.Load() != 1 {
		t.Fatalf("second Get: %q %v, generated %d", p2, err, s.generated.Load())
	}

	// A new version of the file gets a new thumbnail.
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(540, 720), nil)
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "a.jpg"), future, future); err != nil {
		t.Fatal(err)
	}
	p3, err := s.Get(context.Background(), "a.jpg")
	if err != nil || p3 == p {
		t.Fatalf("after change: %q %v; want a new path", p3, err)
	}
}

func TestOrientation(t *testing.T) {
	s, dir := setup(t)
	writeJPEG(t, filepath.Join(dir, "r.jpg"), noisy(720, 540), exifApp1(6))
	p, err := s.Get(context.Background(), "r.jpg")
	if err != nil {
		t.Fatal(err)
	}
	img, _ := decodeFile(t, p)
	if got := img.Bounds().Size(); got != image.Pt(Size, 640) {
		t.Fatalf("rotated thumbnail is %v; want %dx640", got, Size)
	}
}

func TestTransparentPNG(t *testing.T) {
	s, dir := setup(t)
	img := noisy(600, 600)
	img.Pix[3] = 0 // one transparent pixel is enough
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := s.Get(context.Background(), "t.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, format := decodeFile(t, p); format != "png" {
		t.Fatalf("format %s; want png to keep transparency", format)
	}
}

func TestPassthrough(t *testing.T) {
	s, dir := setup(t)
	// Big image, small file (a flat colour compresses to almost nothing).
	writeJPEG(t, filepath.Join(dir, "small.jpg"), image.NewRGBA(image.Rect(0, 0, 1200, 800)), nil)
	// Big file, small image: nothing to gain from a thumbnail.
	writeJPEG(t, filepath.Join(dir, "narrow.jpg"), noisy(4000, 300), nil)
	if err := os.WriteFile(filepath.Join(dir, "tiny.png"), []byte("not even an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.svg"), bytes.Repeat([]byte("<svg/>"), 20000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.jpg"), append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, 100<<10)...), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"small.jpg", "narrow.jpg", "tiny.png", "x.svg", "broken.jpg"} {
		if _, err := s.Get(context.Background(), name); !errors.Is(err, ErrPassthrough) {
			t.Errorf("%s: err %v; want ErrPassthrough", name, err)
		}
	}
	if _, err := s.Get(context.Background(), "missing.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v; want ErrNotExist", err)
	}
	if s.generated.Load() != 0 {
		t.Errorf("generated %d thumbnails; want 0", s.generated.Load())
	}
}

func TestConcurrentGetGeneratesOnce(t *testing.T) {
	s, dir := setup(t)
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(720, 540), nil)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.Get(context.Background(), "a.jpg"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := s.generated.Load(); n != 1 {
		t.Fatalf("generated %d times; want 1", n)
	}
}

func TestCancelledCallerDoesNotFailOthers(t *testing.T) {
	s, dir := setup(t)
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(720, 540), nil)
	s.cpu <- struct{}{} // hold every worker so generation has to wait
	for range cap(s.cpu) - 1 {
		s.cpu <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := s.Get(ctx, "a.jpg"); first <- err }()
	time.Sleep(20 * time.Millisecond) // let the first caller start the shared generation
	second := make(chan error, 1)
	go func() { _, err := s.Get(context.Background(), "a.jpg"); second <- err }()
	time.Sleep(20 * time.Millisecond)

	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first: %v; want Canceled", err)
	}
	for range cap(s.cpu) {
		<-s.cpu
	}
	if err := <-second; err != nil {
		t.Fatalf("second caller failed with the first one: %v", err)
	}
}

func TestOrient(t *testing.T) {
	// 3x2 source, pixels numbered row by row:
	//   0 1 2
	//   3 4 5
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range 6 {
		src.Pix[i*4] = byte(i)
	}
	want := map[int][]string{
		1: {"012", "345"},
		2: {"210", "543"},
		3: {"543", "210"},
		4: {"345", "012"},
		5: {"03", "14", "25"},
		6: {"30", "41", "52"},
		7: {"52", "41", "30"},
		8: {"25", "14", "03"},
	}
	for o, rows := range want {
		got := orient(src, o)
		var lines []string
		for y := range got.Bounds().Dy() {
			var line []byte
			for x := range got.Bounds().Dx() {
				line = append(line, '0'+got.Pix[got.PixOffset(x, y)])
			}
			lines = append(lines, string(line))
		}
		if len(lines) != len(rows) {
			t.Errorf("orientation %d: %v; want %v", o, lines, rows)
			continue
		}
		for i := range rows {
			if lines[i] != rows[i] {
				t.Errorf("orientation %d: %v; want %v", o, lines, rows)
				break
			}
		}
	}
}

func TestExifOrientationMalformed(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0xFF, 0xD8},
		{0xFF, 0xD8, 0xFF, 0xE1, 0x00},       // truncated length
		{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01}, // length below 2
		append([]byte{0xFF, 0xD8}, exifApp1(9)...), // out of range
	} {
		if o := exifOrientation(bytes.NewReader(b)); o != 1 {
			t.Errorf("%x: orientation %d; want 1", b, o)
		}
	}
	if o := exifOrientation(bytes.NewReader(append([]byte{0xFF, 0xD8}, exifApp1(8)...))); o != 8 {
		t.Errorf("valid EXIF: orientation %d; want 8", o)
	}
}

func TestFit(t *testing.T) {
	for _, c := range []struct{ w, h, tw, th int }{
		{1200, 800, 720, 480},
		{800, 1200, 480, 720},
		{1000, 1000, 480, 480},
		{20000, 1000, 1920, 96}, // panorama: long side capped
	} {
		if tw, th := fit(c.w, c.h); tw != c.tw || th != c.th {
			t.Errorf("fit(%d, %d) = %d, %d; want %d, %d", c.w, c.h, tw, th, c.tw, c.th)
		}
	}
}

func TestPrune(t *testing.T) {
	cache := t.TempDir()
	old := time.Now().Add(-2 * maxAge)
	files := map[string]bool{ // path → should survive
		"v1/ab/" + string(bytes.Repeat([]byte("ab"), 32)): false,
		"v1/cd/" + string(bytes.Repeat([]byte("cd"), 32)): true, // recent
		"v1/ab/x.tmp":   false,
		"v1/notes.txt":  true, // not ours
		"unrelated.jpg": true, // outside version dirs
	}
	for p, keep := range files {
		full := filepath.Join(cache, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !keep || p == "v1/notes.txt" || p == "unrelated.jpg" {
			if err := os.Chtimes(full, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	prune(cache, time.Now())
	for p, keep := range files {
		_, err := os.Stat(filepath.Join(cache, p))
		if exists := err == nil; exists != keep {
			t.Errorf("%s: exists=%v; want %v", p, exists, keep)
		}
	}
}

func TestFailureRemembered(t *testing.T) {
	s, dir := setup(t)
	// Valid header, garbage after: DecodeConfig passes, Decode fails.
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(720, 540), nil)
	b, _ := os.ReadFile(filepath.Join(dir, "a.jpg"))
	if err := os.WriteFile(filepath.Join(dir, "broken.jpg"), b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(context.Background(), "broken.jpg"); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("first Get: %v; want ErrPassthrough", err)
	}
	info, _ := s.root.StatFile("broken.jpg")
	key := s.key("broken.jpg", info)
	if err := s.failed(key); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("failure not remembered: %v", err)
	}

	// Other failures are retried after a while.
	s.fail(key, ErrCache)
	s.mu.Lock()
	f := s.failures[key]
	f.until = time.Now().Add(-time.Second)
	s.failures[key] = f
	s.mu.Unlock()
	if err := s.failed(key); err != nil {
		t.Fatalf("expired failure still reported: %v", err)
	}
}

func TestThumbnailLargerThanOriginal(t *testing.T) {
	s, dir := setup(t)
	// A paletted PNG stores a byte per pixel; its RGBA thumbnail, blended by
	// scaling and kept as PNG for the transparency, takes far more.
	pal := color.Palette{color.NRGBA{}}
	for i := 1; i < 256; i++ {
		pal = append(pal, color.NRGBA{byte(i), byte(i * 7), byte(i * 13), 255})
	}
	img := image.NewPaletted(image.Rect(0, 0, 600, 600), pal)
	r := rand.New(rand.NewPCG(3, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(r.IntN(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(context.Background(), "p.png"); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("err %v; want ErrPassthrough for a thumbnail bigger than the original", err)
	}
	if n := s.generated.Load(); n != 0 {
		t.Fatalf("stored %d thumbnails; want 0", n)
	}
}

func TestUnwritableCache(t *testing.T) {
	needReadOnlyDirs(t)
	dir := t.TempDir()
	root, err := fsview.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, version), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(cache, version), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(cache, version), 0o700); err != nil {
			t.Error(err)
		}
	})
	if _, err := New(root, cache); err == nil {
		t.Fatal("New succeeded with a read-only cache dir")
	}
}

func TestCacheWriteError(t *testing.T) {
	needReadOnlyDirs(t)
	s, dir := setup(t)
	writeJPEG(t, filepath.Join(dir, "a.jpg"), noisy(720, 540), nil)
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(s.dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	_, err := s.Get(context.Background(), "a.jpg")
	// Not ErrPermission: that would read as "the image is not readable".
	if !errors.Is(err, ErrCache) || errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err %v; want ErrCache only", err)
	}
}

func TestDecodedBytes(t *testing.T) {
	for _, c := range []struct {
		m    color.Model
		want int64
	}{
		{color.GrayModel, 100},
		{color.YCbCrModel, 300},
		{color.NRGBAModel, 400},
		{color.NRGBA64Model, 800},
		{color.Palette{color.Black}, 400},
	} {
		if got := decodedBytes(image.Config{ColorModel: c.m, Width: 10, Height: 10}); got != c.want {
			t.Errorf("%T: %d; want %d", c.m, got, c.want)
		}
	}
}

// needReadOnlyDirs skips a test that relies on a read-only directory refusing
// new files: root ignores the mode, and on Windows it is only an attribute.
func needReadOnlyDirs(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("read-only directories are not enforced here")
	}
}
