// Package server wires kropka's HTTP handlers.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/tkabala/kropka/internal/auth"
	"github.com/tkabala/kropka/internal/fsview"
	"github.com/tkabala/kropka/internal/thumb"
)

// Config configures the HTTP handler.
type Config struct {
	Root    *fsview.Root
	Thumbs  *thumb.Service // nil serves originals in place of thumbnails
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
		"name":    path.Base(s.cfg.Root.Dir()),
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
	case err != nil:
		code := statusFor(err)
		http.Error(w, http.StatusText(code), code)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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

func redirectRaw(w http.ResponseWriter, r *http.Request, rel string) {
	u := url.URL{Path: "/raw/" + rel}
	w.Header().Set("Cache-Control", "private, no-cache")
	http.Redirect(w, r, u.EscapedPath(), http.StatusTemporaryRedirect)
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
