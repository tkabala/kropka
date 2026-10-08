// Package render turns text files into HTML for the viewer: Markdown is
// rendered, source code is syntax highlighted, anything else is escaped.
//
// The output is inserted into kropka's own page, so it must never carry
// script: raw HTML in Markdown is dropped and dangerous link schemes
// (javascript:, vbscript:, ...) are removed by goldmark. Highlighting uses CSS
// classes, never inline styles, so the page's CSP can stay strict.
package render

import (
	"bytes"
	"html"
	"net/url"
	"path"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// IDPrefix is put in front of heading ids so they cannot clash with the ids of
// kropka's own page. The UI adds it back when following a "#heading" link.
const IDPrefix = "md-"

// Style is the chroma style the viewer's CSS was generated from (see CSS).
const Style = "github-dark"

// Linker maps a link or image target in a Markdown file to a URL. target is a
// path relative to the served root, already cleaned and unescaped.
type Linker interface {
	Link(target string) string
	Image(target string) string
}

// IsMarkdown reports whether name is rendered as Markdown.
func IsMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// File renders the text of the file at rel (a path relative to the served
// root). Relative links and images in Markdown are resolved against rel's
// folder and passed through l.
func File(rel string, src []byte, l Linker) (string, error) {
	if IsMarkdown(rel) {
		return markdown(rel, src, l)
	}
	return code(path.Base(rel), src)
}

func code(name string, src []byte) (string, error) {
	lexer := lexers.Match(name)
	if lexer == nil || lexer.Config().Name == "plaintext" {
		return `<pre class="plain">` + html.EscapeString(string(src)) + "</pre>", nil
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, string(src))
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := formatter.Format(&b, styles.Get(Style), it); err != nil {
		return "", err
	}
	return b.String(), nil
}

var formatter = chromahtml.New(chromahtml.WithClasses(true))

func markdown(rel string, src []byte, l Linker) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM, // tables, task lists, strikethrough, autolinks
			highlighting.NewHighlighting(
				highlighting.WithStyle(Style),
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(&links{dir: path.Dir(rel), l: l}, 999)),
		),
		// No html.WithUnsafe: raw HTML is replaced by a comment and dangerous
		// URLs are dropped.
	)
	var b bytes.Buffer
	b.WriteString(`<div class="markdown">`)
	if err := md.Convert(src, &b); err != nil {
		return "", err
	}
	b.WriteString("</div>")
	return b.String(), nil
}

// links rewrites relative targets to kropka URLs, opens external links in a
// new tab and prefixes heading ids.
type links struct {
	dir string
	l   Linker
}

func (t *links) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			if id, ok := n.AttributeString("id"); ok {
				if b, ok := id.([]byte); ok {
					n.SetAttributeString("id", []byte(IDPrefix+string(b)))
				}
			}
		case *ast.Link:
			if dest, ok := t.resolve(n.Destination); ok {
				n.Destination = []byte(t.l.Link(dest))
			} else if external(n.Destination) {
				n.SetAttributeString("target", []byte("_blank"))
				n.SetAttributeString("rel", []byte("noopener"))
			}
		case *ast.AutoLink:
			// Always absolute (a URL or an email); nothing to resolve.
		case *ast.Image:
			if dest, ok := t.resolve(n.Destination); ok {
				n.Destination = []byte(t.l.Image(dest))
			}
		}
		return ast.WalkContinue, nil
	})
}

// resolve turns a relative (or root-relative) link target into a path from
// the served root. It reports false for URLs with a scheme or host and for
// in-page anchors. A target above the root resolves to the root itself, which
// is as far as kropka lets anyone go.
func (t *links) resolve(dest []byte) (string, bool) {
	u, err := url.Parse(string(dest))
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return "", false
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		p = path.Join(t.dir, p)
	}
	p = path.Clean(strings.TrimPrefix(p, "/"))
	if p == ".." || strings.HasPrefix(p, "../") {
		p = "."
	}
	return p, true
}

func external(dest []byte) bool {
	u, err := url.Parse(string(dest))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
