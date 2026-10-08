// Package server wires kropka's HTTP handlers.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tkabala/kropka/internal/auth"
	"github.com/tkabala/kropka/internal/fsview"
	"github.com/tkabala/kropka/internal/render"
	"github.com/tkabala/kropka/internal/thumb"
	"github.com/tkabala/kropka/internal/watch"
)

// ssePing keeps idle event streams alive through proxies and SSH tunnels, and
// notices clients that went away without closing the connection.
var ssePing = 30 * time.Second

// Config configures the HTTP handler.
type Config struct {
	Root    *fsview.Root
	Thumbs  *thumb.Service // nil serves originals in place of thumbnails
	Watch   *watch.Watcher // nil disables live reload
	Token   string         // empty disables auth
	UI      fs.FS          // static frontend
	Version string
	Logger  *log.Logger
}

// New builds the root handler.
func New(cfg Config) http.Handler {
	s := &srv{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ls", s.handleList)
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/render", s.handleRender)
	mux.HandleFunc("GET /raw/{path...}", s.handleRaw)
	mux.HandleFunc("GET /thumb/{path...}", s.handleThumb)
	mux.Handle("GET /", uiHandler(cfg.UI))

	var h http.Handler = mux
	h = auth.Middleware(cfg.Token, h)
	h = securityHeaders(h)
	if cfg.Logger != nil {
		h = logRequests(cfg.Logger, h)
	}
	return h
}

type srv struct{ cfg Config }

func (s *srv) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    filepath.Base(s.cfg.Root.Dir()),
		"version": s.cfg.Version,
	})
}

func (s *srv) handleList(w http.ResponseWriter, r *http.Request) {
	rel, err := s.cfg.Root.Clean(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	entries, err := s.cfg.Root.List(rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    rel,
		"entries": entries,
	})
}

// handleEvents is a Server-Sent Events stream that sends "change" whenever the
// listing of the folder may have changed. The client then reloads it.
func (s *srv) handleEvents(w http.ResponseWriter, r *http.Request) {
	rel, err := s.cfg.Root.Clean(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	// 204 tells EventSource to stop for good rather than reconnect: the page
	// works as before, just without live updates.
	if s.cfg.Watch == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	changes, stop, err := s.cfg.Watch.Subscribe(rel)
	if err != nil {
		if code := statusFor(err); code != http.StatusInternalServerError {
			writeErr(w, code, err)
			return
		}
		// Out of inotify watches, the watcher closing for shutdown, ...
		if s.cfg.Logger != nil {
			s.cfg.Logger.Printf("live reload for %s: %v", rel, err)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer stop()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx would hold events back otherwise
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}
	ping := time.NewTicker(ssePing)
	defer ping.Stop()
	for {
		var msg string
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-changes:
			if !ok {
				return // folder removed, or shutting down
			}
			msg = "event: change\ndata:\n\n"
		case <-ping.C:
			msg = ": ping\n\n"
		}
		if _, err := io.WriteString(w, msg); err != nil || rc.Flush() != nil {
			return
		}
	}
}

// renderLimit caps how much of a file the viewer renders; the rest is left to
// a download.
var renderLimit int64 = 1 << 20

// handleRender returns a text file as HTML for the viewer: Markdown rendered,
// code highlighted. The JSON wrapper keeps the HTML from ever being opened as
// a page in kropka's origin.
func (s *srv) handleRender(w http.ResponseWriter, r *http.Request) {
	rel, err := s.cfg.Root.Clean(r.URL.Query().Get("path"))
	if err == nil && rel == "." {
		err = fsview.ErrInvalidPath
	}
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	f, info, err := s.cfg.Root.OpenFile(rel)
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	defer f.Close()
	src, err := io.ReadAll(io.LimitReader(f, renderLimit))
	if err != nil {
		writeErr(w, statusFor(err), err)
		return
	}
	truncated := info.Size() > int64(len(src))
	if truncated {
		src = trimPartialRune(src)
	}
	out, err := render.File(rel, src, linker{s.cfg.Root})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"html":      out,
		"truncated": truncated,
	})
}

