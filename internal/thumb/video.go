package thumb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

// frameAt is where a video's thumbnail is taken from: the first frame is
// often black or a fade-in. Clips shorter than this use their first frame.
const frameAt = time.Second

var (
	// ffmpegTimeout bounds one frame grab; a damaged file can make ffmpeg crawl.
	ffmpegTimeout = 30 * time.Second
	maxFrameBytes = 32 << 20 // a 480 px RGB PNG is well under 2 MB
)

// SetFFmpeg turns on video thumbnails, taken by the ffmpeg binary at path.
// Call it before the Service is used; an empty path leaves them off.
func (s *Service) SetFFmpeg(path string) { s.ffmpeg = path }

// Videos reports whether video thumbnails are on.
func (s *Service) Videos() bool { return s.ffmpeg != "" }

// videoThumb grabs a frame of the video in f, scaled to thumbnail size.
func (s *Service) videoThumb(ctx context.Context, f *os.File) (*image.RGBA, error) {
	select {
	case s.cpu <- struct{}{}:
		defer func() { <-s.cpu }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var b []byte
	var err error
	for _, at := range []time.Duration{frameAt, 0} {
		if b, err = s.grabFrame(ctx, f, at); err == nil && len(b) > 0 {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPassthrough, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: no video frame", ErrPassthrough)
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: frame: %v", ErrPassthrough, err)
	}
	sb := src.Bounds()
	tw, th := fit(sb.Dx(), sb.Dy())
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.BiLinear.Scale(out, out.Rect, src, sb, draw.Src, nil)
	return out, nil
}

// grabFrame runs ffmpeg for one frame at the given time, as a PNG with its
// short side at Size. ffmpeg reads the file kropka already opened through
// os.Root (as its stdin), never a path, so it can't be steered out of the
// served folder; it seeks in it as in any file, which MP4s without
// "faststart" need. Rotation metadata is applied, and so is a non-square
// pixel aspect ratio.
func (s *Service) grabFrame(ctx context.Context, f *os.File, at time.Duration) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	tctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	size := strconv.Itoa(Size)
	//nolint:gosec // G204: ffmpeg is the configured binary and every argument is fixed
	cmd := exec.CommandContext(tctx, s.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", strconv.FormatFloat(at.Seconds(), 'f', -1, 64),
		"-i", stdinInput,
		"-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1",
		"-vf", "scale=iw*sar:ih,scale="+size+":"+size+":force_original_aspect_ratio=increase",
		"-pix_fmt", "rgb24", "-f", "image2pipe", "-c:v", "png", "pipe:1")
	cmd.Stdin = f
	var stdout limitedBuffer
	stdout.max = maxFrameBytes
	var stderr limitedBuffer
	stderr.max = 4 << 10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case tctx.Err() != nil:
		// Not a context error to the caller: Get would retry it forever.
		return nil, fmt.Errorf("ffmpeg took over %v", ffmpegTimeout)
	case stdout.over:
		return nil, errors.New("ffmpeg: frame too large")
	case err != nil:
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// limitedBuffer keeps the first max bytes written to it and drops the rest,
// so a misbehaving ffmpeg can't fill memory. The buffer is a named field, not
// embedded: an embedded bytes.Buffer would lend it ReadFrom, which io.Copy
// prefers to Write, and the limit would never be checked.
type limitedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.over = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }
