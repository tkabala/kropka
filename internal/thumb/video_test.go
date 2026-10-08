package thumb

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// needFFmpeg returns the ffmpeg on PATH, or skips the test.
func needFFmpeg(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	return p
}

// makeVideo encodes a test pattern of the given size and length into p.
func makeVideo(t *testing.T, ffmpeg, p, size string, seconds float64, extra ...string) {
	t.Helper()
	args := []string{"-loglevel", "error", "-y", "-f", "lavfi",
		"-i", "testsrc=duration=" + strconv.FormatFloat(seconds, 'f', -1, 64) + ":size=" + size + ":rate=25"}
	args = append(append(args, extra...), p)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v\n%s", p, err, out)
	}
}

func thumbSize(t *testing.T, p string) (int, int) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail is %q, %v; want a JPEG", format, err)
	}
	return cfg.Width, cfg.Height
}

func TestVideoThumbs(t *testing.T) {
	ffmpeg := needFFmpeg(t)
	s, dir := setup(t)
	s.SetFFmpeg(ffmpeg)
	// No -movflags +faststart: the index is at the end, so ffmpeg must seek.
	makeVideo(t, ffmpeg, filepath.Join(dir, "wide.mp4"), "640x360", 3, "-c:v", "libx264", "-pix_fmt", "yuv420p")
	// Shorter than frameAt: falls back to the first frame. Small, too: videos have no size floor.
	makeVideo(t, ffmpeg, filepath.Join(dir, "short tall.webm"), "360x640", 0.4)
	os.WriteFile(filepath.Join(dir, "broken.mp4"), make([]byte, 100<<10), 0o644)

	for name, want := range map[string][2]int{"wide.mp4": {853, 480}, "short tall.webm": {480, 853}} {
		p, err := s.Get(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w, h := thumbSize(t, p); w != want[0] || h != want[1] {
			t.Errorf("%s: %dx%d; want %dx%d", name, w, h, want[0], want[1])
		}
	}
	if _, err := s.Get(context.Background(), "broken.mp4"); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("broken video: %v; want ErrPassthrough", err)
	}
	// Remembered: the file isn't handed to ffmpeg again.
	s.SetFFmpeg(filepath.Join(t.TempDir(), "gone"))
	if _, err := s.Get(context.Background(), "broken.mp4"); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("broken video, again: %v; want ErrPassthrough", err)
	}
	if n := s.generated.Load(); n != 2 {
		t.Errorf("generated %d thumbnails; want 2", n)
	}
}

func TestVideoWithoutFFmpeg(t *testing.T) {
	s, dir := setup(t)
	os.WriteFile(filepath.Join(dir, "clip.mp4"), make([]byte, 100<<10), 0o644)
	if s.Videos() {
		t.Error("Videos() true without SetFFmpeg")
	}
	if _, err := s.Get(context.Background(), "clip.mp4"); !errors.Is(err, ErrPassthrough) {
		t.Fatalf("got %v; want ErrPassthrough", err)
	}
}

// fakeFFmpeg writes a shell script standing in for ffmpeg.
func fakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	p := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVideoFFmpegMisbehaves(t *testing.T) {
	defer func(d time.Duration, n int) { ffmpegTimeout, maxFrameBytes = d, n }(ffmpegTimeout, maxFrameBytes)
	ffmpegTimeout, maxFrameBytes = 300*time.Millisecond, 1<<10

	for name, tc := range map[string]struct{ script, want string }{
		"hangs":       {"exec sleep 10", "took over"},
		"floods":      {"exec head -c 100000 /dev/zero", "too large"},
		"says no":     {"echo 'no video stream' >&2; exit 1", "no video stream"},
		"stays quiet": {"exit 0", "no video frame"},
	} {
		t.Run(name, func(t *testing.T) {
			s, dir := setup(t)
			s.SetFFmpeg(fakeFFmpeg(t, tc.script))
			os.WriteFile(filepath.Join(dir, "v.mp4"), []byte("x"), 0o644)
			start := time.Now()
			_, err := s.Get(context.Background(), "v.mp4")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want an error containing %q", err, tc.want)
			}
			if isCtxErr(err) {
				t.Errorf("%v reads as a context error, which Get would retry forever", err)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

func TestVideoCallerGoesAway(t *testing.T) {
	s, dir := setup(t)
	s.SetFFmpeg(fakeFFmpeg(t, "exec sleep 10"))
	os.WriteFile(filepath.Join(dir, "v.mp4"), []byte("x"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.Get(ctx, "v.mp4"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v; want the caller's deadline", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("ffmpeg outlived its caller: %v", d)
	}
	// Not remembered as a failure: the next caller gets a fresh try.
	if err := s.failed(s.key("v.mp4", mustStat(t, s, "v.mp4"))); err != nil {
		t.Errorf("cancellation remembered as %v", err)
	}
}

func mustStat(t *testing.T, s *Service, rel string) os.FileInfo {
	t.Helper()
	info, err := s.root.StatFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
