package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvClientValidatesPinnedURLs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	loginDir := filepath.Join(home, ".mainplane")
	if err := os.MkdirAll(loginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loginDir, "login.json"), []byte(`{"url":"https://admin.example.com","pinned":true,"key":"adminsecret","harness":"h1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAINPLANE_URL", "http://127.0.0.1:8080")
	t.Setenv("MAINPLANE_KEY", "mp_key_abc.def@http://127.0.0.1:9999")
	if c, ok := EnvClient(); ok || c != nil {
		t.Fatalf("expected malformed loopback pin to fail, got (%+v, %v)", c, ok)
	}
	if _, err := LoadScoped(context.Background()); err == nil {
		t.Fatal("LoadScoped should fail on malformed @ pin instead of falling back to login.json")
	}
	if _, err := Load(context.Background()); err == nil {
		t.Fatal("Load should fail on malformed @ pin instead of falling back to login.json")
	}

	t.Setenv("MAINPLANE_KEY", "mp_key_abc.def@https://bad.example.com:8443/path")
	if c, ok := EnvClient(); ok || c != nil {
		t.Fatalf("expected port/path pin to fail, got (%+v, %v)", c, ok)
	}

	t.Setenv("MAINPLANE_KEY", "mp_key_abc.def@")
	if c, ok := EnvClient(); ok || c != nil {
		t.Fatalf("expected empty @ pin to fail, got (%+v, %v)", c, ok)
	}
	if _, err := LoadScoped(context.Background()); err == nil {
		t.Fatal("LoadScoped should fail on empty @ pin")
	}

	t.Setenv("MAINPLANE_URL", "")
	t.Setenv("MAINPLANE_KEY", "mp_key_abc.def@https://mp.example.com")
	c, ok := EnvClient()
	if !ok || c == nil || c.Key != "mp_key_abc.def" || c.URL != "https://mp.example.com" {
		t.Fatalf("valid pinned token got (%+v, %v)", c, ok)
	}
}
