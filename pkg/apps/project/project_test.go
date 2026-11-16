package project

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A store takes no write past its cap, and a file written again counts once.
func TestStoreCap(t *testing.T) {
	maxStore = 10
	a := &App{dir: t.TempDir(), projects: map[string]Project{"p": {ID: "p"}}}
	if err := os.Mkdir(filepath.Join(a.dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, n int) error { return a.write(httptest.NewRecorder(), "p", name, make([]byte, n)) }
	if err := errors.Join(write("a", 6), write("a", 7), write("b", 3)); err != nil {
		t.Fatal(err)
	}
	if err := write("b", 4); !errors.Is(err, errFull) {
		t.Fatalf("a write past the cap: %v", err)
	}
	if err := a.write(httptest.NewRecorder(), "p", "a", nil); err != nil {
		t.Fatal(err)
	}
	if err := write("b", 10); err != nil || a.projects["p"].Counter != 5 {
		t.Fatalf("after a delete: %v, counter %d", err, a.projects["p"].Counter)
	}
}
