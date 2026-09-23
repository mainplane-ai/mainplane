package elevate

import (
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

// Is says whether this process runs elevated.
func Is() bool {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer func() { _ = t.Close() }()
	var e, n uint32
	return syscall.GetTokenInformation(t, tokenElevation, (*byte)(unsafe.Pointer(&e)), 4, &n) == nil && e != 0
}

// Run runs this binary with args elevated, which shows the UAC prompt, and
// returns its exit code. The elevated process gets a console of its own, so
// its output comes back through a file and is printed when it ends. Args are
// versions and paths, and no Windows path holds a quote.
func Run(args ...string) int {
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
	return code(err)
}
