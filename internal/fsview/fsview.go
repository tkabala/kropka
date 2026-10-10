// Package fsview gives read-only, traversal-safe access to the served directory.
//
// All access goes through os.Root, which guarantees that no path (including
// one resolved through symlinks) can escape the root directory.
package fsview

import (
	"errors"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Kind is a coarse file category used by the UI to pick a renderer.
type Kind string

// The kinds an Entry can have.
const (
	KindDir   Kind = "dir"
	KindImage Kind = "image"
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindPDF   Kind = "pdf"
	KindText  Kind = "text"
	KindOther Kind = "other"
)

// Entry is a single item in a directory listing.
type Entry struct {
	Name  string `json:"name"`
	Kind  Kind   `json:"kind"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix milliseconds
	MIME  string `json:"mime,omitempty"`
}

// ErrInvalidPath is returned for paths that are malformed or try to leave the root.
var ErrInvalidPath = errors.New("invalid path")

// ErrNotDir is returned when a listing is requested for a file.
var ErrNotDir = errors.New("not a folder")

// ErrHidden is returned when a hidden path is requested and hidden files are disabled.
var ErrHidden = errors.New("hidden path")

// Root is the served directory.
type Root struct {
	root       *os.Root
	dir        string
	showHidden bool
}

// Open opens dir as the served root.
func Open(dir string, showHidden bool) (*Root, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &Root{root: r, dir: abs, showHidden: showHidden}, nil
}

// Dir returns the absolute path of the served directory.
func (r *Root) Dir() string { return r.dir }

// Close releases the root.
func (r *Root) Close() error { return r.root.Close() }

// Clean turns a URL-style path ("/a/b", "a/b/", "") into a root-relative path
// ("a/b", "."). It rejects anything that would climb out of the root and, unless
// hidden files are enabled, any segment starting with a dot.
func (r *Root) Clean(p string) (string, error) {
	if strings.ContainsRune(p, 0) || strings.Contains(p, "\\") {
		return "", ErrInvalidPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", ErrInvalidPath
		}
		if !r.showHidden && strings.HasPrefix(seg, ".") && seg != "." {
			return "", ErrHidden
		}
	}
	c := path.Clean("/" + p)
	c = strings.TrimPrefix(c, "/")
	if c == "" {
		c = "."
	}
	return c, nil
}

// List returns the entries of a directory: folders first, then files by name.
func (r *Root) List(rel string) ([]Entry, error) {
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, ErrNotDir
	}

	dirents, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		name := d.Name()
		if r.Hidden(name) {
			continue
		}
		// Stat through the root so symlinks are followed only if they stay inside it.
		info, err := r.root.Stat(path.Join(rel, name))
		if err != nil {
			continue // broken or escaping symlink: skip silently
		}
		e := Entry{
			Name:  name,
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		}
		if info.IsDir() {
			e.Kind = KindDir
			e.Size = 0
		} else if info.Mode().IsRegular() {
			e.MIME = mimeOf(name)
			e.Kind = kindOf(name, e.MIME)
		} else {
			continue // sockets, devices, fifos
		}
		out = append(out, e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Kind == KindDir) != (out[j].Kind == KindDir) {
			return out[i].Kind == KindDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Walk calls fn for every folder and regular file below the folder rel,
// parents before children, in name order. It leaves out what List leaves out
// (hidden names, broken or escaping symlinks, FIFOs and devices), and also
// symlinks to folders, which could loop; symlinks to files inside the root
// are reported with the target's info. A subfolder that can't be read is
// skipped. Walk stops at the first error fn returns and returns it.
func (r *Root) Walk(rel string, fn func(rel string, info fs.FileInfo) error) error {
	if _, err := r.StatDir(rel); err != nil {
		return err
	}
	return r.walk(rel, fn)
}

func (r *Root) walk(dir string, fn func(string, fs.FileInfo) error) error {
	f, err := r.root.Open(dir)
	if err != nil {
		return nil
	}
	dirents, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return nil
	}
	sort.Slice(dirents, func(i, j int) bool { return dirents[i].Name() < dirents[j].Name() })
	for _, d := range dirents {
		if r.Hidden(d.Name()) {
			continue
		}
		p := path.Join(dir, d.Name())
		info, err := r.root.Stat(p)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir() && d.Type()&fs.ModeSymlink == 0:
			if err := fn(p, info); err != nil {
				return err
			}
			if err := r.walk(p, fn); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := fn(p, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// Hidden reports whether a file named name is left out of listings.
func (r *Root) Hidden(name string) bool {
	return !r.showHidden && strings.HasPrefix(name, ".")
}

// StatDir returns the info of a directory; a file reports ErrNotDir.
func (r *Root) StatDir(rel string) (fs.FileInfo, error) {
	st, err := r.root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, ErrNotDir
	}
	return st, nil
}

// StatFile returns the info of a regular file; anything else reports fs.ErrNotExist.
func (r *Root) StatFile(rel string) (fs.FileInfo, error) {
	st, err := r.root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	return st, nil
}

// OpenFile opens a regular file for reading.
func (r *Root) OpenFile(rel string) (*os.File, fs.FileInfo, error) {
	// Check before opening: open(2) on a FIFO blocks until a writer appears,
	// and opening a device can have side effects.
	if _, err := r.StatFile(rel); err != nil {
		return nil, nil, err
	}
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fs.ErrNotExist
	}
	return f, info, nil
}

var textExt = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".log": true, ".csv": true, ".tsv": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".conf": true,
	".xml": true, ".go": true, ".py": true, ".js": true, ".ts": true, ".rs": true, ".c": true,
	".h": true, ".cpp": true, ".java": true, ".sh": true, ".sql": true, ".css": true, ".html": true,
	".jsx": true, ".tsx": true, ".mjs": true, ".rb": true, ".php": true, ".kt": true, ".swift": true,
	".cs": true, ".hpp": true, ".lua": true, ".zig": true, ".scss": true, ".vue": true, ".svelte": true,
	".diff": true, ".patch": true, ".proto": true, ".tf": true, ".nix": true, ".ipynb": true,
}

// Extensions Go's mime table may not know on minimal systems.
var extraMIME = map[string]string{
	".webp": "image/webp", ".avif": "image/avif", ".heic": "image/heic",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".ogg": "audio/ogg", ".wav": "audio/wav", ".flac": "audio/flac",
	".md": "text/markdown; charset=utf-8", ".log": "text/plain; charset=utf-8", ".yaml": "text/yaml; charset=utf-8",
	".yml": "text/yaml; charset=utf-8", ".toml": "text/plain; charset=utf-8",
}

func mimeOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if m, ok := extraMIME[ext]; ok {
		return m
	}
	if m := mime.TypeByExtension(ext); m != "" {
		return m
	}
	if textExt[ext] {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// MIMEOf exposes the MIME detection used for listings.
func MIMEOf(name string) string { return mimeOf(name) }

// KindOf returns the kind a listing reports for a file named name.
func KindOf(name string) Kind { return kindOf(name, mimeOf(name)) }

func kindOf(name, m string) Kind {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case strings.HasPrefix(m, "image/"):
		return KindImage
	case strings.HasPrefix(m, "video/"):
		return KindVideo
	case strings.HasPrefix(m, "audio/"):
		return KindAudio
	case m == "application/pdf":
		return KindPDF
	case strings.HasPrefix(m, "text/") || textExt[ext]:
		return KindText
	}
	return KindOther
}
