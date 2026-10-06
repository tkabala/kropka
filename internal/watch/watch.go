// Package watch tells listeners when the contents of a folder change.
//
// Only folders someone is looking at are watched: every Subscribe adds a
// reference, and the OS watch is dropped with the last one. Bursts of events
// (a copy in progress, a script writing many files) are coalesced into one
// notification.
package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/tkabala/kropka/internal/fsview"
)

// A notification goes out once events stop for Quiet, and at the latest MaxWait
// after the first one, so a folder that keeps changing still updates.
var (
	Quiet   = 300 * time.Millisecond
	MaxWait = 2 * time.Second
)

// ErrClosed is returned by Subscribe after Close.
var ErrClosed = errors.New("watcher closed")

// Watcher watches folders of a Root on behalf of subscribers.
type Watcher struct {
	root     *fsview.Root
	realRoot string // root.Dir() with symlinks resolved
	fsw      *fsnotify.Watcher

	// osMu serializes fsw.Add and fsw.Remove so they happen in the order the
	// reference counts changed. It is never held together with mu while
	// calling fsw: on some platforms those calls wait for fsnotify's reader,
	// which may be waiting for loop, which may be waiting for mu.
	osMu sync.Mutex

	mu     sync.Mutex
	dirs   map[string]*dir // by real path (symlinks resolved), as given to fsnotify
	closed bool
}

type dir struct {
	abs   string
	info  fs.FileInfo // the folder watched, to notice another one taking its path
	subs  map[chan struct{}]struct{}
	first time.Time // first event of the pending burst; zero when none is pending
	timer *time.Timer
}

// New starts a watcher for root.
func New(root *fsview.Root) (*Watcher, error) {
	realRoot, err := filepath.EvalSymlinks(root.Dir())
	if err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{root: root, realRoot: realRoot, fsw: fsw, dirs: map[string]*dir{}}
	go w.loop()
	return w, nil
}

// Subscribe watches the folder rel (as returned by Root.Clean). The channel
// receives a value after its listing changes; when the folder is removed it
// receives a last one and is closed. Close closes it too. Call stop when done.
func (w *Watcher) Subscribe(rel string) (changes <-chan struct{}, stop func(), err error) {
	// Check through os.Root, so a symlink that leaves the root is never watched.
	info, err := w.root.StatDir(rel)
	if err != nil {
		return nil, nil, err
	}
	abs, err := w.realPath(rel)
	if err != nil {
		return nil, nil, err
	}

	w.osMu.Lock()
	defer w.osMu.Unlock()
	w.mu.Lock()
	d := w.dirs[abs]
	closed := w.closed
	replaced := d != nil && !os.SameFile(d.info, info)
	if replaced {
		// Another folder took the path, e.g. after a parent was renamed, which
		// the OS doesn't report. The watch still follows the old folder, so
		// end it: its subscribers reload and subscribe to the new one.
		w.notify(d)
		w.drop(d)
		d = nil
	}
	w.mu.Unlock()
	if closed {
		return nil, nil, ErrClosed
	}
	if replaced {
		_ = w.fsw.Remove(abs)
	}
	existing := d != nil
	if !existing {
		if err := w.fsw.Add(abs); err != nil {
			return nil, nil, err
		}
		d = &dir{abs: abs, info: info, subs: map[chan struct{}]struct{}{}}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.closed:
		return nil, nil, ErrClosed
	case existing && w.dirs[abs] != d:
		return nil, nil, fs.ErrNotExist // removed while we weren't holding mu
	}
	w.dirs[abs] = d
	// One slot is enough: pending notifications for a subscriber are merged.
	c := make(chan struct{}, 1)
	d.subs[c] = struct{}{}

	var once sync.Once
	stop = func() { once.Do(func() { w.unsubscribe(d, c) }) }
	return c, stop, nil
}

