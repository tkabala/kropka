//go:build unix

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to write from run and read from the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestRun serves a directory end to end and stops on SIGINT, as Ctrl+C would.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"--port", "0", "--token", "tok", "--cache-dir", t.TempDir(), "--ffmpeg", "off", dir}, &stdout, &stderr)
	}()

	urlRE := regexp.MustCompile(`http://localhost:\d+/\?t=tok`)
	var url string
	for deadline := time.Now().Add(10 * time.Second); url == ""; {
		select {
		case err := <-done:
			t.Fatalf("run returned early: %v\nstderr: %s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no URL in banner:\n%s", stdout.String())
		}
		url = urlRE.FindString(stdout.String())
		time.Sleep(10 * time.Millisecond)
	}

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	res, err := c.Get(url) // the signal handler is installed by the time this is served
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, res.StatusCode)
	}
	res, err = c.Get(strings.TrimSuffix(url, "?t=tok") + "raw/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(body) != "hi" {
		t.Errorf("raw/hello.txt: %d %q", res.StatusCode, body)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop on SIGINT")
	}
	if !strings.Contains(stdout.String(), "bye.") {
		t.Errorf("no goodbye in:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "GET") {
		t.Errorf("requests not logged:\n%s", stderr.String())
	}
}
