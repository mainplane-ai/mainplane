// mainplane is the device binary: the worker, and the plainest connector.
//
//	mainplane worker  [join token]    make this machine a worker, named by its hostname
//	mainplane install <join token>    and again at every boot, as a service; asks for sudo or UAC, code still runs as you
//	mainplane login   <api key>       find the harness, remember it and the key in ~/.mainplane/login.json, and become its release
//	mainplane update  [version]       become that release, by default the logged-in harness's, else the latest stable
//	mainplane uninstall               remove the worker service and the CLI; ~/.mainplane stays
//	mainplane status                  the harness logged in to, and this worker on the mesh from its local socket
//	mainplane version                 the release this binary was built from
//	mainplane <verb> ...              one verb per harness route, see cli.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/elevate"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	if len(os.Args) > 2 && os.Args[1] == "worker" && os.Args[2] == "remove" {
		cli("worker remove", os.Args[3:])
		return
	}
	switch os.Args[1] {
	case "worker", "install":
		if len(os.Args) > 3 || os.Args[1] == "install" && len(os.Args) != 3 {
			usage()
		}
		work(os.Args[1] == "install", os.Args[2:])
	case "update":
		if len(os.Args) > 3 {
			usage()
		}
		update(strings.Join(os.Args[2:], ""))
	case "uninstall":
		if len(os.Args) != 2 {
			usage()
		}
		elevate.Root(os.Args[1:]...)
		if err := worker.Uninstall(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("mainplane uninstalled")
	case "file", "js":
		operator(os.Args[1:])
	case "login":
		if len(os.Args) != 3 {
			usage()
		}
		logIn(os.Args[2])
	case "status":
		status(os.Args[2:])
	case "version":
		if len(os.Args) != 2 {
			usage()
		}
		fmt.Println(version.V)
	default:
		cli(os.Args[1], os.Args[2:])
	}
}

// logIn finds the harness of an api key, checks that it takes the key, and
// remembers both, saying whether this CLI was logged in to it or to another
// harness before. Then the CLI becomes the harness's release.
func logIn(token string) {
	harness, key, err := auth.Parse(auth.Key, token)
	if err != nil {
		log.Fatal(err)
	}
	url, err := pointer.Find(context.Background(), harness, "")
	if err != nil {
		log.Fatal(err)
	}
	c := client{URL: url, Key: key, Harness: harness}
	v, err := c.harness()
	if err != nil {
		log.Fatal(err)
	}
	var was client // a login file that does not read is written anew
	if b, err := os.ReadFile(loginPath()); err == nil {
		_ = json.Unmarshal(b, &was)
	}
	if err := c.save(); err != nil {
		log.Fatal(err)
	}
	switch was.Harness {
	case harness:
		fmt.Println("already logged in to this mainplane-server")
	case "":
		fmt.Println("logged in")
	default:
		fmt.Printf("logged in; switched from the mainplane-server at %s\n", was.URL)
	}
	if !version.Match(v) {
		update(v)
	}
}

// work installs the worker, or is the worker: with a token given, as whoever
// runs it; without, as the service install left, whose scratch is the
// operator's. A machine already a worker of the token's harness keeps its
// install and its token, and the install asks for no root.
func work(install bool, args []string) {
	token, op := "", (*user.User)(nil)
	if len(args) == 1 {
		token = args[0]
	} else {
		var err error
		if token, op, err = worker.Installed(); err != nil {
			log.Fatalf("no join token: mainplane worker <join token>, or mainplane install <join token> once: %v", err)
		}
	}
	key, secret, err := auth.Parse(auth.Join, token)
	if err != nil {
		log.Fatal(err)
	}
	if install {
		if h, name, err := mesh.Joined(); err == nil && h == key {
			fmt.Printf("this machine is already worker %s of this mainplane-server\n", name)
			return
		}
		elevate.Root("install", token)
		if err := worker.Install(token); err != nil {
			log.Fatal(err)
		}
		return
	}
	name, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	dir := ""
	if op != nil {
		dir = op.HomeDir
	} else {
		dir = home()
	}
	if err := worker.Work(key, worker.Local{Name: name, Secret: secret, Scratch: filepath.Join(dir, ".mainplane"), Interps: worker.Default[runtime.GOOS], Operator: op}); err != nil {
		log.Fatal(err)
	}
}