// trimPartialRune drops a UTF-8 sequence cut in half by the size limit.
func trimPartialRune(b []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// linker points links in Markdown files at the UI: folders open as a listing,
// files the viewer can show open in the viewer, anything else as the raw file.
type linker struct{ root *fsview.Root }

func (l linker) Link(target string) string {
	rel, err := l.root.Clean(target)
	if err != nil {
		return "#/" // hidden or invalid: don't reveal anything about it
	}
	if _, err := l.root.StatDir(rel); err == nil {
		return hashFor(rel, "")
	}
	switch fsview.KindOf(rel) {
	case fsview.KindImage, fsview.KindVideo, fsview.KindAudio, fsview.KindText:
		return hashFor(path.Dir(rel), path.Base(rel))
	}
	return rawPath(rel)
}

func (l linker) Image(target string) string { return rawPath(target) }

// hashFor mirrors hashFor in app.js: every segment escaped, so "?view=" can
// only be the separator.
func hashFor(dir, view string) string {
	h := "#/"
	if dir != "." {
		segs := strings.Split(dir, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		h += strings.Join(segs, "/")
	}
	if view != "" {
		h += "?view=" + url.PathEscape(view) // not QueryEscape: JS doesn't decode "+"
	}
	return h
}

func rawPath(rel string) string {
	return (&url.URL{Path: "/raw/" + rel}).EscapedPath()
}

// handleRaw streams a file. http.ServeContent handles Range (video seeking),
// If-Modified-Since and HEAD.
func (s *srv) handleRaw(w http.ResponseWriter, r *http.Request) {
	rel, err := s.cfg.Root.Clean(r.PathValue("path"))
	if err == nil && rel == "." {
		err = fsview.ErrInvalidPath
	}
	if err != nil {
		code := statusFor(err)
		http.Error(w, http.StatusText(code), code)
		return
	}
	f, info, err := s.cfg.Root.OpenFile(rel)
	if err != nil {
		http.Error(w, http.StatusText(statusFor(err)), statusFor(err))
		return
	}
	defer f.Close()

	h := w.Header()
	ctype := fsview.MIMEOf(info.Name())
	h.Set("Content-Type", ctype)
	// Served files are untrusted: an .html or .svg must not run scripts in
	// kropka's origin (it could read the session and act as the user).
	// PDFs are exempt because browsers refuse to render them in a sandbox;
	// their viewers run outside the page's origin anyway.
	if ctype != "application/pdf" {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	}
	h.Set("Cache-Control", "private, no-cache")
	if r.URL.Query().Get("dl") == "1" {
		h.Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(info.Name()))
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// handleThumb serves a grid-sized thumbnail, or redirects to the original when
// there is none (small files, SVG, formats Go cannot decode).
func (s *srv) handleThumb(w http.ResponseWriter, r *http.Request) {
	rel, err := s.cfg.Root.Clean(r.PathValue("path"))
	if err == nil && rel == "." {
		err = fsview.ErrInvalidPath
	}
	if err != nil {
		code := statusFor(err)
		http.Error(w, http.StatusText(code), code)
		return
	}
	if s.cfg.Thumbs == nil {
		redirectRaw(w, r, rel)
		return
	}
	p, err := s.cfg.Thumbs.Get(r.Context(), rel)
	switch {
	case errors.Is(err, thumb.ErrPassthrough):
		redirectRaw(w, r, rel)
		return
	case r.Context().Err() != nil:
		return // client went away
	case err != nil && statusFor(err) != http.StatusInternalServerError:
		code := statusFor(err) // missing, hidden or unreadable file
		http.Error(w, http.StatusText(code), code)
		return
	case err != nil:
		// The original may still be fine (a full disk, an unwritable cache):
		// show it rather than a broken tile.
		s.thumbFailed(w, r, rel, err)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.thumbFailed(w, r, rel, err) // pruned or removed since Get
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.thumbFailed(w, r, rel, err)
		return
	}
	// The UI adds ?v=<mtime>, so each file version has its own URL and can be
	// cached for good; re-rendering the grid then costs no requests at all.
	if r.URL.Query().Has("v") {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	// No name: ServeContent sniffs JPEG or PNG from the bytes.
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *srv) thumbFailed(w http.ResponseWriter, r *http.Request, rel string, err error) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf("thumbnail for %s: %v", rel, err)
	}
	redirectRaw(w, r, rel)
}

func redirectRaw(w http.ResponseWriter, r *http.Request, rel string) {
	w.Header().Set("Cache-Control", "private, no-cache")
	http.Redirect(w, r, rawPath(rel), http.StatusTemporaryRedirect)
}

func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; media-src 'self'; style-src 'self'; script-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer") // the token must never leak via Referer
		h.Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func logRequests(l *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Log the path only: the query may carry the token.
		l.Printf("%d %s %s %s", rec.status, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fsview.ErrHidden):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden
	case errors.Is(err, fsview.ErrInvalidPath), errors.Is(err, fsview.ErrNotDir), errors.Is(err, fs.ErrInvalid):
		return http.StatusBadRequest
	}
	// os.Root reports escape attempts with its own error; treat as bad request.
	if err != nil && isEscape(err) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func isEscape(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) && pe.Err != nil && pe.Err.Error() == "path escapes from parent"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	msg := http.StatusText(code)
	if code == http.StatusBadRequest {
		msg = err.Error()
	}
	writeJSON(w, code, map[string]string{"error": msg})
}
