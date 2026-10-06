// Package auth implements kropka's single-token access control.
//
// The token is printed in the startup URL (and QR code). On first visit it is
// exchanged for an HttpOnly cookie and stripped from the address bar, so it
// does not linger in browser history or leak through screenshots.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const (
	// CookieName is the session cookie holding the token.
	CookieName = "kropka_token"
	// QueryParam is the URL parameter carrying the token on first visit.
	QueryParam = "t"
)

// NewToken returns a random 128-bit URL-safe token.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Middleware guards next with token. An empty token disables auth.
func Middleware(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	match := func(s string) bool {
		return subtle.ConstantTimeCompare([]byte(s), want) == 1
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get(QueryParam); q != "" {
			if !match(q) {
				deny(w)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     CookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				// Lax, not Strict: the first visit usually comes from another
				// app (a QR scanner, a chat link), and browsers withhold Strict
				// cookies on redirects of a cross-site navigation, so the
				// redirect below would land on "Access denied".
				SameSite: http.SameSiteLaxMode,
				Secure:   r.TLS != nil,
			})
			u := *r.URL
			qs := u.Query()
			qs.Del(QueryParam)
			u.RawQuery = qs.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(CookieName); err == nil && match(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		deny(w)
	})
}

func deny(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1">
<title>kropka</title><body style="font-family:system-ui;background:#111;color:#ddd;display:grid;place-items:center;min-height:90vh;margin:0;text-align:center;padding:1rem">
<div><div style="font-size:4rem;line-height:1">.</div><p>Access denied.</p><p style="color:#888">Open the link (or scan the QR code) printed by <code>kropka</code> in the terminal.</p></div>`))
}
