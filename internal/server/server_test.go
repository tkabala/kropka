package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tkabala/kropka/internal/auth"
	"github.com/tkabala/kropka/internal/fsview"
	"github.com/tkabala/kropka/internal/thumb"
	"github.com/tkabala/kropka/internal/watch"
)

const token = "test-token"

func newServer(t *testing.T, tok string) (*httptest.Server, string) {
	t.Helper()
	return newServerFFmpeg(t, tok, "")
}

// newServerFFmpeg is newServer with video thumbnails by the given ffmpeg.
func newServerFFmpeg(t *testing.T, tok, ffmpeg string) (*httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "pics"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pics", "a.jpg"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evil.html"), []byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte("# Notes\n\n[pic](../pics/a.jpg) [up](../evil.html) [folder](../pics) "+
		"[zip](../x.zip) [secret](../.env) [out](../../secret.txt) [sp](my%20notes.md)\n\n![p](../pics/a.jpg)\n\n<script>alert(1)</script>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := fsview.Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	thumbs, err := thumb.New(r, filepath.Join(base, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	thumbs.SetFFmpeg(ffmpeg)
	w, err := watch.New(r)
	if err != nil {
		t.Fatal(err)
	}
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>ui")}}
	ts := httptest.NewServer(New(Config{Root: r, Thumbs: thumbs, Watch: w, Token: tok, UI: ui, Version: "test", Logger: log.New(io.Discard, "", 0)}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { w.Close() }) // runs first: ends open event streams, which ts.Close would wait for
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
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 1 || body.Entries[0].Name != "a.jpg" || body.Entries[0].Kind != fsview.KindImage {
		t.Fatalf("entries = %+v", body.Entries)
	}
}

// The UI titles the page with the root folder's name, so it must not be a full Windows path.
func TestInfo(t *testing.T) {
	ts, _ := newServer(t, token)
	res, _ := authed(t, ts).Get(ts.URL + "/api/info")
	var body struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "root" || body.Version != "test" {
		t.Fatalf("info = %+v; want name root, version test", body)
	}
}

func TestListFile(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	res, _ := c.Get(ts.URL + "/api/ls?path=pics/a.jpg")
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
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
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "root", "pics", "big photo.jpg"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "root", "pics", "big.jpg"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// The cache becomes read-only after startup (a full disk behaves alike).
	dirs, _ := filepath.Glob(filepath.Join(base, "cache", "v*"))
	for _, d := range dirs {
		if err := os.Chmod(d, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(d, 0o700); err != nil {
				t.Error(err)
			}
		})
	}

	// Not 403: the original is readable, so show it.
	res, _ := noFollow.Get(ts.URL + "/thumb/pics/big.jpg?v=1")
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "/raw/pics/big.jpg" {
		t.Errorf("unwritable cache: %d %q; want 307 to /raw/pics/big.jpg", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestEvents(t *testing.T) {
	ts, base := newServer(t, token)
	c := authed(t, ts)

	for q, want := range map[string]int{"missing": 404, "pics/a.jpg": 400, "..": 400} {
		res, err := c.Get(ts.URL + "/api/events?path=" + q)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("events for %q: %d; want %d", q, res.StatusCode, want)
		}
	}

	res, err := c.Get(ts.URL + "/api/events?path=pics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); res.StatusCode != http.StatusOK || ct != "text/event-stream" {
		t.Fatalf("events: %d %q", res.StatusCode, ct)
	}
	if err := os.WriteFile(filepath.Join(base, "root", "pics", "b.jpg"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream ended without a change event")
			}
			if l == "event: change" {
				return
			}
		case <-timeout:
			t.Fatal("no change event")
		}
	}
}

func TestEventsDisabled(t *testing.T) {
	r, err := fsview.Open(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ts := httptest.NewServer(New(Config{Root: r, UI: fstest.MapFS{}}))
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// 204 makes EventSource give up instead of reconnecting forever.
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("events without a watcher: %d; want 204", res.StatusCode)
	}
}

func TestRender(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	res, err := c.Get(ts.URL + "/api/render?path=docs/notes.md")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var body struct {
		HTML      string `json:"html"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<h1 id="md-notes">`,
		`href="#/pics?view=a.jpg"`,         // viewable: opens in the viewer
		`href="#/?view=evil.html"`,         // text, in the root folder
		`href="#/pics"`,                    // a folder: its listing
		`href="/raw/x.zip"`,                // not viewable: the file itself
		`href="#/"`,                        // hidden: nothing revealed
		`href="#/"`,                        // above the root: the root
		`href="#/docs?view=my%20notes.md"`, // escaped once, decodable by the UI
		`src="/raw/pics/a.jpg"`,
	} {
		if !strings.Contains(body.HTML, want) {
			t.Errorf("missing %q in:\n%s", want, body.HTML)
		}
	}
	if strings.Contains(body.HTML, "<script") || body.Truncated {
		t.Errorf("truncated=%v, html:\n%s", body.Truncated, body.HTML)
	}

	for path, code := range map[string]int{"": 400, "docs": 404, "missing.md": 404, "../secret.txt": 400} {
		res, _ := c.Get(ts.URL + "/api/render?path=" + path)
		if res.StatusCode != code {
			t.Errorf("%q: %d; want %d", path, res.StatusCode, code)
		}
	}
}

func TestRenderTruncated(t *testing.T) {
	defer func(n int64) { renderLimit = n }(renderLimit)
	renderLimit = 5
	ts, base := newServer(t, token)
	// The limit cuts "€" (3 bytes) after its first 2.
	if err := os.WriteFile(filepath.Join(base, "root", "long.txt"), []byte("abc€def"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := authed(t, ts).Get(ts.URL + "/api/render?path=long.txt")
	var body struct {
		HTML      string `json:"html"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Truncated || body.HTML != `<pre class="plain">abc</pre>` {
		t.Fatalf("got %+v; want truncated \"abc\"", body)
	}
}

// readZip downloads a zip and returns its entries' contents by name ("" for folders).
func readZip(t *testing.T, c *http.Client, url string) (map[string]string, *zip.Reader, *http.Response) {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, res.StatusCode, data)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	return got, zr, res
}

func TestZip(t *testing.T) {
	ts, base := newServer(t, token)
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "pics", "2026", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pics", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pics", ".git", "config"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pics", "2026", "zażółć 🎉.md"), []byte(strings.Repeat("text ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "pics", "a.jpg"), old, old); err != nil {
		t.Fatal(err)
	}
	c := authed(t, ts)

	got, zr, res := readZip(t, c, ts.URL+"/zip/pics")
	if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename*=UTF-8''pics.zip` {
		t.Errorf("Content-Disposition %q", cd)
	}
	want := map[string]string{
		"pics/":                 "",
		"pics/2026/":            "",
		"pics/2026/empty/":      "",
		"pics/2026/zażółć 🎉.md": strings.Repeat("text ", 1000),
		"pics/a.jpg":            "0123456789",
	}
	if len(got) != len(want) {
		t.Fatalf("entries %q; want %q", keys(got), keys(want))
	}
	for name, body := range want {
		if b, ok := got[name]; !ok || b != body {
			t.Errorf("%s: %q (present %v)", name, b, ok)
		}
	}
	for _, f := range zr.File {
		switch f.Name {
		case "pics/a.jpg":
			if f.Method != zip.Store {
				t.Errorf("a.jpg deflated; JPEGs should be stored")
			}
			if !f.Modified.Equal(old) {
				t.Errorf("a.jpg modified %v; want %v", f.Modified, old)
			}
		case "pics/2026/zażółć 🎉.md":
			if f.Method != zip.Deflate || f.CompressedSize64 >= f.UncompressedSize64 {
				t.Errorf("text not deflated: method %d, %d → %d", f.Method, f.UncompressedSize64, f.CompressedSize64)
			}
			if f.NonUTF8 {
				t.Errorf("name not flagged UTF-8")
			}
		}
	}

	// The root: named after the served folder; nothing outside it or hidden.
	got, _, res = readZip(t, c, ts.URL+"/zip/")
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename*=UTF-8''root.zip` {
		t.Errorf("root Content-Disposition %q", cd)
	}
	if _, ok := got["root/docs/notes.md"]; !ok {
		t.Errorf("root zip lacks docs/notes.md: %q", keys(got))
	}
	for name := range got {
		if strings.Contains(name, ".git") || strings.Contains(name, "secret") {
			t.Errorf("root zip has %s", name)
		}
	}
}

func TestZipErrors(t *testing.T) {
	ts, _ := newServer(t, token)
	c := authed(t, ts)
	for path, code := range map[string]int{
		"/zip/nope":       http.StatusNotFound,
		"/zip/.git":       http.StatusNotFound,
		"/zip/pics/a.jpg": http.StatusBadRequest,
		"/zip/..%2f":      http.StatusBadRequest,
	} {
		res, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != code {
			t.Errorf("%s: %d; want %d", path, res.StatusCode, code)
		}
	}
	res, _ := client(t).Get(ts.URL + "/zip/pics")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session: %d; want 401", res.StatusCode)
	}
}

func TestZipLimits(t *testing.T) {
	defer func(b int64, n int) { zipMaxBytes, zipMaxFiles = b, n }(zipMaxBytes, zipMaxFiles)
	ts, _ := newServer(t, token)
	c := authed(t, ts)

	res, _ := c.Get(ts.URL + "/zip/?check=1")
	var ok struct{ Files, Size int64 }
	if err := json.NewDecoder(res.Body).Decode(&ok); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || ok.Files != 3 {
		t.Fatalf("check: %d %+v; want 200 and 3 files", res.StatusCode, ok)
	}

	for _, set := range []func(){
		func() { zipMaxFiles = 2 },
		func() { zipMaxFiles, zipMaxBytes = 100, 20 },
	} {
		set()
		for _, q := range []string{"?check=1", ""} {
			res, _ := c.Get(ts.URL + "/zip/" + q)
			var body struct{ Error string }
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body.Error, "too large") {
				t.Errorf("limits %d B/%d files, %q: %d %q; want 413", zipMaxBytes, zipMaxFiles, q, res.StatusCode, body.Error)
			}
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestZipMessageNumbers(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 4 << 30: "4 GB", 1536 << 20: "1.5 GB"} {
		if got := fmtBytes(n); got != want {
			t.Errorf("fmtBytes(%d) = %q; want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{7: "7", 999: "999", 1000: "1,000", 50_000: "50,000", 1234567: "1,234,567"} {
		if got := fmtCount(n); got != want {
			t.Errorf("fmtCount(%d) = %q; want %q", n, got, want)
		}
	}
}

func TestVideoThumb(t *testing.T) {
	listing := func(c *http.Client, url string) bool {
		res, err := c.Get(url + "/api/ls?path=")
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ VideoThumbs bool }
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.VideoThumbs
	}

	// Without ffmpeg: no thumbnail, and no redirect either, since <img> can't show a video.
	ts, base := newServer(t, token)
	if err := os.WriteFile(filepath.Join(base, "root", "clip.mp4"), make([]byte, 100<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	c := authed(t, ts)
	if listing(c, ts.URL) {
		t.Error("videoThumbs true without ffmpeg")
	}
	res, _ := c.Get(ts.URL + "/thumb/clip.mp4")
	if res.StatusCode != http.StatusNotFound || res.Header.Get("Cache-Control") != "private, no-cache" {
		t.Errorf("no ffmpeg: %d %q; want an uncached 404", res.StatusCode, res.Header.Get("Cache-Control"))
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ts, base = newServerFFmpeg(t, token, ffmpeg)
	c = authed(t, ts)
	if !listing(c, ts.URL) {
		t.Error("videoThumbs false with ffmpeg")
	}
	clip := filepath.Join(base, "root", "my clip.mp4")
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=25", clip).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	res, _ = c.Get(ts.URL + "/thumb/my%20clip.mp4?v=1")
	got, _, err := image.DecodeConfig(res.Body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" || err != nil {
		t.Fatalf("video thumb: %d %q %v", res.StatusCode, res.Header.Get("Content-Type"), err)
	}
	if got.Width != 640 || got.Height != thumb.Size {
		t.Errorf("video thumb %dx%d; want 640x%d", got.Width, got.Height, thumb.Size)
	}
	// A damaged video: 404, so the grid falls back to the video itself.
	if err := os.WriteFile(filepath.Join(base, "root", "broken.mp4"), make([]byte, 100<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = c.Get(ts.URL + "/thumb/broken.mp4")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("broken video: %d; want 404", res.StatusCode)
	}
}
