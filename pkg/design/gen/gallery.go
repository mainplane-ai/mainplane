package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// imports is the components in the order mainplane.css imports them.
var imports = regexp.MustCompile(`@import "\.\./components/([\w-]+)/`)

// gallery writes the developer page of every component, index.html, and
// each component's snippet as components/<name>.html, into out.
func gallery(dir, out string) {
	css := string(must(os.ReadFile(filepath.Join(dir, "css", "mainplane.css"))))
	check(os.MkdirAll(filepath.Join(out, "components"), 0o755))
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Mainplane design system</title>` +
		`<link rel="stylesheet" href="mainplane.css"><link rel="stylesheet" href="gallery.css"><script src="mainplane.js" defer></script><script src="gallery.js" defer></script>` +
		`<body class="mp ds-page"><header><h1>Mainplane design system</h1><p>Link <code>/web/ds/1/mainplane.css</code> (and <code>mainplane.js</code> for icons and menus). Copy a component's markup to change it. Specs: <code>pkg/design/components/&lt;name&gt;/spec.md</code>.</p></header><nav>`)
	var names []string
	for _, m := range imports.FindAllStringSubmatch(css, -1) {
		names = append(names, m[1])
		fmt.Fprintf(&b, `<a href="#%s">%s</a>`, m[1], m[1])
	}
	b.WriteString("</nav><main>")
	for _, n := range names {
		snip := string(must(os.ReadFile(filepath.Join(dir, "components", n, "snippet.html"))))
		write(filepath.Join(out, "components", n+".html"), snip)
		spec := strings.SplitN(string(must(os.ReadFile(filepath.Join(dir, "components", n, "spec.md")))), "\n", 3)
		title, rule := strings.TrimPrefix(spec[0], "# "), ""
		if len(spec) > 1 {
			rule = spec[1]
		}
		fmt.Fprintf(&b, `<section id="%s"><h2>%s <button class="btn sm copy" data-src="components/%s.html">Copy</button></h2><p>%s</p><div class="pns"><div class="pn" data-theme="light">%s</div><div class="pn" data-theme="dark">%s</div></div></section>`,
			n, html.EscapeString(title), n, html.EscapeString(rule), snip, snip)
	}
	b.WriteString("</main>")
	write(filepath.Join(out, "index.html"), b.String())
	frame := string(must(os.ReadFile(filepath.Join(dir, "gallery", "gallery.css")))) + string(must(os.ReadFile(filepath.Join(dir, "gallery", "demo.css"))))
	write(filepath.Join(out, "gallery.css"), frame)
	write(filepath.Join(out, "gallery.js"), string(must(os.ReadFile(filepath.Join(dir, "gallery", "gallery.js")))))
}
