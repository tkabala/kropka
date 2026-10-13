package render

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

// Text and attribute values in the output are escaped, so every "<" followed
// by a letter starts a real tag, and no attribute value contains '"' or '>'.
var (
	tagRe  = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)([^>]*)>`)
	attrRe = regexp.MustCompile(`\s*([^\s="]+)(?:="([^"]*)")?`)
)

// What Markdown and highlighted code may produce. Anything else, raw HTML
// that got through for one, fails the fuzz test.
var (
	allowedTags = set("div p h1 h2 h3 h4 h5 h6 a img em strong del code pre span " +
		"ul ol li blockquote hr br table thead tbody tr th td input")
	allowedAttrs = set("id class href src alt title target rel align type checked disabled start")
)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// FuzzRender checks the promise in the package doc on arbitrary input: the
// HTML lands in kropka's own page, so it may only use known tags and
// attributes, which rules out script, event handlers and inline styles; links
// may not use a script scheme; and ids must carry IDPrefix so they cannot
// clobber the page's own.
func FuzzRender(f *testing.F) {
	names := []string{"x.md", "x.go", "x.html", "x.js", "x.txt"}
	for _, s := range []string{
		"# Hello world\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n\n- [x] done\n\n~~gone~~\n",
		"<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[a](javascript:alert(1)) [b](vbscript:x)\n",
		"[rel](other.md) [up](../../x) ![p](img/p.png) <https://a.b> x@y.z www.example.com [d](data:text/html,x)\n",
		"[t](\tjava\nscript:alert(1)) [e](JaVaScRiPt:x) [h](&#106;avascript:x)\n",
		"```html\n<style>x{}</style><script>y()</script>\n```\n\n    indented\n\n1. one\n2. two\n\n> quote\n\n---\n",
		"package main\n\nfunc main() { println(\"<b>\") }\n",
	} {
		for i := range names {
			f.Add(uint8(i), s)
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, src string) {
		name := names[int(kind)%len(names)]
		out, err := File(name, []byte(src), fakeLinker{})
		if err != nil {
			return // a lexer refusing the input is fine; it renders nothing
		}
		for _, tag := range tagRe.FindAllStringSubmatch(out, -1) {
			if !allowedTags[strings.ToLower(tag[1])] {
				t.Fatalf("%s: tag %q in:\n%s", name, tag[0], out)
			}
			rest := tag[2]
			for _, a := range attrRe.FindAllStringSubmatch(rest, -1) {
				attr, val := strings.ToLower(a[1]), html.UnescapeString(a[2])
				switch {
				case attr == "/" && strings.TrimSpace(rest) == "/": // self-closing
				case !allowedAttrs[attr]:
					t.Fatalf("%s: attribute %q in %q", name, attr, tag[0])
				case (attr == "href" || attr == "src") && dangerous(val):
					t.Fatalf("%s: %s=%q in %q", name, attr, val, tag[0])
				case attr == "id" && !strings.HasPrefix(val, IDPrefix):
					t.Fatalf("%s: id %q without %q", name, val, IDPrefix)
				}
			}
		}
	})
}

// dangerous reports whether a browser would treat u as a script URL. Like a
// browser, it ignores leading spaces and control characters and tabs and
// newlines anywhere. Of data: URLs, only the image types goldmark lets
// through are fine.
func dangerous(u string) bool {
	u = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, u)
	u = strings.ToLower(strings.TrimLeft(u, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f"+
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f "))
	if strings.HasPrefix(u, "data:") {
		for _, ok := range []string{"png", "gif", "jpeg", "webp"} {
			if strings.HasPrefix(u, "data:image/"+ok) {
				return false
			}
		}
		return true
	}
	return strings.HasPrefix(u, "javascript:") || strings.HasPrefix(u, "vbscript:")
}
