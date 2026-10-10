package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"log"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

func TestZipFailed(t *testing.T) {
	for _, tc := range []struct {
		err    error
		logged bool
	}{
		{errors.New("disk on fire"), true},
		{syscall.EPIPE, false},
		{syscall.ECONNRESET, false},
	} {
		var buf bytes.Buffer
		s := &srv{cfg: Config{Logger: log.New(&buf, "", 0)}}
		func() {
			defer func() {
				if r := recover(); r != http.ErrAbortHandler {
					t.Errorf("%v: panicked with %v; want ErrAbortHandler", tc.err, r)
				}
			}()
			s.zipFailed("pics", tc.err)
		}()
		if got := strings.Contains(buf.String(), "zip of pics"); got != tc.logged {
			t.Errorf("%v: logged %q; want logged=%v", tc.err, buf.String(), tc.logged)
		}
	}

	// No logger configured: still aborts.
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Errorf("nil logger: panicked with %v; want ErrAbortHandler", r)
		}
	}()
	(&srv{}).zipFailed("pics", errors.New("x"))
}

func TestZipMethod(t *testing.T) {
	for name, want := range map[string]uint16{
		"a.jpg":    zip.Store,
		"a.MP4":    zip.Store,
		"a.mp3":    zip.Store,
		"a.pdf":    zip.Store,
		"a.tar.gz": zip.Store,
		"a.docx":   zip.Store,
		"a.svg":    zip.Deflate,
		"a.bmp":    zip.Deflate,
		"a.txt":    zip.Deflate,
		"a.bin":    zip.Deflate,
		"Makefile": zip.Deflate,
	} {
		if got := zipMethod(name); got != want {
			t.Errorf("zipMethod(%q) = %d; want %d", name, got, want)
		}
	}
}
