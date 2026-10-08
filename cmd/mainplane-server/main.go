// mainplane-server is the server binary: the harness, and the credentials
// that reach it. A new key or join prints its token once; the table holds
// only hashes.
//
//	mainplane-server up   <config.json>
//	mainplane-server install [config.json]    and again at every boot, as a service, behind a quick tunnel; asks for sudo or admin itself;
//	                                          with none, keeps an installed config; prints a new join token "default", the one before it refused,
//	                                          and a new api key "default", the ones before it kept
//	mainplane-server tunnel <url> <cloudflared token> | quick   the installed harness moves to the user's own tunnel, or back
//	mainplane-server key  new <name> | revoke <name> | list    on the installed harness, as root
//	mainplane-server join new <name> [ephemeral] | revoke <name> | list
//	mainplane-server update [version]         the installed harness becomes release version, the latest stable by default
//	mainplane-server uninstall                the service, binary and admin worker go; sessions and the auth table stay
//	mainplane-server version
package main

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/elevate"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/server"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

func main() {
	args, verb := os.Args[1:], ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch {
	case verb == "version" && len(args) == 1:
		fmt.Println(version.V)
	case verb == "update" && len(args) <= 2:
		elevate.Root(args...)
		fatal(server.Update(strings.Join(args[1:], "")))
	case verb == "uninstall" && len(args) == 1:
		elevate.Root(args...)
		fatal(server.Uninstall())
		fmt.Println("harness and its admin worker uninstalled")
	case verb == "up" && len(args) == 2:
		fatal(server.Up(args[1]))
	case verb == "install":
		h, fresh := config(args[1:])
		fatal(h.Expand())
		if !elevate.Is() {
			var code int
			if h, code = install(h); code != 0 {
				os.Exit(code)
			}
			if fresh { // the elevated install handed back; from a config file it printed itself
				done(h)
				network(h, true)
			}
			return
		}
		stop := spin("installing mainplane-server")
		// a plain install, the only one with a key yet, keeps the config there is
		keep := h.Key != ""
		err := server.Install(&h.Config, keep)
		stop("")
		fatal(err)
		h.Key = cmp.Or(h.Key, rand.Text())
		// beside the default keys before it, so a reinstall logs no connector out
		fatal(server.Auth(server.Dir).Add(auth.Key, defaultName, h.Key))
		k, err := server.Key(server.Dir)
		fatal(err)
		h.Harness = pointer.Encode(k)
		secret := rand.Text()
		fatal(server.Auth(server.Dir).Set(auth.Join, defaultName, secret))
		h.Join = auth.Token(auth.Join, h.Harness, secret)
		if keep && !fresh { // an unelevated install handed over; hand back what it prints
			b, err := json.Marshal(h)
			fatal(err)
			fatal(os.WriteFile(args[1], b, 0o600))
			return
		}
		done(h)
		network(h, fresh)
	case verb == "tunnel":
		move(args)
	case (verb == auth.Key || verb == auth.Join) && (len(args) == 2 && args[1] == "list" || len(args) == 3 && (args[1] == "new" || args[1] == "revoke")),
		verb == auth.Join && len(args) == 4 && args[1] == "new" && args[3] == "ephemeral":
		elevate.Root(args...)
		if _, err := server.Load(server.Conf); err != nil {
			log.Fatalf("no installed harness: %v", err)
		}
		table(verb, args[1:])
	default:
		usage()
	}
}

// defaultName names the api key and the join secret every install makes
// anew, so the first connector and worker need no second command.
const defaultName = "default"

// script is the latest release's install script for Linux and macOS; with
// .ps1, for Windows.
const script = "https://mainplane.ai/install"

// handover is a config and, from an install that made the config itself, the
// secret of the api key the install prints, and then what the elevated
// install found: the harness key and the default join token.
type handover struct {
	server.Config
	Key     string `json:"key,omitempty"`
	Harness string `json:"harness,omitempty"`
	Join    string `json:"join,omitempty"`
}

// config is what install's arguments name: the default config with a fresh
// key, or a config file.
func config(args []string) (h handover, fresh bool) {
	switch {
	case len(args) == 0:
		return handover{Config: server.Default(), Key: rand.Text()}, true
	case len(args) == 1:
		b, err := os.ReadFile(args[0])
		fatal(err)
		fatal(json.Unmarshal(b, &h))
		return h, false
	}
	usage()
	return h, false
}

// install hands the expanded config to an elevated install through a file
// only this user may read, since neither sudo nor UAC carries this shell's
// environment over, and reads the file back for the harness key it writes there. The
// file is in a directory of its own: Linux refuses root a write to another
// user's file in /tmp.
func install(h handover) (handover, int) {
	d, err := os.MkdirTemp("", "mainplane-server-*")
	fatal(err)
	defer func() { _ = os.RemoveAll(d) }()
	f := filepath.Join(d, "handover.json")
	b, err := json.Marshal(h)
	fatal(err)
	fatal(os.WriteFile(f, b, 0o600))
	if code := elevate.Run("install", f); code != 0 {
		return h, code
	}
	h, _ = config([]string{f})
	return h, 0
}

// network waits, behind the spinner, until workers can find the harness
// through its tunnel, after the lines are printed: they name no URL, and the
// wait is the first start's. Then, with login, it logs in the CLI the admin
// install placed with the default api key, which needs the tunnel too.
func network(h handover, login bool) {
	fmt.Println()
	stop := spin("initializing network")
	_, err := server.Reached(h.Harness)
	if err == nil && login {
		if out, e := exec.Command(worker.Bin, "login", auth.Token(auth.Key, h.Harness, h.Key)).CombinedOutput(); e != nil {
			err = fmt.Errorf("mainplane login: %w: %s", e, out)
		}
	}
	if err != nil {
		stop("")
		fatal(err)
	}
	stop(" done")
}

