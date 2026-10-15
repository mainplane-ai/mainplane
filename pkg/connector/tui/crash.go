package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	xterm "golang.org/x/term"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// childEnv marks the process that is the TUI under Supervise.
const childEnv = "MAINPLANE_TUI_CHILD"

// crashed finds the start of a Go crash in stderr, or the hang that
// Terminal.hung writes in the same shape.
var crashed = regexp.MustCompile(`(?m)^(panic|fatal error|hang): `)

// Supervise runs this binary again with the same arguments as the TUI, and
// waits. Only a parent survives every way the TUI can die, a fatal error and
// a hang included: when the child crashes, the parent restores the terminal,
// saves the report to ~/.mainplane/crash, and asks to send it to
// bugs.mainplane.ai. It returns only in the child.
func Supervise() {
	if os.Getenv(childEnv) != "" {
		debug.SetTraceback("all") // a crash shows every goroutine, the loop's too
		return
	}
	restore, err := term.Guard()
	if err != nil {
		log.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	var stderr bytes.Buffer // held until the end: a write would land on the TUI's screen
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, &stderr
	signal.Notify(make(chan os.Signal, 1), os.Interrupt) // the child gets Ctrl+C too, and decides
	if err := cmd.Run(); cmd.ProcessState == nil {
		log.Fatal(err)
	}
	code, out := cmd.ProcessState.ExitCode(), stderr.String()
	i := crashed.FindStringIndex(out)
	if code == -1 || code == 2 && i != nil { // killed, or crashed
		restore()
	}
	if code != 2 || i == nil {
		fmt.Fprint(os.Stderr, out)
		if code == -1 { // killed
			code = 1
		}
		os.Exit(code)
	}
	fmt.Fprint(os.Stderr, out[:i[0]])
	offer(crashReport(out[i[0]:]))
	os.Exit(2)
}

// crashReport is what a crash report holds: the release, the platform, the
// terminal, and the trace. Nothing of the session.
func crashReport(trace string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mainplane TUI crash\nversion: %s\nos: %s/%s\n", version.V, runtime.GOOS, runtime.GOARCH)
	if w, h, err := xterm.GetSize(int(os.Stdout.Fd())); err == nil {
		fmt.Fprintf(&b, "size: %dx%d\n", w, h)
	}
	for _, k := range []string{"TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORTERM"} {
		if v := os.Getenv(k); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	for _, k := range []string{"WT_SESSION", "TMUX", "SSH_TTY"} {
		if os.Getenv(k) != "" {
			fmt.Fprintf(&b, "%s: set\n", k)
		}
	}
	b.WriteString("\n" + trace)
	return b.String()
}

// offer saves the report, shows its start, and sends it when the user says
// yes.
func offer(r string) {
	lines := strings.SplitAfter(r, "\n")
	fmt.Fprint(os.Stderr, strings.Join(lines[:min(len(lines), 30)], ""))
	path, err := saveCrash(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nThe Mainplane TUI crashed. Saving the report failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "\nThe Mainplane TUI crashed. The whole report is in %s\n", path)
	}
	fmt.Fprint(os.Stderr, "Send it to bugs.mainplane.ai? It holds the version, OS, terminal and stack trace, nothing of your sessions. [Y/n] ")
	a, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a = strings.ToLower(strings.TrimSpace(a)); a != "" && a != "y" && a != "yes" {
		return
	}
	id, err := sendCrash(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Sending failed: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Sent. Thank you. It is report %d.\n", id)
}

func saveCrash(r string) (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(h, ".mainplane", "crash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().UTC().Format("2006-01-02T150405Z")+".txt")
	return path, os.WriteFile(path, []byte(r), 0o600)
}

// sendCrash posts the report as plain text with its own content type, so
// triage tells crashes from bug reports. The endpoint keeps 256 KB.
func sendCrash(r string) (int, error) {
	req, err := http.NewRequest("POST", "https://bugs.mainplane.ai", strings.NewReader(r[:min(len(r), 256<<10)]))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "text/x-mainplane-crash")
	req.Header.Set("User-Agent", fmt.Sprintf("mainplane/%s (%s/%s)", version.V, runtime.GOOS, runtime.GOARCH))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("bugs.mainplane.ai: %s", resp.Status)
	}
	var v struct{ ID int }
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v.ID, err
}
