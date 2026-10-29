// mainplane is the device binary: the worker, and the plainest connector.
//
//	mainplane                         the TUI; mainplane <session id> opens on that session; on the harness's machine
//	                                  with no provider yet, the quickstart
//	mainplane quickstart              the TUI on the quickstart, which the server install opens
//	mainplane worker  [<network> <device code>]   make this machine a worker, named by its hostname
//	mainplane install <network> <device code>     and again at every boot, as a service; asks for sudo or UAC, code still runs as you
//	mainplane login   <api key>       find the harness, remember it and the key in ~/.mainplane/login.json, and become its release
//	mainplane uninstall               remove the worker service and the CLI; ~/.mainplane stays
//	mainplane status                  the harness logged in to, and this worker on the mesh from its local socket
//	mainplane version                 the release this binary was built from
//	mainplane <verb> ...              one verb per harness route, see cli.go
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui"
	"github.com/mainplane-ai/mainplane/pkg/elevate"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// startTime bounds finding the harness and asking its release before the TUI
// opens.
const startTime = 15 * time.Second

func main() {
	if len(os.Args) == 1 || len(os.Args) == 2 && statefile.ValidID(os.Args[1]) {
		app(strings.Join(os.Args[1:], ""), false)
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "worker" && os.Args[2] == "remove" {
		cli("worker remove", os.Args[3:])
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "workers" {
		cli("workers set", os.Args[2:])
		return
	}
	switch os.Args[1] {
	case "worker", "install":
		if len(os.Args) > 4 || os.Args[1] == "install" && len(os.Args) < 3 {
			usage()
		}
		work(os.Args[1] == "install", os.Args[2:])
	case "elevated-update": // internal: update re-running itself as root
		if len(os.Args) != 3 {
			usage()
		}
		update(os.Args[2])
	case "quickstart":
		if len(os.Args) != 2 {
			usage()
		}
		app("", true)
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
	c, was, v, err := connector.Login(context.Background(), token)
	if err != nil {
		log.Fatal(err)
	}
	switch was.Harness {
	case c.Harness:
		fmt.Println("already logged in to this mainplane-server")
	case "":
		fmt.Println("logged in")
	default:
		fmt.Printf("logged in; switched from the mainplane-server at %s\n", cmp.Or(was.URL, "this machine"))
	}
	if !version.Match(v) {
		update(v)
	}
}

// app opens the TUI, on login when there is none, on the quickstart with
// quick or on the harness's machine while it has no provider, and on
// session id when it is given. When this binary is not the harness's release it becomes it
// first, in the plain terminal where sudo or UAC can ask, then runs again.
// It runs as the child of Supervise, which outlives a crash or a hang.
func app(id string, quick bool) {
	tui.Supervise()
	ctx, cancel := context.WithTimeout(context.Background(), startTime)
	defer cancel()
	var cp *connector.Client
	c, err := connector.Load(ctx)
	switch {
	case err == nil:
		v, err := c.Version(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if !version.Match(v) {
			rerun(v)
		}
		if !quick && id == "" && c.Local != "" {
			ps, err := c.Providers(ctx)
			if err != nil {
				log.Fatal(err)
			}
			quick = len(ps) == 0
		}
		cp = c
	case errors.Is(err, fs.ErrNotExist) && id == "" && !quick:
	case errors.Is(err, fs.ErrNotExist):
		log.Fatal("not logged in: mainplane login <api key>")
	default:
		log.Fatal(err)
	}
	v, err := tui.Run(cp, id, quick)
	if err != nil {
		log.Fatal(err)
	}
	if v != "" {
		rerun(v)
	}
}

// rerun updates this binary to release v through elevated-update, which asks
// for root itself when it needs it, then runs the new binary with the same
// arguments and exits with its code.
func rerun(v string) {
	fmt.Printf("Updating to match your Mainplane (%s)\n", v)
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if err := attached(exe, "elevated-update", v).Run(); err != nil {
		log.Fatalf("update to %s failed: %v", v, err)
	}
	cmd := attached(exe, os.Args[1:]...)
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		log.Fatal(err)
	}
	os.Exit(cmd.ProcessState.ExitCode())
}

func attached(exe string, args ...string) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// work installs the worker, or is the worker: with a network name and device
// code, or the join token they trade for, as whoever runs it; with none, as
// the service install left, whose scratch is the operator's. A machine
// already a worker of the token's harness keeps its install and its token,
// and the install asks for no root. A token goes to the elevated install, and
// admin's install passes one, but no person types it.
func work(install bool, args []string) {
	// A Linux service is a systemd unit. Without systemd, as in most
	// containers, the worker runs by hand, before anything is written.
	if fi, err := os.Stat("/run/systemd/system"); install && runtime.GOOS == "linux" && (err != nil || !fi.IsDir()) {
		log.Fatalf("mainplane install needs systemd to run the worker at every boot, and this machine has none.\nUse: mainplane worker %s   (keep it running with your own supervisor)", strings.Join(args, " "))
	}
	token, op := "", (*user.User)(nil)
	var err error
	switch len(args) {
	case 0:
		if token, op, err = worker.Installed(); err != nil {
			log.Fatalf("not joined: mainplane worker <network> <device code>, or mainplane install <network> <device code> once: %v", err)
		}
	case 1:
		token = args[0]
	case 2:
		if token, err = redeem(args[0], args[1]); err != nil {
			log.Fatal(err)
		}
		// a worker started by hand restarts after an update with its
		// arguments: the token, which a new code does not refuse
		os.Args = []string{os.Args[0], os.Args[1], token}
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

// redeem trades device code for the join token of the harness that holds
// network name.
func redeem(name, code string) (string, error) {
	code, err := auth.Code(code)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	key, err := pointer.Named(ctx, strings.ToLower(name))
	if err != nil {
		return "", err
	}
	url, err := pointer.Find(ctx, key, "")
	if err != nil {
		return "", err
	}
	token, err := auth.Redeem(ctx, url, key, code)
	if errors.Is(err, auth.ErrCode) {
		return "", fmt.Errorf("wrong device code for network %s", name)
	}
	return token, err
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

// update makes this binary release v, the logged-in harness's. A binary only
// root may replace, like a Linux or macOS worker's, updates elevated through
// the internal verb elevated-update; v goes along, since sudo may give root a
// home without the login.
func update(v string) {
	if v == version.V {
		fmt.Printf("mainplane is %s\n", v)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if f, err := os.CreateTemp(filepath.Dir(exe), ".update-*"); err != nil {
		elevate.Root("elevated-update", v)
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	fmt.Printf("mainplane %s -> %s, your mainplane-server's release: downloading\n", version.V, v)
	if err := release.Install(exe, "mainplane", v); err != nil {
		log.Fatal(err)
	}
}

// status prints the harness this CLI is logged in to, if any, then what the
// worker on this machine says of its node on the mesh; it needs no root.
func status(args []string) {
	if len(args) != 0 {
		usage()
	}
	c, err := login()
	v := ""
	if err == nil {
		v, err = c.Version(context.Background())
	}
	if err == nil {
		fmt.Printf("mainplane-server version: %s\n", v)
		var ferr error
		if c.Local != "" || c.Mesh != "" { // calls go off the tunnel; the URL shown must still be current
			ferr = c.Find(context.Background())
		}
		if ferr != nil {
			fmt.Printf("mainplane-server url: %v\n", ferr)
		} else {
			fmt.Printf("mainplane-server url: %s\n", c.URL)
		}
		if c.Local != "" {
			fmt.Printf("mainplane-server on this machine: %s\n", c.Local)
		}
		if c.Mesh != "" {
			fmt.Printf("mainplane-server on the mesh: %s\n", c.Mesh)
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

func usage() {
	fmt.Fprint(os.Stderr, `usage: mainplane [<session id> | <verb> ...]

                                     no verb opens the TUI: login, then the session given or a new one; on the
                                     harness's machine with no provider yet, the quickstart
  quickstart                         the TUI on the quickstart: a provider, workers, a connector, a drive
  worker     [<network> <code>]      make this machine a worker, named by its hostname, with the network name and
                                     device code mainplane-server join prints; none is the installed worker's
  install    <network> <code>        and again at every boot, as a service; asks for sudo or UAC, code still runs as you
  login      <api key>               remember the harness and the key, which every verb below uses, and become the
                                     harness's release
  uninstall                          remove the worker service and the CLI; asks for sudo or UAC; ~/.mainplane stays
  status                             the harness logged in to, then this worker on the mesh: its name and address,
                                     and each peer's address and path, direct or through the relay
  version                            the release this binary was built from

  new                                POST /sessions, body from stdin: {"model","context_limit","workers","tools","input","params",
                                     "preset","name"} or {"from","n"};
                                     params are the vendor's own request fields, max_tokens included where its API requires it
  message    <id> <text> [file...]   POST /sessions/{id}/records, one record per part
  tail       <id> [after]            GET  /sessions/{id}/records, rendered
  chat       [id]                    tail that follows; every stdin line is a message. No id: the session updated last
  retry      <id>                    POST /sessions/{id}/retry, step a failed or idle session from its tip
  stop       <id>                    POST /sessions/{id}/stop: cut a running step, kill its environments and jobs
  delete     <id>                    DELETE /sessions/{id}: stop the session, then it is gone for good
  info       <id>                    GET  /sessions/{id}
  name       <id> [text]             PUT  /sessions/{id}/name: the session's name; no text removes it
  sessions   [status]                GET  /sessions
  workers                            GET  /workers
  workers    <id> <name>...          PUT  /sessions/{id}/workers: the session's workers are these from its next step on
  worker     remove <name>           DELETE /workers/{name}: it leaves the mesh for good; mainplane-server join cycle
                                     too keeps the device code it had from joining machines again
  providers                          GET  /providers: each one's name, shape and url
  key        <provider> <key>        PUT  /providers/{provider}: the harness saves the key, with the shape and url
                                     the catalog gives the provider, in its config, and serves it at once
  signin     <provider>              POST /providers/{provider}/login: sign in where a provider takes no key, such as
                                     chatgpt; prints the URL to open, reads what to paste back, waits for the end
`)
	os.Exit(2)
}
