package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A navigation gets the page at any path, every other request the API, and
// the frame a dashboard is shown in comes sandboxed.
func TestHandler(t *testing.T) {
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("api")) }))
	get := func(method, path string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		method, path string
		header       []string
		page         bool
	}{
		{"GET", "/", nil, false}, // a worker, the CLI
		{"GET", "/agents/x", []string{"Accept", "application/json"}, false},
		{"GET", "/agents/x", []string{"Sec-Fetch-Mode", "navigate", "Accept", "text/html"}, true},
		{"GET", "/", []string{"Accept", "text/html,*/*"}, true},                                // a browser too old to say
		{"GET", "/projects", []string{"Sec-Fetch-Mode", "cors", "Accept", "text/html"}, false}, // the page's own call
		{"POST", "/projects", []string{"Sec-Fetch-Mode", "navigate"}, false},
	} {
		w := get(c.method, c.path, c.header...)
		if page := w.Header().Get("Content-Security-Policy") == policy; page != c.page || page == (w.Body.String() == "api") || !strings.Contains(w.Header().Get("Vary"), "Sec-Fetch-Mode") {
			t.Errorf("%s %s %v: page %v, want %v; %q", c.method, c.path, c.header, page, c.page, w.Body)
		}
	}
	root, _ := build()
	if w := get("GET", root+"frame.html", "Sec-Fetch-Mode", "navigate"); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts;") || w.Header().Get("Cache-Control") != forever {
		t.Errorf("frame: %d %v", w.Code, w.Header())
	}
	if w := get("GET", "/web/0000000000000000/frame.html"); w.Code != 404 {
		t.Errorf("a file of another build: %d", w.Code)
	}
}
