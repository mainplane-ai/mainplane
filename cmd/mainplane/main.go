// mainplane is the device binary.
//
//	mainplane mainplaned <name> <harness host:port>    make this machine a worker
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mainplane-ai/mainplane/pkg/worker"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "mainplaned" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		worker.Dial(os.Args[3], worker.Local{Name: os.Args[2], Scratch: filepath.Join(home, ".mainplane"), Interps: worker.Default[runtime.GOOS]})
	}
	fmt.Fprintln(os.Stderr, "usage: mainplane mainplaned <name> <harness host:port>")
	os.Exit(2)
}
