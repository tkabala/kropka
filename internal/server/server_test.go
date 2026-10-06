package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tkabala/kropka/internal/auth"
	"github.com/tkabala/kropka/internal/fsview"
	"github.com/tkabala/kropka/internal/thumb"
)

const token = "test-token"

func newServer(t *testing.T, tok string) (*httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "pics"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(root, "pics", "a.jpg"), []byte("0123456789"), 0o644)
	os.WriteFile(filepath.Join(root, "evil.html"), []byte("<script>alert(1)</script>"), 0o644)

	r, err := fsview.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	thumbs, err := thumb.New(r, filepath.Join(base, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>ui")}}
	ts := httptest.NewServer(New(Config{Root: r, Thumbs: thumbs, Token: tok, UI: ui, Version: "test"}))
	t.Cleanup(ts.Close)
	return ts, base
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func TestAuthFlow(t *testing.T) {
	ts, _ := newServer(t, token)
	c := client(t)

	res, _ := c.Get(ts.URL + "/api/ls")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d; want 401", res.StatusCode)
	}
	res, _ = c.Get(ts.URL + "/?t=wrong")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d; want 401", res.StatusCode)
	}

	// Correct token: redirected to a token-free URL, cookie set.
	noFollow := &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ = noFollow.Get(ts.URL + "/?" + auth.QueryParam + "=" + token)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("token: %d; want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); strings.Contains(loc, token) {
		t.Errorf("redirect keeps token in URL: %s", loc)
	}
	// Strict would be withheld on the redirect when the link is opened from
	// another app, e.g. a QR scanner.
	if cs := res.Cookies(); len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v; want one HttpOnly, SameSite=Lax cookie", cs)
	}

	res, _ = c.Get(ts.URL + "/api/ls")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("with cookie: %d; want 200", res.StatusCode)
	}
}

func authed(t *testing.T, ts *httptest.Server) *http.Client {
	c := client(t)
	if _, err := c.Get(ts.URL + "/?t=" + token); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestList(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	res, _ := c.Get(ts.URL + "/api/ls?path=pics")
	var body struct {
		Entries []fsview.Entry `json:"entries"`
	}
	json.NewDecoder(res.Body).Decode(&body)
	if len(body.Entries) != 1 || body.Entries[0].Name != "a.jpg" || body.Entries[0].Kind != fsview.KindImage {
		t.Fatalf("entries = %+v", body.Entries)
	}
}

func TestListFile(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	res, _ := c.Get(ts.URL + "/api/ls?path=pics/a.jpg")
	var body struct {
		Error string `json:"error"`
	}
	json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != http.StatusBadRequest || body.Error != fsview.ErrNotDir.Error() {
		t.Fatalf("ls on a file: %d %q; want 400 %q", res.StatusCode, body.Error, fsview.ErrNotDir)
	}
}

func TestTraversalBlocked(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	for _, p := range []string{
		"/raw/../secret.txt",
		"/raw/%2e%2e/secret.txt",
		"/raw/pics/%2e%2e/%2e%2e/secret.txt",
		"/raw/..%2fsecret.txt",
		"/thumb/../secret.txt",
		"/thumb/%2e%2e/secret.txt",
		"/api/ls?path=..",
		"/api/ls?path=pics/../..",
	} {
		res, err := c.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode == http.StatusOK || strings.Contains(string(b), "secret") {
			t.Errorf("%s: status %d, body %q", p, res.StatusCode, b)
		}
	}
}

func TestRawRangeAndSandbox(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)

	req, _ := http.NewRequest("GET", ts.URL+"/raw/pics/a.jpg", nil)
	req.Header.Set("Range", "bytes=2-4")
	res, _ := c.Do(req)
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || string(b) != "234" {
		t.Errorf("range: %d %q; want 206 \"234\"", res.StatusCode, b)
	}

	res, _ = c.Get(ts.URL + "/raw/evil.html")
	if csp := res.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
		t.Errorf("html served without sandbox CSP: %q", csp)
	}

	res, _ = c.Get(ts.URL + "/raw/pics/a.jpg?dl=1")
	if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("dl=1: Content-Disposition %q", cd)
	}
}

func TestNoAuth(t *testing.T) {
	ts, _ := newServer(t, "")
	res, _ := http.Get(ts.URL + "/api/ls")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("no-auth: %d", res.StatusCode)
	}
	if res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing Referrer-Policy")
	}
}

func TestThumb(t *testing.T) {
	ts, base := newServer(t, token)
	c := authed(t, ts)
	noFollow := &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// A large image gets a thumbnail.
	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7919 % 251)
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100})
	os.WriteFile(filepath.Join(base, "root", "pics", "big photo.jpg"), buf.Bytes(), 0o644)

	res, _ := c.Get(ts.URL + "/thumb/pics/big%20photo.jpg?v=1")
	got, _, err := image.DecodeConfig(res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" || err != nil {
		t.Fatalf("thumb: %d %q %v", res.StatusCode, res.Header.Get("Content-Type"), err)
	}
	if got.Height != thumb.Size {
		t.Errorf("thumb height %d; want %d", got.Height, thumb.Size)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned thumb Cache-Control %q; want immutable", cc)
	}

	// A small file is not worth a thumbnail: redirect to the original.
	res, _ = noFollow.Get(ts.URL + "/thumb/pics/a.jpg")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "/raw/pics/a.jpg" {
		t.Errorf("small: %d %q; want 307 to /raw/pics/a.jpg", res.StatusCode, res.Header.Get("Location"))
	}

	for p, want := range map[string]int{
		"/thumb/pics/missing.jpg": http.StatusNotFound,
		"/thumb/.hidden.jpg":      http.StatusNotFound,
		"/thumb/pics":             http.StatusNotFound,
		"/thumb/":                 http.StatusBadRequest,
	} {
		res, _ := noFollow.Get(ts.URL + p)
		if res.StatusCode != want {
			t.Errorf("%s: %d; want %d", p, res.StatusCode, want)
		}
	}
}

func TestThumbCacheFailure(t *testing.T) {
	// Root ignores the mode, and on Windows it is only an attribute.
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("read-only directories are not enforced here")
	}
	ts, base := newServer(t, token)
	c := authed(t, ts)
	noFollow := &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7919 % 251)
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100})
	os.WriteFile(filepath.Join(base, "root", "pics", "big.jpg"), buf.Bytes(), 0o644)

	// The cache becomes read-only after startup (a full disk behaves alike).
	dirs, _ := filepath.Glob(filepath.Join(base, "cache", "v*"))
	for _, d := range dirs {
		os.Chmod(d, 0o500)
		t.Cleanup(func() { os.Chmod(d, 0o700) })
	}

	// Not 403: the original is readable, so show it.
	res, _ := noFollow.Get(ts.URL + "/thumb/pics/big.jpg?v=1")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "/raw/pics/big.jpg" {
		t.Errorf("unwritable cache: %d %q; want 307 to /raw/pics/big.jpg", res.StatusCode, res.Header.Get("Location"))
	}
}
