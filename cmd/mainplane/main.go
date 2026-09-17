// mainplane is the device binary: the worker, and the plainest connector.
//
//	mainplane mainplaned <name> <harness host:port>    make this machine a worker
//	mainplane install    <name> <harness host:port>    and again at every boot
//	mainplane <verb> <url> ...                         one verb per harness route, see cli.go
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
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "mainplaned", "install":
		if len(os.Args) != 4 {
			usage()
		}
		name, addr := os.Args[2], os.Args[3]
		if os.Args[1] == "install" {
			if err := worker.Install(name, addr); err != nil {
				log.Fatal(err)
			}
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		worker.Dial(addr, worker.Local{Name: name, Scratch: filepath.Join(home, ".mainplane"), Interps: worker.Default[runtime.GOOS]})
	default:
		cli(os.Args[1], os.Args[2:])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mainplane <verb> ...

  mainplaned <name> <harness host:port>   make this machine a worker
  install    <name> <harness host:port>   and again at every boot

  every verb below takes the harness url first, as http://host:port
  new        <url>                        POST /sessions, body from stdin: {"model","context","workers"} or {"from","n"}
  message    <url> <id> <text> [file...]  POST /sessions/{id}/records, one record per part
  tail       <url> <id> [after]           GET  /sessions/{id}/records, rendered
  chat       <url> <id>                   tail that follows; every stdin line is a message
  retry      <url> <id>                   POST /sessions/{id}/retry, step a failed or idle session from its tip
  stop       <url> <id>                   POST /sessions/{id}/stop
  info       <url> <id>                   GET  /sessions/{id}
  sessions   <url> [status]               GET  /sessions
  workers    <url>                        GET  /workers
  providers  <url>                        GET  /providers
`)
	os.Exit(2)
}
