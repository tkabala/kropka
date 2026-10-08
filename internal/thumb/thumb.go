// Package thumb makes grid-sized thumbnails and caches them on disk.
//
// A thumbnail is generated once per file version (path, size, mtime) and kept
// under the user cache dir. Images that are small already, too big to decode
// safely, or in a format Go cannot read report ErrPassthrough: the caller
// should serve the original instead. Videos get a frame grabbed by ffmpeg
// when it is available (SetFFmpeg), and ErrPassthrough otherwise.
package thumb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register decoders
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/tkabala/kropka/internal/fsview"
)

const (
	// Size is the short side of a thumbnail in pixels. Grid tiles are square
	// and cropped with object-fit: cover, so the short side is what must stay
	// sharp: ~130 CSS px on a phone at 3x, ~240 on a desktop at 2x.
	Size = 480
	// maxLong caps the long side for panoramas and very tall strips.
	maxLong = 4 * Size

	// MinBytes is the file size below which originals are served as they are.
	// The UI uses the same threshold to skip /thumb/ for small files.
	MinBytes  = 64 << 10
	maxPixels = 50_000_000 // larger images are not decoded at all
	// memBudget bounds the bytes of decoded images held at once. It fits the
	// largest image allowed (50 MP at 16 bits per channel), so any one image
	// can always be decoded.
	memBudget = 400 << 20
	freeAfter = 64 << 20            // decodes at least this big are returned to the OS at once
	maxAge    = 30 * 24 * time.Hour // unused thumbnails are pruned after this
	version   = "v2"                // bump when the output changes

	maxFailures = 4096        // failures remembered at most
	retryAfter  = time.Minute // a failure other than ErrPassthrough is retried after this
)

var (
	// ErrPassthrough means there is no thumbnail and the original should be served.
	ErrPassthrough = errors.New("thumb: serve the original")
	// ErrCache means a thumbnail could not be stored in the cache directory.
	ErrCache = errors.New("thumb: cannot write the cache")
)

// Service generates and caches thumbnails for files under a root.
type Service struct {
	root  *fsview.Root
	dir   string
	cpu   chan struct{}       // bounds concurrent work
	mem   *semaphore.Weighted // bounds bytes of decoded images held at once
	group singleflight.Group

	mu       sync.Mutex
	failures map[string]failure // by key, so a new file version is tried afresh

	ffmpeg string // video thumbnails are off when empty

	generated atomic.Int64 // thumbnails written, for tests
}

// DefaultDir returns the cache directory used when none is configured.
func DefaultDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "kropka", "thumbs"), nil
}

// New returns a Service caching under cacheDir, and prunes stale entries in the background.
func New(root *fsview.Root, cacheDir string) (*Service, error) {
	dir := filepath.Join(cacheDir, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// An existing but read-only directory passes MkdirAll; find out now rather
	// than on every thumbnail.
	probe, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return nil, err
	}
	probe.Close()
	os.Remove(probe.Name())

	s := &Service{
		root:     root,
		dir:      dir,
		cpu:      make(chan struct{}, min(runtime.NumCPU(), 4)),
		mem:      semaphore.NewWeighted(memBudget),
		failures: make(map[string]failure),
	}
	go prune(cacheDir, time.Now())
	return s, nil
}

// Get returns the path of the cached thumbnail for rel, generating it if needed.
func (s *Service) Get(ctx context.Context, rel string) (string, error) {
	info, err := s.root.StatFile(rel)
	if err != nil {
		return "", err
	}
	// A small image is its own thumbnail. A video never is, however small.
	if video := fsview.KindOf(rel) == fsview.KindVideo; (video && s.ffmpeg == "") || (!video && info.Size() < MinBytes) {
		return "", ErrPassthrough
	}
	key := s.key(rel, info)
	p := filepath.Join(s.dir, key[:2], key)
	if st, err := os.Stat(p); err == nil {
		touch(p, st)
		return p, nil
	}
	if err := s.failed(key); err != nil {
		return "", err
	}
	for {
		// Concurrent requests for one image share a single generation. It runs
		// with the first caller's context; if that caller goes away, the others
		// retry on their own.
		ch := s.group.DoChan(key, func() (any, error) {
			err := s.generate(ctx, rel, info.Size(), p)
			if err != nil && !isCtxErr(err) {
				s.fail(key, err)
			}
			return nil, err
		})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case res := <-ch:
			if res.Err != nil && isCtxErr(res.Err) && ctx.Err() == nil {
				continue
			}
			if res.Err != nil {
				return "", res.Err
			}
			return p, nil
		}
	}
}

func (s *Service) key(rel string, info fs.FileInfo) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%d", s.root.Dir(), rel, info.Size(), info.ModTime().UnixNano(), Size)
	return hex.EncodeToString(h.Sum(nil))
}

// failure is a generation that did not produce a thumbnail. Remembering it
// saves decoding a corrupt or unsuitable image again on every request.
type failure struct {
	err   error
	until time.Time // zero: for as long as the file is unchanged
}

func (s *Service) failed(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.failures[key]
	if !ok {
		return nil
	}
	if !f.until.IsZero() && time.Now().After(f.until) {
		delete(s.failures, key)
		return nil
	}
	return f.err
}

