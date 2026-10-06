package watch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tkabala/kropka/internal/fsview"
)

func newWatcher(t *testing.T) (*Watcher, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := fsview.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dir
}

func subscribe(t *testing.T, w *Watcher, rel string) <-chan struct{} {
	t.Helper()
	c, stop, err := w.Subscribe(rel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return c
}

func write(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case _, ok := <-c:
		if !ok {
			t.Fatalf("%s: channel closed", what)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no notification", what)
	}
}

func expectNone(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("%s: unexpected notification", what)
	case <-time.After(3 * Quiet):
	}
}

func TestNotifiesOncePerBurst(t *testing.T) {
	w, dir := newWatcher(t)
	c := subscribe(t, w, ".")
	for i := range 20 {
		write(t, filepath.Join(dir, string(rune('a'+i))+".jpg"))
	}
	expect(t, c, "burst")
	expectNone(t, c, "after burst")
}

func TestMaxWait(t *testing.T) {
	w, dir := newWatcher(t)
	c := subscribe(t, w, ".")
	// Events never stop for Quiet, yet a notification must still go out.
	deadline := time.Now().Add(MaxWait + 3*time.Second)
	got := false
	for i := 0; time.Now().Before(deadline) && !got; i++ {
		write(t, filepath.Join(dir, "log.txt"))
		select {
		case <-c:
			got = true
		case <-time.After(Quiet / 3):
		}
	}
	if !got {
		t.Fatal("no notification while the folder kept changing")
	}
}

func TestScope(t *testing.T) {
	w, dir := newWatcher(t)
	sub := subscribe(t, w, "sub")
	root := subscribe(t, w, ".")

	write(t, filepath.Join(dir, ".hidden.swp"))
	expectNone(t, root, "hidden file")

	write(t, filepath.Join(dir, "a.txt"))
	expect(t, root, "file in root")
	expectNone(t, sub, "file in parent")

	write(t, filepath.Join(dir, "sub", "b.txt"))
	expect(t, sub, "file in sub")
}

func TestRefCount(t *testing.T) {
	w, dir := newWatcher(t)
	c1, stop1, err := w.Subscribe("sub")
	if err != nil {
		t.Fatal(err)
	}
	c2, stop2, err := w.Subscribe("sub")
	if err != nil {
		t.Fatal(err)
	}
	stop1()
	stop1() // idempotent
	write(t, filepath.Join(dir, "sub", "a.txt"))
	expect(t, c2, "remaining subscriber")
	expectNone(t, c1, "stopped subscriber")

	stop2()
	if l := w.fsw.WatchList(); len(l) != 0 {
		t.Errorf("still watching %v after the last subscriber left", l)
	}
}

func TestFolderRemoved(t *testing.T) {
	w, dir := newWatcher(t)
	c := subscribe(t, w, "sub")
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Skip("can't remove a watched folder here:", err)
	}
	deadline := time.After(5 * time.Second)
	notified := false
	for {
		select {
		case _, ok := <-c:
			if ok {
				notified = true
				continue
			}
			if !notified {
				t.Error("channel closed without a last notification")
			}
			// Recreated: a new subscription must work again.
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			c = subscribe(t, w, "sub")
			write(t, filepath.Join(dir, "sub", "a.txt"))
			expect(t, c, "recreated folder")
			return
		case <-deadline:
			t.Fatal("channel not closed after the folder was removed")
		}
	}
}

func TestSubscribeErrors(t *testing.T) {
	w, dir := newWatcher(t)
	write(t, filepath.Join(dir, "f.txt"))
	if _, _, err := w.Subscribe("f.txt"); !errors.Is(err, fsview.ErrNotDir) {
		t.Errorf("file: %v; want ErrNotDir", err)
	}
	if _, _, err := w.Subscribe("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v; want ErrNotExist", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err == nil {
		if _, _, err := w.Subscribe("out"); err == nil {
			t.Error("subscribed to a symlink leaving the root")
		}
	}
}

func TestClose(t *testing.T) {
	w, _ := newWatcher(t)
	c := subscribe(t, w, ".")
	w.Close()
	select {
	case _, ok := <-c:
		if ok {
			t.Fatal("got a notification instead of a closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed by Close")
	}
	if _, _, err := w.Subscribe("."); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after Close: %v; want ErrClosed", err)
	}
}

// The OS keeps one watch per folder, so two names for it must share it.
func TestSymlinkAlias(t *testing.T) {
	w, dir := newWatcher(t)
	if err := os.Symlink("sub", filepath.Join(dir, "alias")); err != nil {
		t.Skip("can't create symlinks here:", err)
	}
	_, stopReal, err := w.Subscribe("sub")
	if err != nil {
		t.Fatal(err)
	}
	alias := subscribe(t, w, "alias")
	write(t, filepath.Join(dir, "sub", "a.txt"))
	expect(t, alias, "alias while the real name is watched too")

	stopReal()
	write(t, filepath.Join(dir, "sub", "b.txt"))
	expect(t, alias, "alias after the real name was dropped")
}

func TestRenameRecreate(t *testing.T) {
	w, dir := newWatcher(t)
	c := subscribe(t, w, "sub")
	if err := os.Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "old")); err != nil {
		t.Skip("can't rename a watched folder here:", err)
	}
	for range c {
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	c = subscribe(t, w, "sub")
	write(t, filepath.Join(dir, "old", "y.txt"))
	expectNone(t, c, "file in the folder moved away")
	write(t, filepath.Join(dir, "sub", "x.txt"))
	expect(t, c, "file in the recreated folder")
}

// Renaming a parent moves a watched folder without telling its watch.
func TestParentRenamed(t *testing.T) {
	w, dir := newWatcher(t)
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := subscribe(t, w, "a/b")
	if err := os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "a.prev")); err != nil {
		t.Skip("can't rename a watched folder's parent here:", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := subscribe(t, w, "a/b")

	// The old subscriber is told to reload, then let go.
	expect(t, old, "subscriber of the replaced folder")
	select {
	case _, ok := <-old:
		if ok {
			t.Fatal("old subscription still open")
		}
	case <-time.After(time.Second):
		t.Fatal("old subscription not closed")
	}

	write(t, filepath.Join(dir, "a", "b", "new.jpg"))
	expect(t, c, "file in the recreated folder")
	write(t, filepath.Join(dir, "a.prev", "b", "late.jpg"))
	expectNone(t, c, "file in the folder moved away")
}
