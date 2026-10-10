package auth

import (
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two tokens are equal: %q", a)
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		t.Fatalf("token %q is not URL-safe base64: %v", a, err)
	}
	if len(raw) != 16 {
		t.Errorf("token has %d bytes; want 16", len(raw))
	}
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
})

func TestMiddlewareDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	Middleware("", ok).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("got %d %q; want 200 ok", rec.Code, rec.Body)
	}
}

func TestMiddleware(t *testing.T) {
	h := Middleware("secret", ok)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("query exchanged for cookie", func(t *testing.T) {
		rec := serve(httptest.NewRequest("GET", "/a/b?x=1&t=secret", nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("code %d; want 303", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/a/b?x=1" {
			t.Errorf("Location %q; want /a/b?x=1", loc)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d cookies; want 1", len(cookies))
		}
		c := cookies[0]
		if c.Name != CookieName || c.Value != "secret" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
			t.Errorf("cookie %+v", c)
		}
	})

	t.Run("redirect stays on this host", func(t *testing.T) {
		for _, target := range []string{"//evil.com/?t=secret", "///evil.com/x?t=secret"} {
			rec := serve(httptest.NewRequest("GET", "http://kropka"+target, nil))
			if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "//") || !strings.HasPrefix(loc, "/") {
				t.Errorf("%s: Location %q leaves the host", target, loc)
			}
		}
	})

	t.Run("secure cookie over TLS", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?t=secret", nil)
		r.TLS = &tls.ConnectionState{}
		cookies := serve(r).Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure {
			t.Errorf("cookies %+v; want one Secure cookie", cookies)
		}
	})

	t.Run("cookie admits", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: "secret"})
		if rec := serve(r); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("got %d %q; want 200 ok", rec.Code, rec.Body)
		}
	})

	deny := map[string]func() *http.Request{
		"no credentials": func() *http.Request { return httptest.NewRequest("GET", "/", nil) },
		"wrong query":    func() *http.Request { return httptest.NewRequest("GET", "/?t=nope", nil) },
		"prefix query":   func() *http.Request { return httptest.NewRequest("GET", "/?t=secre", nil) },
		"wrong cookie": func() *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{Name: CookieName, Value: "nope"})
			return r
		},
		"wrong query beats good cookie": func() *http.Request {
			r := httptest.NewRequest("GET", "/?t=nope", nil)
			r.AddCookie(&http.Cookie{Name: CookieName, Value: "secret"})
			return r
		},
	}
	for name, req := range deny {
		t.Run(name, func(t *testing.T) {
			rec := serve(req())
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("code %d; want 401", rec.Code)
			}
			if rec.Body.String() == "ok" {
				t.Error("reached the guarded handler")
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control %q; want no-store", cc)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Error("set a cookie on denial")
			}
		})
	}
}