func (s *Service) fail(key string, err error) {
	f := failure{err: err}
	// ErrPassthrough depends only on the file, which the key pins. Anything
	// else (a full disk, an I/O error) may go away.
	if !errors.Is(err, ErrPassthrough) {
		f.until = time.Now().Add(retryAfter)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failures) >= maxFailures {
		clear(s.failures) // keys of old file versions pile up; starting over is cheap
	}
	s.failures[key] = f
}

func (s *Service) generate(ctx context.Context, rel string, size int64, dst string) error {
	f, _, err := s.root.OpenFile(rel)
	if err != nil {
		return err
	}
	defer f.Close()

	video := fsview.KindOf(rel) == fsview.KindVideo
	var out *image.RGBA
	if video {
		out, err = s.videoThumb(ctx, f)
	} else {
		out, err = s.imageThumb(ctx, f)
	}
	if err != nil {
		return err
	}
	b, err := encode(out)
	if err != nil {
		return err
	}
	// Transparent images become PNG, which can outgrow a lossy original.
	// A video's thumbnail is worth having at any size: <img> can't show the video.
	if !video && int64(len(b)) >= size {
		return ErrPassthrough
	}
	if err := writeAtomic(dst, b); err != nil {
		return fmt.Errorf("%w: %v", ErrCache, err)
	}
	s.generated.Add(1)
	return nil
}

// imageThumb decodes the image in f and scales it to thumbnail size, upright.
func (s *Service) imageThumb(ctx context.Context, f *os.File) (*image.RGBA, error) {
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPassthrough, err) // SVG, AVIF, HEIC, not an image...
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels || min(cfg.Width, cfg.Height) <= Size {
		return nil, ErrPassthrough
	}

	select {
	case s.cpu <- struct{}{}:
		defer func() { <-s.cpu }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out, err := s.decodeScaled(ctx, f, cfg)
	if err != nil {
		return nil, err
	}
	if format == "jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		out = orient(out, exifOrientation(f))
	}
	return out, nil
}

// decodeScaled decodes the image in f and scales it to thumbnail size. The
// full-size image counts against the memory budget only while it is alive.
func (s *Service) decodeScaled(ctx context.Context, f io.ReadSeeker, cfg image.Config) (*image.RGBA, error) {
	n := min(decodedBytes(cfg), memBudget)
	if err := s.mem.Acquire(ctx, n); err != nil {
		return nil, err
	}
	defer s.mem.Release(n)

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPassthrough, err)
	}
	sb := src.Bounds()
	tw, th := fit(sb.Dx(), sb.Dy())
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.BiLinear.Scale(out, out.Rect, src, sb, draw.Src, nil)
	if n >= freeAfter {
		// Left to itself, the GC lets the heap grow to twice the live size, so
		// the next large image would be decoded next to this one's garbage.
		src = nil
		debug.FreeOSMemory()
	}
	return out, nil
}

// decodedBytes estimates the memory image.Decode needs for an image: a
// 16-bit PNG takes eight times as much as a grayscale JPEG of the same size.
func decodedBytes(cfg image.Config) int64 {
	var bpp int64
	switch cfg.ColorModel {
	case color.GrayModel, color.AlphaModel:
		bpp = 1
	case color.Gray16Model, color.Alpha16Model:
		bpp = 2
	case color.YCbCrModel: // 4:4:4 at worst
		bpp = 3
	case color.RGBA64Model, color.NRGBA64Model:
		bpp = 8
	default: // RGBA, NRGBA, CMYK, NYCbCrA; paletted images use less
		bpp = 4
	}
	return int64(cfg.Width) * int64(cfg.Height) * bpp
}

// fit scales w×h so the short side is Size, capping the long side at maxLong.
func fit(w, h int) (int, int) {
	scale := float64(Size) / float64(min(w, h))
	if long := float64(max(w, h)) * scale; long > maxLong {
		scale = maxLong / float64(max(w, h))
	}
	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// encode returns img as JPEG, or PNG when it has transparency.
func encode(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	if img.Opaque() {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	} else {
		err = png.Encode(&buf, img)
	}
	return buf.Bytes(), err
}

// writeAtomic writes b to dst so a reader never sees a half-written file.
func writeAtomic(dst string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// touch marks a thumbnail as used so pruning keeps it, at most once a day.
func touch(p string, st fs.FileInfo) {
	if now := time.Now(); now.Sub(st.ModTime()) > 24*time.Hour {
		_ = os.Chtimes(p, now, now)
	}
}

// prune deletes thumbnails unused for maxAge and leftover temp files. It only
// touches files kropka creates, inside version directories, so a cache dir
// pointed somewhere unexpected loses nothing else.
func prune(cacheDir string, now time.Time) {
	dirs, _ := filepath.Glob(filepath.Join(cacheDir, "v[0-9]*"))
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			age := now.Sub(info.ModTime())
			name := e.Name()
			if (isKey(name) && age > maxAge) || (strings.HasSuffix(name, ".tmp") && age > time.Hour) {
				_ = os.Remove(p)
			}
			return nil
		})
	}
}

func isKey(name string) bool {
	if len(name) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
