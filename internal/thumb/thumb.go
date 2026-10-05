// Package thumb makes grid-sized thumbnails and caches them on disk.
//
// A thumbnail is generated once per file version (path, size, mtime) and kept
// under the user cache dir. Images that are small already, too big to decode
// safely, or in a format Go cannot read report ErrPassthrough: the caller
// should serve the original instead.
package thumb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // register decoders
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	minBytes    = 64 << 10   // smaller files are served as they are
	maxPixels   = 50_000_000 // larger images are not decoded at all
	pixelBudget = 100_000_000
	maxAge      = 30 * 24 * time.Hour // unused thumbnails are pruned after this
	version     = "v1"                // bump when the output changes
)

// ErrPassthrough means there is no thumbnail and the original should be served.
var ErrPassthrough = errors.New("thumb: serve the original")

// Service generates and caches thumbnails for files under a root.
type Service struct {
	root  *fsview.Root
	dir   string
	cpu   chan struct{}       // bounds concurrent work
	mem   *semaphore.Weighted // bounds decoded pixels held at once
	group singleflight.Group

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
	s := &Service{
		root: root,
		dir:  dir,
		cpu:  make(chan struct{}, min(runtime.NumCPU(), 4)),
		mem:  semaphore.NewWeighted(pixelBudget),
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
	if info.Size() < minBytes {
		return "", ErrPassthrough
	}
	key := s.key(rel, info)
	p := filepath.Join(s.dir, key[:2], key)
	if st, err := os.Stat(p); err == nil {
		touch(p, st)
		return p, nil
	}
	for {
		// Concurrent requests for one image share a single generation. It runs
		// with the first caller's context; if that caller goes away, the others
		// retry on their own.
		ch := s.group.DoChan(key, func() (any, error) { return nil, s.generate(ctx, rel, p) })
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

func (s *Service) generate(ctx context.Context, rel, dst string) error {
	f, _, err := s.root.OpenFile(rel)
	if err != nil {
		return err
	}
	defer f.Close()

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPassthrough, err) // SVG, AVIF, HEIC, not an image...
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px > maxPixels || min(cfg.Width, cfg.Height) <= Size {
		return ErrPassthrough
	}

	if err := s.mem.Acquire(ctx, px); err != nil {
		return err
	}
	defer s.mem.Release(px)
	select {
	case s.cpu <- struct{}{}:
		defer func() { <-s.cpu }()
	case <-ctx.Done():
		return ctx.Err()
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPassthrough, err)
	}
	o := 1
	if format == "jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		o = exifOrientation(f)
	}

	sb := src.Bounds()
	tw, th := fit(sb.Dx(), sb.Dy())
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.BiLinear.Scale(out, out.Rect, src, sb, draw.Src, nil)
	out = orient(out, o)

	if err := writeAtomic(dst, out); err != nil {
		return err
	}
	s.generated.Add(1)
	return nil
}

// fit scales w×h so the short side is Size, capping the long side at maxLong.
func fit(w, h int) (int, int) {
	scale := float64(Size) / float64(min(w, h))
	if long := float64(max(w, h)) * scale; long > maxLong {
		scale = maxLong / float64(max(w, h))
	}
	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// writeAtomic encodes img as JPEG, or PNG when it has transparency, so a
// reader never sees a half-written file.
func writeAtomic(dst string, img *image.RGBA) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if img.Opaque() {
		err = jpeg.Encode(tmp, img, &jpeg.Options{Quality: 80})
	} else {
		err = png.Encode(tmp, img)
	}
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
