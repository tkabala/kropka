//go:build unix

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
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
	url, done, stdout, stderr := startRun(t, "http", dir)

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

	// A connection that never sends a request, like a browser's spare one,
	// must not keep run from stopping.
	idle, err := net.Dial("tcp", strings.TrimPrefix(strings.TrimSuffix(url, "/?t=tok"), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()

	interrupt(t, done)
	if !strings.Contains(stdout.String(), "bye.") {
		t.Errorf("no goodbye in:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "GET") {
		t.Errorf("requests not logged:\n%s", stderr.String())
	}
}

// TestRunTLS serves over HTTPS with --tls-cert and --tls-key.
func TestRunTLS(t *testing.T) {
	certFile, keyFile := writeTestCert(t, "localhost", "127.0.0.1")
	url, done, stdout, _ := startRun(t, "https", "--tls-cert", certFile, "--tls-key", keyFile, t.TempDir())
	if strings.Contains(stdout.String(), "not valid for") {
		t.Errorf("certificate warning for a covered host:\n%s", stdout.String())
	}

	pem, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	c := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true},
		Timeout:   5 * time.Second,
		// Stop at the token exchange to see its cookie.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("GET %s: %d", url, res.StatusCode)
	}
	if res.ProtoMajor != 2 {
		t.Errorf("served over %s; want HTTP/2", res.Proto)
	}
	if cookies := res.Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("token cookie not Secure: %v", res.Header["Set-Cookie"])
	}

	// Plain HTTP to the TLS port gets an error, not the files.
	res, err = http.Get("http" + strings.TrimPrefix(url, "https"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP: %d; want 400", res.StatusCode)
	}

	interrupt(t, done)
}

// startRun runs kropka with args (plus a fixed token, a free port and no
// thumbnails or ffmpeg) and waits for the scheme://localhost URL in its banner.
func startRun(t *testing.T, scheme string, args ...string) (url string, done chan error, stdout, stderr *syncBuffer) {
	t.Helper()
	stdout, stderr = &syncBuffer{}, &syncBuffer{}
	done = make(chan error, 1)
	args = append([]string{"--port", "0", "--token", "tok", "--cache-dir", t.TempDir(), "--ffmpeg", "off"}, args...)
	go func() { done <- run(args, stdout, stderr) }()

	urlRE := regexp.MustCompile(scheme + `://localhost:\d+/\?t=tok`)
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
	return url, done, stdout, stderr
}

// interrupt sends SIGINT, as Ctrl+C would, and waits for run to return.
func interrupt(t *testing.T, done chan error) {
	t.Helper()
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
}
