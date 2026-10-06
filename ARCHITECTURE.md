# Architecture

kropka is a single Go binary. The browser UI is plain HTML, CSS and JavaScript embedded
with `go:embed`, so there is no build step for the frontend and nothing to install.

```
browser (SPA, hash routing)
   │
   ▼
logRequests → securityHeaders → auth.Middleware → ServeMux
                                                    ├── GET /api/info        folder name, version
                                                    ├── GET /api/ls?path=    directory listing (JSON)
                                                    ├── GET /api/events?path= change notifications (SSE)
                                                    ├── GET /raw/{path...}   file bytes (Range, ETag, sandbox CSP)
                                                    ├── GET /thumb/{path...} grid thumbnail, or 307 to /raw/
                                                    └── GET /                embedded UI
                                                    │
                                                    ▼
                                             fsview.Root (os.Root)
                                                    │
                                                    ▼
                                             served directory
```

## Packages

| Package | Responsibility |
|---|---|
| `cmd/kropka` | Flags and env vars, picking a free port, startup banner and QR code, graceful shutdown |
| `internal/fsview` | Read-only, traversal-safe access to the directory; listing; MIME and kind detection |
| `internal/auth` | Random token, token-to-cookie exchange, request guard |
| `internal/thumb` | Thumbnail generation, on-disk cache, EXIF orientation |
| `internal/watch` | Watching viewed folders (`fsnotify`), reference counting, debouncing |
| `internal/server` | HTTP routes, security headers, request log |
| `internal/ui` | Embedded frontend (`static/`) |

## Key decisions

**`os.Root` for all file access.** Path cleaning in `fsview.Clean` rejects `..` early, but
the real guarantee comes from `os.Root` (Go 1.24+): the kernel-level lookups it performs
cannot leave the root, even through symlinks. A symlink that points inside the root works;
one that points outside is skipped in listings and fails on open.

**Token in the URL, then a cookie.** The startup link carries `?t=<token>` so that one
click or one QR scan is enough. The server swaps it for an `HttpOnly` cookie and redirects
to a clean URL, which keeps the token out of history and out of `Referer` headers. The
request log records only the path, never the query.

**Untrusted files are sandboxed.** Anything under `/raw/` is sent with
`Content-Security-Policy: sandbox`. Without it, an `.html` or `.svg` in the served folder
would run in kropka's origin. PDFs are the exception because browsers refuse to render them
in a sandbox.

**Localhost by default.** The common case is a remote server reached over SSH, where
`127.0.0.1` plus `ssh -L` is both the simplest and the safest option. `--lan` is an explicit
opt-in and prints a QR code.

**Hash routing.** `#/folder?view=file` keeps the server trivial (no SPA fallback) and makes
the phone's back button close the viewer and walk up folders naturally.

**No frontend toolchain (for now).** The UI is small enough that vanilla JS keeps the repo
approachable and the binary small. If it grows (Markdown, code highlighting, PhotoSwipe), the
plan is Vite building into `internal/ui/static`, still embedded.

**Thumbnails: one size, cached forever per file version.** Grid tiles are square and
cropped, so a thumbnail only needs its short side sharp: 480 px covers a phone at 3x and a
desktop at 2x, and one size keeps the cache small. Thumbnails are JPEG, or PNG when the image
has transparency, stored under the user cache dir by hash(root, path, size, mtime). The UI
adds `?v=<mtime>` to the URL, which lets the browser cache them as immutable. Decoding is
bounded by a worker pool and a budget of decoded bytes (estimated from the colour model, so
a 16-bit PNG counts eight times a grayscale JPEG), and large decodes are handed back to the
OS at once; `singleflight` makes a burst of requests for one image do the work once. Files
under 64 KB, images already smaller than a thumbnail, images over 50 MP, formats Go cannot
decode (SVG, AVIF, HEIC) and thumbnails that would be no smaller than the original are
redirected to `/raw/`; the UI loads files under 64 KB and SVGs from `/raw/` directly. Any
other failure (a full disk, an unwritable cache) is logged and redirected too, so the grid
never needs a fallback of its own. Failures are remembered per file version, so a corrupt
image is not decoded on every request. Thumbnails unused for 30 days are pruned at startup.

**Live reload: the server says "changed", the client re-lists.** The page opens an
`EventSource` on `/api/events?path=` for the folder on screen. The server watches only folders
someone is viewing (reference counted, so ten tabs on one folder cost one OS watch) and sends a
bare `change` event; the client then fetches `/api/ls` as usual, so there is one code path for
listings and nothing to keep in sync. Events are coalesced: a notification goes out 300 ms
after the folder goes quiet, or 2 s after the first event if it never does. Attribute-only
changes and hidden files are ignored. The stream is closed while the tab is hidden, because
browsers allow only six HTTP/1.1 connections per server and a phone shouldn't hold one in the
background; on return the page reconnects and refreshes once. If watching is unavailable
(no inotify watches left, an unsupported platform), the server answers `204`, which makes
`EventSource` stop for good, and the page works as before. On shutdown the watcher is closed
first, which ends every stream, so `Shutdown` doesn't wait for connections that never go idle.
Network filesystems (NFS, SMB, FUSE mounts) often don't deliver change events; there the page
still refreshes when it regains focus.

## Planned components

- **Video thumbnails** — a frame via `ffmpeg` when present, through the same cache.
