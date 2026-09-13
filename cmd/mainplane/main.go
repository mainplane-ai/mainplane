// mainplane is the device binary.
//
//	mainplane mainplaned <name> <harness host:port>    make this machine a worker
//	mainplane install    <name> <harness host:port>    and again at every boot
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
	if len(os.Args) != 4 {
		usage()
	}
	name, addr := os.Args[2], os.Args[3]
	switch os.Args[1] {
	case "mainplaned":
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		worker.Dial(addr, worker.Local{Name: name, Scratch: filepath.Join(home, ".mainplane"), Interps: worker.Default[runtime.GOOS]})
	case "install":
		if err := worker.Install(name, addr); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mainplane mainplaned|install <name> <harness host:port>")
	os.Exit(2)
}
