// mainplaned makes this machine a worker: it dials the harness, serves it
// until the connection ends, and dials again.
//
//	mainplaned <name> <harness host:port>
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to a minute, so a harness down for a day is not hammered.
const (
	redialMin = 200 * time.Millisecond
	redialMax = time.Minute
)

// interps is the interpreter each OS ships with. The first is the default.
var interps = map[string][]string{"windows": {"pwsh"}, "linux": {"bash"}, "darwin": {"bash"}}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mainplaned <name> <harness host:port>")
		os.Exit(2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	l := worker.Local{Name: os.Args[1], Scratch: filepath.Join(home, ".mainplane"), Interps: interps[runtime.GOOS]}
	wait := redialMin
	for {
		conn, err := net.Dial("tcp", os.Args[2])
		if err == nil {
			log.Printf("connected to %s as %s", os.Args[2], l.Name)
			wait = redialMin
			err = worker.Serve(conn, l)
		}
		log.Printf("%v, redial in %s", err, wait)
		time.Sleep(wait)
		wait = min(2*wait, redialMax)
	}
}