// done says the harness is installed, and how to make a machine a worker of
// it or log its CLI in.
func done(h handover) {
	fmt.Printf("%s\n%s\n%s", version.Installed(), installLines(auth.Join, h.Join), installLines(auth.Key, auth.Token(auth.Key, h.Harness, h.Key)))
}

// installLines are what token does and the lines that run the latest install
// script with it, on Linux and macOS, then on Windows: a join token makes the
// machine a worker, an api key logs its CLI in.
func installLines(kind, token string) string {
	label := "log in:"
	if kind == auth.Join {
		label = "connect a worker:"
	}
	return fmt.Sprintf("%s\n  linux, macos:  curl -fsSL %s | sh -s -- %s\n  windows:       & ([scriptblock]::Create((irm %s.ps1))) %s\n", label, script, token, script, token)
}

// spinner is design/ascii/spinner.json: Braille frames and the milliseconds
// each shows, uneven so the dot swishes round.
var spinner = []struct {
	frame rune
	ms    int
}{{'⣀', 103}, {'⡄', 129}, {'⠆', 148}, {'⠃', 129}, {'⠋', 58}, {'⠙', 49}, {'⠸', 62}, {'⢠', 122}}

// spin shows label behind the spinner on a terminal until stop, which ends
// the line with end, or clears it when end is empty; elsewhere, as over ssh,
// the label at once and end after it.
func spin(label string) (stop func(end string)) {
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Print(label)
		return func(end string) { fmt.Println(end) }
	}
	quit, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; ; i++ {
			s := spinner[i%len(spinner)]
			fmt.Printf("\r%c %s", s.frame, label)
			select {
			case <-quit:
				fmt.Printf("\r%s\r", strings.Repeat(" ", len(label)+2))
				return
			case <-time.After(time.Duration(s.ms) * time.Millisecond):
			}
		}
	}()
	return func(end string) {
		close(quit)
		<-finished
		if end != "" {
			fmt.Println(label + end)
		}
	}
}

// move moves the installed harness to the user's own tunnel, or back to a
// quick one.
func move(args []string) {
	var t *server.Tunnel
	switch {
	case len(args) == 2 && args[1] == "quick":
	case len(args) == 3:
		u, err := neturl.Parse(args[1])
		if err != nil || u.Scheme != "https" || u.Host == "" {
			log.Fatalf("%s: the URL is https://<the name Cloudflare routes to the tunnel>", args[1])
		}
		t = &server.Tunnel{URL: strings.TrimSuffix(args[1], "/"), Token: args[2]}
	default:
		usage()
	}
	elevate.Root(args...)
	url, err := server.SetTunnel(t)
	fatal(err)
	fmt.Printf("harness reached at %s; workers follow in about a minute\n", url)
}

func table(kind string, args []string) {
	store := server.Auth(server.Dir)
	switch args[0] {
	case "new":
		if kind == auth.Join && args[1] == worker.Admin {
			log.Fatalf("%s is the name of the worker on this machine, which install joins", worker.Admin)
		}
		k, err := server.Key(server.Dir)
		fatal(err)
		secret, err := store.Issue(kind, args[1], len(args) == 3)
		fatal(err)
		token := auth.Token(kind, pointer.Encode(k), secret)
		fmt.Println(token)
		fmt.Fprintf(os.Stderr, "\n%s", installLines(kind, token)) // stderr, so stdout stays the token for scripts
	case "revoke":
		fatal(store.Revoke(kind, args[1]))
	case "list":
		t, err := store.Load()
		fatal(err)
		for _, e := range t[kind] {
			fmt.Println(e.Name + map[bool]string{true: " ephemeral"}[e.Ephemeral])
		}
	}
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mainplane-server <verb> ...

  up        <config.json>                 run the harness, with its keys, auth table and sessions beside the config
  install                                 run it at every boot, with the provider keys set in this shell, if any, behind a
                                          quick tunnel: a temporary URL, no domain needed. Makes this machine the worker
                                          admin and logs its CLI in. A config already installed stays as it is.
                                          Providers and links in the config apply when it is saved
  install   <config.json>                 run it at every boot from a root-only copy of the config
                                          Either install prints a new join token named default, for any number of
                                          machines; the default token before it joins no more. It also prints a new
                                          api key named default; the default keys before it still work until
                                          key revoke default
  tunnel    <url> <cloudflared token>     reach the installed harness at url, through a tunnel you made in Cloudflare
                                          that routes url to http://localhost:8080; workers follow
  tunnel    quick                         back to a quick tunnel
  key       new <name> | revoke <name> | list   api keys of the installed harness: what a connector needs to call it
  join      new <name> [ephemeral] | revoke <name> | list
                                          join secrets of the installed harness: what a machine needs to become a worker.
                                          The name is the secret's, for revoke; a worker is named by its hostname.
                                          An ephemeral one's workers leave the mesh 3 minutes after they go quiet.
                                          Revoking refuses new joins; its workers stay until
                                          mainplane worker remove <worker>
  update    [version]                     the installed harness becomes that release, the latest stable by default;
                                          prints the changelog between, restarts it, and workers follow
  uninstall                               remove the service, the binary and the admin worker; the config, sessions and
                                          auth table stay
  version                                 the release this binary was built from

key, join, tunnel, update and uninstall ask for sudo or admin themselves.
`)
	os.Exit(2)
}
