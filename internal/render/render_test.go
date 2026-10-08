package render

import (
	"strings"
	"testing"
)

// fakeLinker tags targets so tests can see what was resolved, and to what.
type fakeLinker struct{}

func (fakeLinker) Link(target string) string  { return "link:" + target }
func (fakeLinker) Image(target string) string { return "img:" + target }

func renderMD(t *testing.T, rel, src string) string {
	t.Helper()
	out, err := File(rel, []byte(src), fakeLinker{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMarkdownBasics(t *testing.T) {
	out := renderMD(t, "README.md", "# Hello world\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- [x] done\n\n~~gone~~\n")
	for _, want := range []string{
		`<div class="markdown">`,
		`<h1 id="md-hello-world">Hello world</h1>`,
		`<table>`,
		`type="checkbox"`,
		`<del>gone</del>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMarkdownNoScript(t *testing.T) {
	src := "<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n" +
		"[a](javascript:alert(1)) [b](vbscript:x) ![c](javascript:alert(1))\n\n" +
		"text <span onclick=\"x()\">inline</span>\n"
	out := renderMD(t, "x.md", src)
	for _, bad := range []string{"<script", "onerror", "onclick", "javascript:", "vbscript:"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}
}

func TestMarkdownLinks(t *testing.T) {
	src := "[rel](other.md) [up](../top.md) [abs](/notes/a.md) [esc](my%20file.txt) " +
		"[away](../../../etc/passwd) [ext](https://example.com) [mail](mailto:a@b.c) [anchor](#install)\n\n" +
		"![pic](img/p.png) ![remote](https://example.com/p.png)\n"
	out := renderMD(t, "docs/guide/x.md", src)
	for _, want := range []string{
		`href="link:docs/guide/other.md"`,
		`href="link:docs/top.md"`,
		`href="link:notes/a.md"`,
		`href="link:docs/guide/my%20file.txt"`, // goldmark escapes the URL again when writing it
		`href="link:."`,
		`href="https://example.com" target="_blank" rel="noopener"`,
		`href="mailto:a@b.c"`,
		`href="#install"`,
		`src="img:docs/guide/img/p.png"`,
		`src="https://example.com/p.png"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMarkdownCodeBlock(t *testing.T) {
	out := renderMD(t, "x.md", "```go\nfunc main() {}\n```\n")
	if !strings.Contains(out, `class="chroma"`) || !strings.Contains(out, `<span class="kd">func</span>`) {
		t.Errorf("code block not highlighted:\n%s", out)
	}
	if strings.Contains(out, "style=") {
		t.Errorf("inline styles would be blocked by the page's CSP:\n%s", out)
	}
}

func TestCode(t *testing.T) {
	out := renderMD(t, "src/main.go", "package main\n\nfunc main() {}\n")
	if !strings.Contains(out, `<pre class="chroma">`) || !strings.Contains(out, `<span class="kn">package</span>`) {
		t.Errorf("not highlighted:\n%s", out)
	}
	if strings.Contains(out, "style=") {
		t.Errorf("inline styles would be blocked by the page's CSP:\n%s", out)
	}
}

func TestPlainText(t *testing.T) {
	for _, name := range []string{"notes.txt", "build.log", "unknown.zzz"} {
		out := renderMD(t, name, "a < b & <script>\n")
		if out != `<pre class="plain">a &lt; b &amp; &lt;script&gt;`+"\n</pre>" {
			t.Errorf("%s: %s", name, out)
		}
	}
}

func TestCodeEscapes(t *testing.T) {
	out := renderMD(t, "page.html", "<script>alert(1)</script>\n")
	if strings.Contains(out, "<script") {
		t.Errorf("unescaped:\n%s", out)
	}
}
