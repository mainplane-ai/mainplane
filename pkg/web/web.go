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
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/apps/project"
)

//go:generate go run ../design/gen ../design dist/ds
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 ../design/css/mainplane.css --bundle --minify --loader:.woff2=file --asset-names=fonts/[name] --outdir=dist/ds --log-level=warning
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 ../design/js/mainplane.js --bundle --minify --format=iife --target=es2022 --outfile=dist/ds/mainplane.js --log-level=warning
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 src/main.ts --bundle --minify --format=esm --target=es2022 --log-level=warning --outfile=dist/app.js

//go:embed all:dist app.css frame.html
var files embed.FS

const (
	// script is the built page. A binary built before go generate ran has none.
	script  = "dist/app.js"
	unbuilt = "This mainplane-server was built without its web interface: run go generate ./pkg/web, then build it again.\n"
	// policy lets the page load only what this harness serves and call only
	// this harness. Its one frame is frame.html, which shows a dashboard, and
	// frame-src holds for the frame's whole life: a dashboard cannot send the
	// frame to another site, to leak what it read or to lend it the bridge.
	policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; font-src 'self'; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	// galleryPolicy is the page's policy plus inline style attributes, which
	// the snippets it shows carry, as a dashboard's copy of them does.
	galleryPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	// forever is how long a browser keeps a file whose URL has the hash of
	// the build in it: a new build is a new URL.
	forever = "public, max-age=31536000, immutable"
	// ds is where the design system is served. Dashboards link it and outlive
	// builds, so the URL has no hash: a breaking change is a new number.
	ds = "/web/ds/1/"
)

// host is a Host header that is safe in a policy.
var host = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)

var types = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8", ".woff2": "font/woff2"}

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
	_ = fs.WalkDir(files, "dist/ds", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := files.ReadFile(name)
			sum.Write(b)
		}
		return nil
	})
	return "/web/" + hex.EncodeToString(sum.Sum(nil))[:16] + "/", assets
}

// Handler serves the page to a browser's navigations, the page's files
// under /web/<hash of the build>/, and hands everything else to api.
func Handler(api http.Handler) http.Handler {
	root, assets := build()
	etag := `"` + strings.TrimSuffix(strings.TrimPrefix(root, "/web/"), "/") + `"`
	shell := `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Mainplane</title><link rel="icon" href="data:,">` +
		`<link rel="stylesheet" href="` + ds + `mainplane.css"><script src="` + ds + `mainplane.js" defer></script><link rel="stylesheet" href="` + root + `app.css"><script type="module" src="` + root + `app.js"></script>`
	if assets[path.Base(script)] == nil {
		shell = unbuilt
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if name, ok := strings.CutPrefix(r.URL.Path, ds); ok {
			if name == "" || strings.HasSuffix(name, "/") {
				name += "index.html"
			}
			b, err := files.ReadFile("dist/ds/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", types[path.Ext(name)])
			h.Set("Cache-Control", "no-cache")
			h.Set("ETag", etag)
			if path.Ext(name) == ".html" {
				h.Set("Content-Security-Policy", galleryPolicy)
			}
			h.Set("Access-Control-Allow-Origin", "*") // fonts load in CORS mode, and a dashboard's frame has the origin null
			h.Set("X-Content-Type-Options", "nosniff")
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write(b)
			return
		}
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
				h.Set("Content-Security-Policy", project.Dashboard(dsSource(r))) // of whatever the frame is told to show
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

// dsSource is the design system's files as sources of a policy, on the
// host the browser asked. Both schemes: a proxy may have ended TLS, and
// Safari matches no scheme-less source in a frame with an opaque origin.
func dsSource(r *http.Request) string {
	if !host.MatchString(r.Host) {
		return "" // no design system
	}
	p := r.Host + strings.TrimSuffix(ds, "1/")
	return "https://" + p + " http://" + p
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
