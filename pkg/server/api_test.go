package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mainplane-ai/mainplane/pkg/auth"
)

func TestKeys(t *testing.T) {
	a, routes := &api{dir: t.TempDir()}, http.NewServeMux()
	a.handle(routes)
	store := Auth(a.dir)
	mux := store.Bearer(&auth.Limit{}, routes)
	self, err := store.Issue(auth.Key, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := store.Issue(auth.Key, "web-2")
	for range 2 { // a reinstall adds a second default key
		if err := store.Add(auth.Key, DefaultKey, auth.Hash(t.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if !store.Check(auth.Key, self) {
		t.Fatal("web-1 refused")
	}
	as := func(secret, method, path string) *httptest.ResponseRecorder {
		w, r := httptest.NewRecorder(), httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		mux.ServeHTTP(w, r)
		return w
	}
	call := func(method, path string) *httptest.ResponseRecorder { return as(self, method, path) }
	if as("wrong", "GET", "/keys").Code != http.StatusUnauthorized || as("wrong", "DELETE", "/keys/web-2").Code != http.StatusUnauthorized {
		t.Fatal("a wrong key got past")
	}

	w := call("GET", "/keys")
	if body := w.Body.String(); strings.Contains(body, self) || strings.Contains(body, auth.Hash(self)) || strings.Contains(body, "hash") {
		t.Fatalf("GET /keys shows a secret or hash: %s", body)
	}
	var rows []keyRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Name != "web-1" || rows[1].Name != "web-2" || rows[2].Name != DefaultKey {
		t.Fatalf("rows %+v", rows)
	}
	if !rows[0].Self || rows[0].Used.IsZero() || rows[0].Created.IsZero() || rows[1].Self || !rows[1].Used.IsZero() || rows[2].Self || !rows[2].Harness || rows[0].Harness {
		t.Fatalf("rows %+v", rows)
	}

	for path, code := range map[string]int{"/keys/web-1": 409, "/keys/" + DefaultKey: 409, "/keys/web-3": 404, "/keys/web-2": 204} {
		if w := call("DELETE", path); w.Code != code {
			t.Errorf("DELETE %s: %d %s, want %d", path, w.Code, w.Body, code)
		}
	}
	if store.Check(auth.Key, other) || !store.Check(auth.Key, self) {
		t.Fatal("web-2 not revoked, or web-1 revoked")
	}
}
