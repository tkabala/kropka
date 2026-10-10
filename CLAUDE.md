# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

kropka is a single Go binary that serves a directory as a mobile-first gallery. The
frontend is vanilla HTML/CSS/JS embedded with `go:embed` — there is no frontend build step.

**Read [ARCHITECTURE.md](ARCHITECTURE.md) before non-trivial changes.** It has the request
pipeline, the package table and the reasoning behind each design decision (thumbnails, live
reload, zip, video, Markdown rendering). Keep it up to date when you change one of those.

## Commands

Tool versions are pinned in `mise.toml` (Go, goreleaser, golangci-lint).

```sh
make build      # CGO_ENABLED=0 build into bin/kropka, version from git describe
make run        # build and serve the current directory
make test       # go test -race ./...
make vet        # go vet ./...
make lint       # golangci-lint run (v2, with revive enabled)
make snapshot   # all release artifacts via GoReleaser, no publishing
make demo       # record the README demo (docs/demo/README.md)
```

Single test: `go test -race -run TestName ./internal/server/`.

CI (`.github/workflows/ci.yml`) runs vet and race tests on Linux, macOS and Windows, plus
golangci-lint and govulncheck. Video thumbnail tests skip when `ffmpeg` isn't on `PATH`.
Code must stay portable: there are `_unix`/`_windows` and `_other` file splits, and several
fixes have been Windows-specific.

## Invariants to preserve

- **All file access goes through `fsview.Root` (`os.Root`).** Never open served files by
  path with `os.Open` etc.; ffmpeg gets the already-opened file on stdin, not a path.
- **Nothing under `/raw/` may run in kropka's origin.** It is served with a sandbox CSP.
  HTML from `/api/render` lands in the page's own origin, so it must carry no script and no
  inline styles (the page CSP forbids inline script and style; goldmark drops raw HTML).
- **Read-only.** The only thing kropka writes is the thumbnail cache, outside the served dir.
- **The token never gets logged** — the request log records the path, not the query.
- Hidden files are excluded consistently (listings, watch events, zip, Markdown links)
  unless `--hidden`.
- Highlighting colours in `internal/ui/static/style.css` are chroma's `github-dark` pasted
  as CSS; changing `render.Style` means regenerating them.

## Conventions

- Conventional Commits with optional scope: `feat:`, `fix(thumb):`, `test(watch):`,
  `docs:`, `ci:`, `chore:`, `style:`.
- Every error is checked or explicitly discarded (`_ =`); the linters enforce this.
- Releases: push a `v*` tag; GoReleaser publishes binaries and a multi-arch GHCR image.
  `main.version` is set via `-ldflags`.
- `docs/demo/` is the scripted README demo recording (Playwright + VHS); it is separate
  from the Go module's runtime code.
