# kropka

**Serve `.` to your phone in one command.**

*kropka* is Polish for "dot", and that is what it serves: the current directory, as a
fast, mobile-first gallery. Generate images, videos or reports on a server, run
`kropka`, and browse them from your laptop or phone without copying anything.

```console
$ kropka

  ● kropka v0.1.0
  serving /home/me/renders

  → http://localhost:8080/?t=Xk3…

  Remote server? Forward the port from your machine:
    ssh -L 8080:localhost:8080 <server>
```

## Features

- **One binary, zero config.** The UI is embedded; no runtime, no config files.
- **Mobile first.** Photo grid, full-screen viewer, swipe between items, swipe down to close.
- **Fast grids.** Large photos get small thumbnails, generated on first view and cached on
  disk, so a folder of 20 MB renders loads like a folder of 40 KB ones.
- **Live.** New and changed files show up on their own while you watch, so you can leave
  the page open while a job writes its output.
- **Video that seeks.** Files are streamed with HTTP Range support.
- **Safe by default.** Localhost only, random access token, read only, no path traversal
  (enforced by Go's `os.Root`), untrusted files served in a CSP sandbox.
- **Text and logs** open inline; PDFs open in the browser's viewer; everything else downloads.

## Install

**Binary** — grab one for Linux, macOS or Windows from
[Releases](../../releases), unpack, put `kropka` on your `PATH`.

**Go**

```sh
go install github.com/tkabala/kropka/cmd/kropka@latest
```

**Docker** (for a permanent setup)

```sh
docker run -d --name kropka -p 8080:8080 \
  -v /path/to/folder:/data:ro \
  -e KROPKA_TOKEN=choose-a-long-secret \
  ghcr.io/tkabala/kropka
```

Without `KROPKA_TOKEN`, a random token is generated on each start; find the link with
`docker logs kropka`.

## Usage

```
kropka [flags] [dir]

  -p, --port   port to listen on, next free one if busy   (8080, env KROPKA_PORT)
  --lan        listen on all interfaces and print a QR code for your phone
  --bind       address to bind                             (env KROPKA_BIND)
  --token      fixed access token instead of a random one  (env KROPKA_TOKEN)
  --no-auth    disable the token (not recommended with --lan)
  --hidden     show dotfiles
  --qr         print a QR code even without --lan
  --quiet      do not log requests
  --cache-dir  where to keep thumbnails                     (user cache dir, env KROPKA_CACHE_DIR)
  --no-thumbs  show original images in the grid
  --version    print version
```

### Reaching it from your phone

| Where the files are | What to run |
|---|---|
| Your laptop, phone on the same Wi‑Fi | `kropka --lan`, scan the QR code |
| A remote server, viewing on your laptop | `kropka` on the server, `ssh -L 8080:localhost:8080 server` locally |
| A remote server, viewing on your phone | `kropka --lan` on a server you can reach (VPN, [Tailscale](https://tailscale.com)), scan the QR code |

## Security model

kropka is meant for short-lived, personal sharing.

- It binds to `127.0.0.1` unless you pass `--lan` or `--bind`.
- Every request needs the token. It is exchanged for an `HttpOnly`, `SameSite=Lax`
  cookie on first visit and removed from the URL; `Referrer-Policy: no-referrer` keeps it
  from leaking to other sites. Lax rather than Strict, so links opened from a QR scanner
  or another app work; kropka has no state-changing requests for Strict to protect.
- All file access goes through [`os.Root`](https://pkg.go.dev/os#Root): `..` and symlinks
  pointing outside the served directory cannot be followed.
- Files are served with `Content-Security-Policy: sandbox`, so an HTML or SVG file in the
  folder cannot run scripts as kropka.
- There is no upload, rename or delete. The only thing kropka writes is its thumbnail cache,
  outside the served folder.
- Traffic is plain HTTP. Over untrusted networks, use an SSH tunnel or a VPN.

## Development

```sh
make run        # build and serve the current directory
make test       # go test -race ./...
make docker     # local image
make snapshot   # all release artifacts via GoReleaser, without publishing
```

Releases are cut by pushing a `v*` tag; GitHub Actions runs GoReleaser, which publishes
binaries and a multi-arch image to GHCR. See [ARCHITECTURE.md](ARCHITECTURE.md) for how the
pieces fit together.

## Roadmap

- [x] Thumbnails with an on-disk cache (fast grids for large photos)
- [x] Live reload: new files appear without refreshing (fsnotify + SSE)
- [ ] Markdown rendering and syntax highlighting
- [ ] Pinch-zoom and pan in the viewer
- [ ] Download a folder as `.zip`
- [ ] Video thumbnails when `ffmpeg` is available

## License

MIT
