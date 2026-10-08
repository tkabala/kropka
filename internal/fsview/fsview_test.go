package fsview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// layout:
//
//	outside/secret.txt
//	root/a.jpg
//	root/sub/b.mp4
//	root/.hidden/c.txt
//	root/escape -> ../outside        (symlink out of root)
//	root/inside -> sub               (symlink within root)
func setup(t *testing.T, showHidden bool) *Root {
	t.Helper()
	base := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(base, "outside"), 0o755))
	must(os.WriteFile(filepath.Join(base, "outside", "secret.txt"), []byte("secret"), 0o644))
	must(os.MkdirAll(filepath.Join(base, "root", "sub"), 0o755))
	must(os.MkdirAll(filepath.Join(base, "root", ".hidden"), 0o755))
	must(os.WriteFile(filepath.Join(base, "root", "a.jpg"), []byte("jpg"), 0o644))
	must(os.WriteFile(filepath.Join(base, "root", "sub", "b.mp4"), []byte("mp4"), 0o644))
	must(os.WriteFile(filepath.Join(base, "root", ".hidden", "c.txt"), []byte("c"), 0o644))
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(base, "root", "escape")); err != nil {
		t.Skipf("symlinks not supported here: %v", err) // e.g. Windows without developer mode
	}
	must(os.Symlink("sub", filepath.Join(base, "root", "inside")))

	r, err := Open(filepath.Join(base, "root"), showHidden)
	must(err)
	t.Cleanup(func() { r.Close() })
	return r
}

func TestClean(t *testing.T) {
	r := setup(t, false)
	ok := map[string]string{
		"":         ".",
		"/":        ".",
		"sub":      "sub",
		"/sub/":    "sub",
		"sub//x":   "sub/x",
		"./sub/./": "sub",
	}
	for in, want := range ok {
		got, err := r.Clean(in)
		if err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"..", "../outside", "sub/../../x", "/..", "a\\..\\b", "x\x00y"}
	for _, in := range bad {
		if _, err := r.Clean(in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Clean(%q) err = %v; want ErrInvalidPath", in, err)
		}
	}
	if _, err := r.Clean(".hidden/c.txt"); !errors.Is(err, ErrHidden) {
		t.Errorf("hidden path: err = %v; want ErrHidden", err)
	}
}

func TestListSkipsHiddenAndEscapingLinks(t *testing.T) {
	r := setup(t, false)
	entries, err := r.List(".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Kind{}
	for _, e := range entries {
		got[e.Name] = e.Kind
	}
	want := map[string]Kind{"sub": KindDir, "inside": KindDir, "a.jpg": KindImage}
	if len(got) != len(want) {
		t.Fatalf("entries = %v; want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: kind %q; want %q", k, got[k], v)
		}
	}
	// Folders come first.
	if entries[0].Kind != KindDir || entries[len(entries)-1].Kind == KindDir {
		t.Errorf("folders not listed first: %+v", entries)
	}
}

func TestListShowsHiddenWhenEnabled(t *testing.T) {
	r := setup(t, true)
	entries, err := r.List(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == ".hidden" {
			return
		}
	}
	t.Error(".hidden not listed with showHidden")
}

func TestOpenFileCannotEscape(t *testing.T) {
	r := setup(t, false)
	if _, _, err := r.OpenFile("escape/secret.txt"); err == nil {
		t.Fatal("read through an escaping symlink")
	}
	f, _, err := r.OpenFile("inside/b.mp4")
	if err != nil {
		t.Fatalf("symlink inside root: %v", err)
	}
	f.Close()
	if _, _, err := r.OpenFile("sub"); err == nil {
		t.Error("OpenFile on a directory should fail")
	}
}

func TestOpenFileRejectsFIFOWithoutBlocking(t *testing.T) {
	r := setup(t, false)
	if err := mkfifo(filepath.Join(r.Dir(), "pipe")); err != nil {
		t.Skipf("mkfifo not supported here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := r.OpenFile("pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("OpenFile on a FIFO: got %v, want ErrNotExist", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OpenFile blocked on a FIFO")
	}
}

func TestWalk(t *testing.T) {
	r := setup(t, false)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink("../a.jpg", filepath.Join(r.Dir(), "sub", "pic.jpg")))
	must(os.Symlink("../outside/secret.txt", filepath.Join(r.Dir(), "sub", "leak.txt")))
	must(os.MkdirAll(filepath.Join(r.Dir(), "sub", "empty"), 0o755))
	if err := mkfifo(filepath.Join(r.Dir(), "sub", "pipe")); err != nil {
		t.Logf("no FIFO in this walk: %v", err)
	}

	var got []string
	must(r.Walk(".", func(rel string, info os.FileInfo) error {
		if info.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
		return nil
	}))
	// No hidden folder, no escaping link, no link to a folder ("inside"), no FIFO.
	// sub/pic.jpg points at a.jpg, which is a file inside the root.
	want := []string{"a.jpg", "sub/", "sub/b.mp4", "sub/empty/", "sub/pic.jpg"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("walked %q; want %q", got, want)
	}
}
