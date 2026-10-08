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
                                                    ├── GET /api/render?path= text file as HTML (Markdown, code)
                                                    ├── GET /raw/{path...}   file bytes (Range, ETag, sandbox CSP)
                                                    ├── GET /thumb/{path...} grid thumbnail, or 307 to /raw/
                                                    ├── GET /zip/{path...}   folder as a .zip, streamed
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
| `internal/render` | Markdown to HTML (`goldmark`), syntax highlighting (`chroma`) |
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
approachable. Markdown and code highlighting are done on the server (below) partly to keep it
that way. If the UI grows (PhotoSwipe), the plan is Vite building into `internal/ui/static`,
still embedded.

**Markdown and code are rendered on the server.** The viewer asks `/api/render` for a text
file and puts the HTML it gets into the page. `.md` files go through goldmark with the GFM
extensions; other files are highlighted by chroma when it knows the language from the file
name, and escaped as plain text otherwise. Since this HTML lands in kropka's own origin rather
than the `/raw/` sandbox, it must never carry script: goldmark drops raw HTML and dangerous
link schemes, highlighting uses CSS classes rather than inline styles, and the page's CSP
(no inline script or style) would block either anyway. The response is JSON so that the
endpoint can't be opened as a page. Heading ids get an `md-` prefix so they can't collide with
the page's own ids; `#heading` links are scrolled to by the UI, since the hash is the route.
Relative links are resolved on the server: a folder becomes its listing, a file the viewer can
show opens in the viewer, anything else links to `/raw/`. A link to a hidden file becomes a link
to the root, which reveals nothing about it. Images go to `/raw/`; remote images are blocked by
the page's CSP, so a Markdown file can't call home. Like the old inline text view, only the
first 1 MB is rendered. The highlighting colours in `style.css` are chroma's `github-dark`,
pasted as CSS, so changing `render.Style` means regenerating them. Chroma's lexers are most
of the binary's growth (7 MB to 12 MB).

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
still refreshes when it regains focus. Windows doesn't report a watched folder itself being
renamed or moved away, so a page showing it isn't told until it refreshes.

**Zip downloads: walked first, then streamed.** `/zip/{path}` walks the folder with the same
rules as a listing (no hidden files, no symlinks leaving the root, no FIFOs or devices) and also
skips symlinks to folders, which could loop. The walk is what enforces the limits (4 GB, 50,000
files): a folder over them gets a `413` before a single byte of zip, and `?check=1` stops after
the walk, which the UI asks first so the error lands in a toast rather than a blank page. The zip
is then written straight to the response, with no temporary file and no `Content-Length`.
Media and archives are stored, everything else deflated. If a read fails halfway, the handler
aborts the connection, so the browser reports a failed download rather than saving a truncated
file.

**Video thumbnails: ffmpeg when there is one, the browser when not.** When `ffmpeg` is on the
`PATH` (or given with `--ffmpeg`), a video's thumbnail is a frame from one second in (the first
is often black), or the first frame of a shorter clip, scaled by ffmpeg and stored in the same
cache as image thumbnails. ffmpeg never gets a path: it reads the file kropka opened through
`os.Root` as its stdin, named as a seekable file (`file:/dev/stdin`; `fd:0` on Windows, which
needs ffmpeg 6.0+), so it can't be pointed outside the served folder and still seeks, as MP4s
without "faststart" require. Each grab runs in the thumbnail worker pool with a 30 s timeout and
a cap on its output. `/api/ls` reports `videoThumbs`, and only then does the grid ask `/thumb/`
for videos; without ffmpeg, or for a file ffmpeg can't read (a `404`, since an `<img>` can't
show a video the way it can an original image), a tile falls back to a muted `<video>` that
loads only its metadata and first frame. The Docker image has no ffmpeg, so there videos keep
those posters.
