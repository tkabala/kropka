package fsview

import (
	"errors"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// FuzzClean checks that every path Clean accepts stays inside the root: it is
// "." or a clean relative path with no "..", backslash or NUL, no hidden
// segment unless they are shown, and cleaning it again changes nothing.
func FuzzClean(f *testing.F) {
	for _, s := range []string{
		"", "/", "sub", "/sub/", "sub//x", "./sub/./", "..", "../outside", "sub/../../x",
		"/..", "a\\..\\b", "x\x00y", ".hidden/c.txt", "a/.b/c", "a/./../b", "...", "a/..b",
	} {
		f.Add(s, false)
		f.Add(s, true)
	}
	roots := map[bool]*Root{}
	for _, hidden := range []bool{false, true} {
		r, err := Open(f.TempDir(), hidden)
		if err != nil {
			f.Fatal(err)
		}
		f.Cleanup(func() { _ = r.Close() })
		roots[hidden] = r
	}
	f.Fuzz(func(t *testing.T, p string, hidden bool) {
		r := roots[hidden]
		c, err := r.Clean(p)
		if err != nil {
			if !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrHidden) {
				t.Fatalf("Clean(%q): unexpected error %v", p, err)
			}
			return
		}
		if c == "" || strings.HasPrefix(c, "/") || path.Clean(c) != c ||
			c == ".." || strings.HasPrefix(c, "../") || strings.ContainsAny(c, "\\\x00") {
			t.Fatalf("Clean(%q) = %q: not a clean path inside the root", p, c)
		}
		// On Windows IsLocal also rejects reserved names like NUL; os.Root
		// refuses those itself.
		if runtime.GOOS != "windows" && !filepath.IsLocal(c) {
			t.Fatalf("Clean(%q) = %q: not local", p, c)
		}
		if !hidden && c != "." {
			for _, seg := range strings.Split(c, "/") {
				if strings.HasPrefix(seg, ".") {
					t.Fatalf("Clean(%q) = %q: hidden segment %q", p, c, seg)
				}
			}
		}
		if again, err := r.Clean(c); err != nil || again != c {
			t.Fatalf("Clean(%q) = %q, but Clean(%q) = %q, %v", p, c, c, again, err)
		}
	})
}
