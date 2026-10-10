package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkabala/kropka/internal/fsview"
)

func TestMakeURL(t *testing.T) {
	for _, tc := range []struct {
		host, token, want string
		port              int
	}{
		{"localhost", "abc", "http://localhost:8080/?t=abc", 8080},
		{"localhost", "", "http://localhost:8080/", 8080},
		{"192.168.1.5", "x-y_z", "http://192.168.1.5:9000/?t=x-y_z", 9000},
		{"::1", "abc", "http://[::1]:8080/?t=abc", 8080},
	} {
		if got := makeURL(tc.host, tc.port, tc.token); got != tc.want {
			t.Errorf("makeURL(%q, %d, %q) = %q; want %q", tc.host, tc.port, tc.token, got, tc.want)
		}
	}
}

func TestIndent(t *testing.T) {
	for in, want := range map[string]string{
		"":         "  ",
		"a":        "  a",
		"a\nb":     "  a\n  b",
		"a\nb\n":   "  a\n  b\n",
		"██\n▀▀\n": "  ██\n  ▀▀\n",
	} {
		if got := indent(in, "  "); got != want {
			t.Errorf("indent(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("KROPKA_TEST_INT", "1234")
	if got := envInt("KROPKA_TEST_INT", 7); got != 1234 {
		t.Errorf("set: %d; want 1234", got)
	}
	t.Setenv("KROPKA_TEST_INT", "nope")
	if got := envInt("KROPKA_TEST_INT", 7); got != 7 {
		t.Errorf("invalid: %d; want 7", got)
	}
	t.Setenv("KROPKA_TEST_INT", "")
	if got := envInt("KROPKA_TEST_INT", 7); got != 7 {
		t.Errorf("empty: %d; want 7", got)
	}
}

func TestBanner(t *testing.T) {
	t.Run("localhost", func(t *testing.T) {
		b := banner("/srv/pics", "127.0.0.1", 8080, "tok", false)
		for _, want := range []string{"serving /srv/pics", "→ http://localhost:8080/?t=tok", "ssh -L 8080:localhost:8080", "Ctrl+C"} {
			if !strings.Contains(b, want) {
				t.Errorf("missing %q in:\n%s", want, b)
			}
		}
		for _, bad := range []string{"--no-auth", "█"} {
			if strings.Contains(b, bad) {
				t.Errorf("unexpected %q in:\n%s", bad, b)
			}
		}
	})
	t.Run("no auth", func(t *testing.T) {
		b := banner("/srv", "127.0.0.1", 8080, "", false)
		if !strings.Contains(b, "access token disabled") {
			t.Errorf("no warning in:\n%s", b)
		}
		if strings.Contains(b, "?t=") {
			t.Errorf("token in URL:\n%s", b)
		}
	})
	t.Run("custom bind", func(t *testing.T) {
		b := banner("/srv", "10.0.0.2", 8081, "tok", false)
		if !strings.Contains(b, "http://10.0.0.2:8081/?t=tok") || strings.Contains(b, "ssh -L") {
			t.Errorf("banner:\n%s", b)
		}
	})
	t.Run("lan with QR", func(t *testing.T) {
		b := banner("/srv", "0.0.0.0", 8080, "tok", true)
		if !strings.Contains(b, "http://localhost:8080/?t=tok") {
			t.Errorf("no localhost URL in:\n%s", b)
		}
		for _, ip := range lanIPs() {
			if !strings.Contains(b, makeURL(ip, 8080, "tok")) {
				t.Errorf("no URL for %s in:\n%s", ip, b)
			}
		}
		if !strings.ContainsAny(b, "█▀▄") {
			t.Errorf("no QR code in:\n%s", b)
		}
		if strings.Contains(b, "ssh -L") {
			t.Errorf("ssh hint with --lan:\n%s", b)
		}
	})
}

func TestLanIPs(t *testing.T) {
	for _, ip := range lanIPs() {
		p := net.ParseIP(ip)
		if p == nil || p.To4() == nil || p.IsLoopback() {
			t.Errorf("lanIPs returned %q", ip)
		}
	}
}

func TestListen(t *testing.T) {
	ln, port, err := listen("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if port == 0 {
		t.Fatal("port 0 not resolved")
	}

	// A busy port moves on to the next free one.
	ln2, port2, err := listen("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln2.Close() }()
	if port2 <= port || port2 >= port+20 {
		t.Errorf("busy %d: got %d", port, port2)
	}

	if _, _, err := listen("256.0.0.1", 0); err == nil {
		t.Error("bad address: no error")
	}
}

func TestFindFFmpeg(t *testing.T) {
	if p, err := findFFmpeg("off"); p != "" || err != nil {
		t.Errorf("off: %q, %v", p, err)
	}
	if _, err := findFFmpeg(""); err != nil {
		t.Errorf("default: %v", err)
	}
	if _, err := findFFmpeg("kropka-no-such-ffmpeg"); err == nil {
		t.Error("missing named ffmpeg: no error")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		if p, err := findFFmpeg(sh); p != sh || err != nil {
			t.Errorf("path: %q, %v", p, err)
		}
	}
}

func TestOpenThumbs(t *testing.T) {
	root, err := fsview.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	cache := t.TempDir()
	if _, err := openThumbs(cache, root); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(cache); len(entries) == 0 {
		t.Error("cache dir not used")
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := openThumbs("", root); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openThumbs(file, root); err == nil {
		t.Error("cache dir is a file: no error")
	}
}

func TestRunExitsEarly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
		stdout  string
		stderr  string
	}{
		{"version", []string{"--version"}, false, "kropka " + version, ""},
		{"help", []string{"-h"}, false, "", "Usage: kropka [flags] [dir]"},
		{"bad flag", []string{"--nope"}, true, "", "flag provided but not defined"},
		{"two dirs", []string{"a", "b"}, true, "", ""},
		{"missing dir", []string{filepath.Join(t.TempDir(), "nope")}, true, "", ""},
		{"bad port", []string{"--bind", "256.0.0.1", "--cache-dir", t.TempDir(), t.TempDir()}, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tc.args, &stdout, &stderr)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v; want error: %v", err, tc.wantErr)
			}
			if !strings.Contains(stdout.String(), tc.stdout) {
				t.Errorf("stdout %q; want %q", stdout.String(), tc.stdout)
			}
			if !strings.Contains(stderr.String(), tc.stderr) {
				t.Errorf("stderr %q; want %q", stderr.String(), tc.stderr)
			}
		})
	}
}
