// Package web is the interface a browser gets at the harness's own URL: one
// page of plain TypeScript, built by go generate into dist and embedded. A
// navigation gets the page at every path and every other request gets the
// API as before, so the page and the API share their URLs and one origin:
// no CORS, and no cookie, since the page sends the key in a header.
package web

import (
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/apps/project"
)

//go:generate go run ../design/gen ../design dist/ds
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 ../design/css/mainplane.css --bundle --minify --loader:.woff2=file --asset-names=fonts/[name] --outdir=dist/ds --log-level=warning
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 ../design/js/mainplane.js --bundle --minify --format=iife --target=es2022 --outfile=dist/ds/mainplane.js --log-level=warning
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 src/main.ts --bundle --minify --loader:.svg=text --format=esm --target=es2022 --log-level=warning --outfile=dist/app.js
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 vendor/temml.mjs --minify --format=esm --target=es2022 --log-level=warning --outfile=dist/math.js
//go:generate go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 vendor/jsqr.mjs --minify --format=esm --target=es2022 --log-level=warning --outfile=dist/qr.js

//go:embed all:dist app.css frame.html favicon.svg pwa
var embedded embed.FS

// files is the page's files: the embedded build, or, for development, the
// pkg/web directory MAINPLANE_WEB_DEV names, read again at every request so
// that a rebuild shows on a reload.
var files fs.FS = embedded

const (
	// script is the built page. A binary built before go generate ran has none.
	script  = "dist/app.js"
	unbuilt = "This mainplane-server was built without its web interface: run go generate ./pkg/web, then build it again.\n"
	// policy lets the page load only what this harness serves, its manifest too and call only
	// this harness. Its one frame is frame.html, which shows a dashboard, and
	// frame-src holds for the frame's whole life: a dashboard cannot send the
	// frame to another site, to leak what it read or to lend it the bridge.
	policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; font-src 'self'; connect-src 'self'; frame-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
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

var types = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8", ".woff2": "font/woff2", ".png": "image/png", ".webmanifest": "application/manifest+json"}

// build is the page's files by name, and the path they are served under,
// which has their hash in it.
func build() (root string, assets map[string][]byte) {
	assets, sum := map[string][]byte{}, sha256.New()
	for _, name := range []string{script, "dist/math.js", "dist/qr.js", "app.css", "frame.html", "pwa/manifest.webmanifest", "pwa/icon-180.png", "pwa/icon-512.png", "pwa/maskable-512.png"} {
		if b, err := fs.ReadFile(files, name); err == nil {
			assets[path.Base(name)] = b
			sum.Write(b)
		}
	}
	_ = fs.WalkDir(files, "dist/ds", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(files, name)
			sum.Write(b)
		}
		return nil
	})
	return "/web/" + hex.EncodeToString(sum.Sum(nil))[:16] + "/", assets
}

// Handler serves the page to a browser's navigations, the page's files
// under /web/<hash of the build>/, and hands everything else to api.
func Handler(api http.Handler) http.Handler {
	if dir := os.Getenv("MAINPLANE_WEB_DEV"); dir != "" {
		files = os.DirFS(dir)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(api).ServeHTTP(w, r) })
	}
	return handler(api)
}

func handler(api http.Handler) http.Handler {
	root, assets := build()
	icon, _ := fs.ReadFile(files, "favicon.svg") // the Mainplane mark, inline: the policy allows data: images
	etag := `"` + strings.TrimSuffix(strings.TrimPrefix(root, "/web/"), "/") + `"`
	shell := `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Mainplane</title>` +
		// Added to a home screen it opens as an app: its icon, and the bar above it in the page's color.
		`<link rel="manifest" href="` + root + `manifest.webmanifest"><link rel="apple-touch-icon" href="` + root + `icon-180.png"><meta name="theme-color" content="#F8F8F5" media="(prefers-color-scheme: light)"><meta name="theme-color" content="#040607" media="(prefers-color-scheme: dark)">` +
		`<link rel="icon" href="data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString(icon) + `">` +
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
			b, err := fs.ReadFile(files, "dist/ds/"+name)
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
			send(w, r, b)
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
				if r.URL.Query().Has("file") {
					h.Set("Content-Security-Policy", project.Sandbox) // a file of a store, as the store serves it: no network
				}
			}
			send(w, r, b)
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

// send writes a file, compressed for a browser that takes it unless it is a
// font, which is compressed already: the page's files are text, and a
// smaller body is fewer round trips on a new connection.
func send(w http.ResponseWriter, r *http.Request, b []byte) {
	w.Header().Add("Vary", "Accept-Encoding")
	if path.Ext(r.URL.Path) == ".woff2" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	z := gzip.NewWriter(w)
	_, _ = z.Write(b)
	_ = z.Close()
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
