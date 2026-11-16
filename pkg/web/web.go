// Package web is the interface a browser gets at the harness's own URL: one
// page of plain TypeScript, built by go generate into dist and embedded. A
// navigation gets the page at every path and every other request gets the
// API as before, so the page and the API share their URLs and one origin:
// no CORS, and no cookie, since the page sends the key in a header.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/apps/project"
)

//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 src/main.ts --bundle --minify --format=esm --target=es2022 --log-level=warning --outfile=dist/app.js

//go:embed all:dist app.css frame.html
var files embed.FS

const (
	// script is the built page. A binary built before go generate ran has none.
	script  = "dist/app.js"
	unbuilt = "This mainplane-server was built without its web interface: run go generate ./pkg/web, then build it again.\n"
	// policy lets the page load only what this harness serves and call only
	// this harness. Its one frame is frame.html, which shows a dashboard.
	policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	// forever is how long a browser keeps a file whose URL has the hash of
	// the build in it: a new build is a new URL.
	forever = "public, max-age=31536000, immutable"
)

var types = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8"}

// build is the page's files by name, and the path they are served under,
// which has their hash in it.
func build() (root string, assets map[string][]byte) {
	assets, sum := map[string][]byte{}, sha256.New()
	for _, name := range []string{script, "app.css", "frame.html"} {
		if b, err := files.ReadFile(name); err == nil {
			assets[path.Base(name)] = b
			sum.Write(b)
		}
	}
	return "/web/" + hex.EncodeToString(sum.Sum(nil))[:16] + "/", assets
}

// Handler serves the page to a browser's navigations, the page's files
// under /web/<hash of the build>/, and hands everything else to api.
func Handler(api http.Handler) http.Handler {
	root, assets := build()
	shell := `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Mainplane</title>` +
		`<link rel="stylesheet" href="` + root + `app.css"><script type="module" src="` + root + `app.js"></script>`
	if assets[path.Base(script)] == nil {
		shell = unbuilt
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if name, ok := strings.CutPrefix(r.URL.Path, "/web/"); ok {
			b, ok := assets[strings.TrimPrefix("/web/"+name, root)]
			if !ok {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", types[path.Ext(name)])
			h.Set("Cache-Control", forever)
			h.Set("X-Content-Type-Options", "nosniff")
			if path.Base(name) == "frame.html" {
				h.Set("Content-Security-Policy", project.Sandbox) // of whatever the frame is told to show
			}
			_, _ = w.Write(b)
			return
		}
		h.Add("Vary", "Accept, Sec-Fetch-Mode")
		if !navigation(r) {
			api.ServeHTTP(w, r)
			return
		}
		h.Set("Content-Type", types[".html"])
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write([]byte(shell))
	})
}

// navigation is whether a browser asks for a page to show: it says so, or,
// one too old to say, asks for HTML. Workers, the CLI and the page's own
// calls do neither.
func navigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
