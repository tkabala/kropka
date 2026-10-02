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
                                                    ├── GET /raw/{path...}   file bytes (Range, ETag, sandbox CSP)
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

## Planned components

- **Thumbnails** — `GET /thumb/{path}?w=320`. Decode with `image/*` and
  `golang.org/x/image`, resize, cache as JPEG under the user cache dir keyed by
  hash(path, mtime, size, width). Bounded worker pool plus `singleflight` so a burst of
  requests for the same image does the work once. Video frames via `ffmpeg` when present.
- **Live reload** — `GET /api/events?path=` as Server-Sent Events. A watcher (`fsnotify`)
  subscribes only to folders someone is viewing (reference counted), debounces bursts, and
  tells clients to reload the listing.
