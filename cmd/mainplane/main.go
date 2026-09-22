// mainplane is the device binary: the worker, and the plainest connector.
//
//	mainplane worker  [join token]    make this machine a worker, named by its hostname
//	mainplane install <join token>    and again at every boot; the token goes in ~/.mainplane/join
//	mainplane login   <api key>       remember the harness and the key in ~/.mainplane/login.json
//	mainplane version                 the release this binary was built from
//	mainplane <verb> ...              one verb per harness route, see cli.go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "worker", "install":
		if len(os.Args) > 3 || os.Args[1] == "install" && len(os.Args) != 3 {
			usage()
		}
		token := ""
		if len(os.Args) == 3 {
			token = os.Args[2]
		} else {
			b, err := os.ReadFile(worker.JoinPath(home()))
			if err != nil {
				log.Fatal("no join token: mainplane worker <join token>, or mainplane install <join token> once")
			}
			token = string(b)
		}
		addr, secret, err := auth.Parse(auth.Join, token)
		if err != nil {
			log.Fatal(err)
		}
		if os.Args[1] == "install" {
			if err := worker.Install(token); err != nil {
				log.Fatal(err)
			}
			return
		}
		name, err := os.Hostname()
		if err != nil {
			log.Fatal(err)
		}
		worker.Dial(addr, worker.Local{Name: name, Secret: secret, Scratch: filepath.Join(home(), ".mainplane"), Interps: worker.Default[runtime.GOOS]})
	case "login":
		if len(os.Args) != 3 {
			usage()
		}
		url, key, err := auth.Parse(auth.Key, os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		b, _ := json.Marshal(client{URL: url, Key: key})
		if err := os.MkdirAll(filepath.Dir(loginPath()), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(loginPath(), b, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Println(url)
	case "version":
		if len(os.Args) != 2 {
			usage()
		}
		fmt.Println(version.V)
	default:
		cli(os.Args[1], os.Args[2:])
	}
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return h
}

func loginPath() string { return filepath.Join(home(), ".mainplane", "login.json") }

func usage() {
	fmt.Fprint(os.Stderr, `usage: mainplane <verb> ...

  worker     [join token]            make this machine a worker, named by its hostname; no token reads the installed one
  install    <join token>            and again at every boot; the token goes in ~/.mainplane/join
  login      <api key>               remember the harness and the key; every verb below uses them
  version                            the release this binary was built from

  new                                POST /sessions, body from stdin: {"model","context","workers"} or {"from","n"}
  message    <id> <text> [file...]   POST /sessions/{id}/records, one record per part
  tail       <id> [after]            GET  /sessions/{id}/records, rendered
  chat       <id>                    tail that follows; every stdin line is a message
  retry      <id>                    POST /sessions/{id}/retry, step a failed or idle session from its tip
  stop       <id>                    POST /sessions/{id}/stop
  info       <id>                    GET  /sessions/{id}
  sessions   [status]                GET  /sessions
  workers                            GET  /workers
  providers                          GET  /providers
`)
	os.Exit(2)
}
