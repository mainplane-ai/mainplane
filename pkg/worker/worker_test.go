package worker

import (
	"bufio"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWorkerInjectsKeyAndURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash test")
	}
	ds, err := newDrives(nil)
	if err != nil {
		t.Fatal(err)
	}
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	scratch := t.TempDir()
	go func() {
		_ = Serve(c2, Local{
			Name:    "w1",
			Scratch: scratch,
			Interps: []string{"bash"},
		}, ds)
	}()

	br := bufio.NewReader(c1)
	hello, err := Decode(br)
	if err != nil || hello.Kind != Hello {
		t.Fatalf("hello: %v %+v", err, hello)
	}

	// 1. Persistent run environment receives MAINPLANE_SESSION, MAINPLANE_KEY, MAINPLANE_URL.
	err = Encode(c1, Frame{
		Header: Header{
			ID:      "r1",
			Kind:    Run,
			Session: "s1111111",
			Key:     "mp_skey_test.s1111111:mac",
			URL:     "http://[fd00::1]:8080",
			Interp:  "bash",
			Timeout: 30,
		},
		Body: []byte(`printf "%s|%s|%s" "$MAINPLANE_SESSION" "$MAINPLANE_KEY" "$MAINPLANE_URL"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := readUntilResult(t, c1, br, "r1")
	want := "s1111111|mp_skey_test.s1111111:mac|http://[fd00::1]:8080"
	if strings.TrimSpace(out) != want {
		t.Fatalf("persistent env got %q, want %q", out, want)
	}

	// 2. Background job also receives MAINPLANE_SESSION, MAINPLANE_KEY, MAINPLANE_URL.
	err = Encode(c1, Frame{
		Header: Header{
			ID:      "j1",
			Kind:    Run,
			Job:     "job1",
			Session: "s1111111",
			Key:     "mp_skey_test.s1111111:mac",
			URL:     "http://[fd00::1]:8080",
			Interp:  "bash",
		},
		Body: []byte(`printf "%s|%s|%s\n" "$MAINPLANE_SESSION" "$MAINPLANE_KEY" "$MAINPLANE_URL"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobOut := readJobResult(t, c1, br)
	if !strings.Contains(jobOut, want) {
		t.Fatalf("job output got %q, want substring %q (log in %s)", jobOut, want, filepath.Join(scratch, "jobs"))
	}
}

func readUntilResult(t *testing.T, conn net.Conn, br *bufio.Reader, id string) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var b strings.Builder
	for {
		f, err := Decode(br)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.ID != id {
			continue
		}
		switch f.Kind {
		case Output:
			b.Write(f.Body)
		case Result:
			return b.String()
		case Error:
			t.Fatalf("worker error: %s", f.Body)
		}
	}
}

func readJobResult(t *testing.T, conn net.Conn, br *bufio.Reader) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		f, err := Decode(br)
		if err != nil {
			t.Fatalf("decode job: %v", err)
		}
		if f.Kind == Result && f.ID == "j1" {
			return string(f.Body)
		}
	}
}