// realPath resolves symlinks in rel, so that every name of a folder maps to the
// same watch: the OS keeps only one per folder, and removing it for one name
// would remove it for all.
func (w *Watcher) realPath(rel string) (string, error) {
	abs, err := filepath.EvalSymlinks(filepath.Join(w.realRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	// StatDir already checked this through os.Root; a symlink swapped in since
	// then must not get the watch out.
	if r, err := filepath.Rel(w.realRoot, abs); err != nil || !filepath.IsLocal(r) {
		return "", fsview.ErrInvalidPath
	}
	return abs, nil
}

func (w *Watcher) unsubscribe(d *dir, c chan struct{}) {
	w.osMu.Lock()
	defer w.osMu.Unlock()
	w.mu.Lock()
	if w.dirs[d.abs] != d {
		w.mu.Unlock()
		return // already dropped: the folder is gone or the watcher is closed
	}
	delete(d.subs, c)
	last := len(d.subs) == 0
	if last {
		w.drop(d)
	}
	w.mu.Unlock()
	if last {
		_ = w.fsw.Remove(d.abs)
	}
}

// drop forgets d and closes the channels of its remaining subscribers. The
// caller holds w.mu and removes the OS watch, if there still is one.
func (w *Watcher) drop(d *dir) {
	if d.timer != nil {
		d.timer.Stop()
	}
	for c := range d.subs {
		close(c)
	}
	delete(w.dirs, d.abs)
}

// Close stops all watches and closes every subscriber's channel.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for _, d := range w.dirs {
		w.drop(d)
	}
	w.mu.Unlock()
	return w.fsw.Close()
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// The kernel queue overflowed and events were lost: anything may
			// have changed.
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.mu.Lock()
				for _, d := range w.dirs {
					w.schedule(d)
				}
				w.mu.Unlock()
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	// Permission and timestamp changes don't show in a listing, and some tools
	// (indexers, backup software) produce lots of them.
	if !ev.Has(fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// The watched folder itself was removed or moved away.
	if d := w.dirs[ev.Name]; d != nil && ev.Has(fsnotify.Remove|fsnotify.Rename) {
		w.notify(d) // so clients reload and find it gone, rather than show a stale listing
		w.drop(d)
		// Not here: Remove may wait for the reader, which waits for this loop.
		go w.forget(d.abs)
	}
	if w.root.Hidden(filepath.Base(ev.Name)) {
		return // editor swap files, .DS_Store and the like
	}
	if d := w.dirs[filepath.Dir(ev.Name)]; d != nil {
		w.schedule(d)
	}
}

// forget removes the OS watch on abs unless it has been subscribed to again.
// Removal fails harmlessly when the OS already dropped the watch with the folder.
func (w *Watcher) forget(abs string) {
	w.osMu.Lock()
	defer w.osMu.Unlock()
	w.mu.Lock()
	_, again := w.dirs[abs]
	w.mu.Unlock()
	if !again {
		_ = w.fsw.Remove(abs)
	}
}

// schedule arranges a notification for d's subscribers. The caller holds w.mu.
func (w *Watcher) schedule(d *dir) {
	now := time.Now()
	if d.first.IsZero() {
		d.first = now
	}
	delay := min(Quiet, d.first.Add(MaxWait).Sub(now))
	if d.timer == nil {
		d.timer = time.AfterFunc(delay, func() { w.fire(d) })
	} else {
		d.timer.Reset(delay)
	}
}

func (w *Watcher) fire(d *dir) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A Reset that raced with the timer firing runs fire twice; the second
	// run finds nothing pending.
	if w.dirs[d.abs] != d || d.first.IsZero() {
		return
	}
	w.notify(d)
}

// notify wakes d's subscribers. The caller holds w.mu.
func (w *Watcher) notify(d *dir) {
	d.first = time.Time{}
	for c := range d.subs {
		select {
		case c <- struct{}{}:
		default: // a notification is already waiting
		}
	}
}