// operator is a root worker's own work, run as the operator; not for people:
// `file read|write <path>`, one read or write, and `js <dir>`, which readies
// the js interpreter's folder.
func operator(args []string) {
	var err error
	switch {
	case args[0] == "file" && len(args) == 3:
		err = worker.File(args[1], args[2])
	case args[0] == "js" && len(args) == 2:
		err = worker.JS(args[1])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// update makes this binary release v: by default the release of the harness
// it is logged into, or the latest stable one when logged into none. A
// binary only root may replace, like a Linux or macOS worker's, updates
// elevated; v goes along, since sudo may give root a home without the login.
func update(v string) {
	if v == "" {
		c, err := login()
		if err == nil {
			v, err = c.harness()
		} else {
			v, err = release.Latest()
		}
		if err != nil {
			log.Fatal(err)
		}
	}
	if v == version.V {
		fmt.Printf("mainplane is %s\n", v)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if f, err := os.CreateTemp(filepath.Dir(exe), ".update-*"); err != nil {
		elevate.Root("update", v)
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	if err := release.Install(exe, "mainplane", v); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("mainplane %s -> %s\n", version.V, v)
}

// status prints the harness this CLI is logged in to, if any, then what the
// worker on this machine says of its node on the mesh; it needs no root.
func status(args []string) {
	if len(args) != 0 {
		usage()
	}
	c, err := login()
	v := ""
	if err == nil && c.mesh != "" { // calls go over the mesh; the URL shown must still be current
		err = c.find()
	}
	if err == nil {
		v, err = c.harness()
	}
	if err == nil {
		fmt.Printf("mainplane-server version: %s\nmainplane-server url: %s\n", v, c.URL)
		if c.mesh != "" {
			fmt.Printf("mainplane-server on the mesh: %s\n", c.mesh)
		}
		fmt.Println()
	} else if !errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("mainplane-server: %v\n\n", err)
	}
	s, err := mesh.Status()
	if errors.Is(err, mesh.ErrNoWorker) {
		fmt.Println(mesh.ErrNoWorker)
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(s)
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
  install    <join token>            and again at every boot, as a service; asks for sudo or UAC, code still runs as you
  login      <api key>               remember the harness and the key, which every verb below uses, and become the
                                     harness's release
  update     [version]               become that release, by default the logged-in harness's, else the latest stable
  uninstall                          remove the worker service and the CLI; asks for sudo or UAC; ~/.mainplane stays
  status                             the harness logged in to, then this worker on the mesh: its name and address,
                                     and each peer's address and path, direct or through the relay
  version                            the release this binary was built from

  new                                POST /sessions, body from stdin: {"model","context_limit","workers","params"} or {"from","n"};
                                     params are the vendor's own request fields, max_tokens included where its API requires it
  message    <id> <text> [file...]   POST /sessions/{id}/records, one record per part
  tail       <id> [after]            GET  /sessions/{id}/records, rendered
  chat       [id]                    tail that follows; every stdin line is a message. No id: the session updated last
  retry      <id>                    POST /sessions/{id}/retry, step a failed or idle session from its tip
  stop       <id>                    POST /sessions/{id}/stop
  info       <id>                    GET  /sessions/{id}
  sessions   [status]                GET  /sessions
  workers                            GET  /workers
  worker     remove <name>           DELETE /workers/{name}: it leaves the mesh for good; revoke its join secret too
                                     to keep that secret from joining machines again
  providers                          GET  /providers
  key        <provider> <key>        PUT  /providers/{provider}: the harness saves the key in its config, and serves
                                     the provider at once
`)
	os.Exit(2)
}
