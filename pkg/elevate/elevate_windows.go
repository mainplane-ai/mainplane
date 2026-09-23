// Package elevate makes a command that needs root or admin work without the
// user typing sudo or opening an elevated shell first.
package elevate

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// tokenElevation is TOKEN_INFORMATION_CLASS TokenElevation, which the syscall
// package does not name.
const tokenElevation = 20

// Root returns when this process is elevated. Otherwise it runs this binary
// with args elevated, which shows the UAC prompt, and exits as that does. The
// elevated process gets a console of its own, so its output comes back
// through a file and is printed when it ends.
func Root(args ...string) {
	if elevated() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	out := filepath.Join(os.TempDir(), fmt.Sprintf("mainplane-elevated-%d.log", os.Getpid()))
	line := `"` + exe + `"`
	for _, a := range args {
		line += ` "` + a + `"`
	}
	// cmd's line sits in a single-quoted string, where a quote is doubled
	q := strings.NewReplacer("'", "''")
	ps := fmt.Sprintf(`$p = Start-Process cmd -ArgumentList '/c "%s > "%s" 2>&1"' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode`, q.Replace(line), q.Replace(out))
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if b, rerr := os.ReadFile(out); rerr == nil {
		_, _ = os.Stdout.Write(b)
		_ = os.Remove(out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}

func elevated() bool {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer func() { _ = t.Close() }()
	var e, n uint32
	return syscall.GetTokenInformation(t, tokenElevation, (*byte)(unsafe.Pointer(&e)), 4, &n) == nil && e != 0
}
