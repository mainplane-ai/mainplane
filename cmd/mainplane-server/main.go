// mainplane-server is the server binary: the harness, and the credentials
// that reach it. A new key prints its token once; the table holds only
// hashes. A machine joins with the network name and the device code.
//
//	mainplane-server up   <config.json>
//	mainplane-server install [config.json]    and again at every boot, as a service, behind a quick tunnel; asks for sudo or admin itself;
//	                                          with --url <url> last, at the harness URL url that the user's own proxy carries;
//	                                          with none, keeps an installed config; prints the join lines, with the network name and
//	                                          device code made at the first install, and a new api key "default", the ones before it kept
//	mainplane-server install quickstart       the same with none, then opens the TUI's quickstart on this terminal
//	mainplane-server tunnel <url> <cloudflared token> | quick   the installed harness moves to the user's own tunnel, or back
//	mainplane-server url <url>                the installed harness moves to url, carried by the user's own proxy
//	mainplane-server key  new <name> | revoke <name> | list    on the installed harness, as root
//	mainplane-server join [cycle]             the join lines, with a new device code after cycle
//	mainplane-server rename <name>            the network name
//	mainplane-server update [version]         the installed harness becomes release version, the latest stable by default
//	mainplane-server uninstall                the service, binary and admin worker go; sessions and the auth table stay
//	mainplane-server version
package main

import (
	"cmp"
	"context"
	"crypto/ed25519"
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
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/server"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
	"golang.org/x/term"
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
		h, fresh, quick := config(args[1:])
		fatal(h.Expand())
		if !elevate.Is() {
			var code int
			if h, code = install(h); code != 0 {
				os.Exit(code)
			}
			if fresh { // the elevated install handed back; from a config file it printed itself
				done(h)
				logIn(h)
				finish(h, quick)
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
		n, err := server.LoadNetwork(server.Dir)
		fatal(err)
		h.Join = n.String()
		if h.NoPointer { // done pins it once the harness has a URL
			h.Join = auth.Token(auth.Join, h.Harness, auth.Secret(k, n.Code))
		}
		if keep && !fresh { // an unelevated install handed over; hand back what it prints
			b, err := json.Marshal(h)
			fatal(err)
			fatal(os.WriteFile(args[1], b, 0o600))
			return
		}
		done(h)
		if fresh {
			logIn(h)
		}
		finish(h, quick) // quick only with no config file, so fresh
	case verb == "tunnel" || verb == "url":
		move(args)
	case verb == auth.Key && (len(args) == 2 && args[1] == "list" || len(args) == 3 && (args[1] == "new" || args[1] == "revoke")):
		elevate.Root(args...)
		if _, err := server.Load(server.Conf); err != nil {
			log.Fatalf("no installed harness: %v", err)
		}
		table(args[1:])
	case verb == auth.Join || verb == "rename":
		join(args)
	default:
		usage()
	}
}

// defaultName names the api key every install makes anew, so the first
// connector needs no second command.
const defaultName = "default"

// handover is a config and, from an install that made the config itself, the
// secret of the api key the install prints, and then what the elevated
// install found: the harness key, and the network name and device code.
type handover struct {
	server.Config
	Key     string `json:"key,omitempty"`
	Harness string `json:"harness,omitempty"`
	Join    string `json:"join,omitempty"`
}

// config is what install's arguments name: the default config with a fresh
// key, with no pointer after --no-pointer, no admin worker after --no-admin,
// reached at a URL after --url, or a config file; and whether the quickstart
// opens after. The quickstart is the admin worker's CLI.
func config(args []string) (h handover, fresh, quick bool) {
	if quick = len(args) > 0 && args[0] == "quickstart"; quick {
		args = args[1:]
	}
	var noPointer, noAdmin bool
	for ; len(args) > 0 && (args[0] == "--no-pointer" || args[0] == "--no-admin"); args = args[1:] {
		noPointer, noAdmin = noPointer || args[0] == "--no-pointer", noAdmin || args[0] == "--no-admin"
	}
	if noAdmin && quick {
		log.Fatal("--no-admin has no quickstart: the quickstart runs on the admin worker's CLI")
	}
	switch {
	case len(args) == 0:
		h = handover{Config: server.Default(), Key: rand.Text()}
		h.NoPointer, h.NoAdmin = noPointer, noAdmin
		return h, true, quick
	case len(args) == 2 && args[0] == "--url":
		h, fresh, _ = config(nil)
		h.URL = harnessURL(args[1], "the URL your proxy carries to http://localhost:8080")
		h.NoPointer, h.NoAdmin = noPointer, noAdmin
		return h, fresh, quick
	case len(args) == 1 && !quick && !noPointer && !noAdmin:
		b, err := os.ReadFile(args[0])
		fatal(err)
		fatal(json.Unmarshal(b, &h))
		return h, false, false
	}
	usage()
	return h, false, false
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
	h, _, _ = config([]string{f})
	return h, 0
}

// logIn logs in the CLI the admin install placed with the default api key.
// It finds the harness on loopback, so it needs no tunnel. With no admin
// worker there is no CLI.
func logIn(h handover) {
	if h.NoAdmin {
		return
	}
	if out, err := exec.Command(worker.Bin, "login", auth.Token(auth.Key, h.Harness, h.Key)).CombinedOutput(); err != nil {
		log.Fatalf("mainplane login: %v: %s", err, out)
	}
}

// finish waits for the network. The quickstart on a terminal opens the TUI
// there instead, as this user, and exits as that does: the harness starts its
// tunnel meanwhile, and the TUI says while it does.
func finish(h handover, quick bool) {
	if quick && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if h.URL != "" && !h.NoPointer { // the TUI would only say the network is starting; done waited with no pointer
			network(h)
		}
		tui := exec.Command(worker.Bin, "quickstart")
		tui.Stdin, tui.Stdout, tui.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := tui.Run(); err != nil && tui.ProcessState == nil {
			log.Fatal(err)
		}
		os.Exit(tui.ProcessState.ExitCode())
	}
	if !h.NoPointer { // done waited
		network(h)
	}
	if quick {
		fmt.Println("no terminal: run mainplane to finish setup")
	}
}

// network waits, behind the spinner, until workers can reach the harness,
// and returns its URL. It comes after the lines are printed, since they name
// no URL and the wait is the first start's, but with no pointer before them.
func network(h handover) string {
	fmt.Println()
	stop := spin("initializing network")
	u, err := server.Reached(h.Harness, h.URL, h.NoPointer)
	if err != nil {
		stop("")
		fatal(err)
	}
	stop(" done")
	return u
}

// done says the harness is installed, and how to make a machine a worker of
// it or log its CLI in. With no pointer the lines carry the harness URL, so
// it waits for one first.
func done(h handover) {
	join, key := h.Join, auth.Token(auth.Key, h.Harness, h.Key)
	fmt.Print(version.Installed())
	if h.NoPointer {
		u := network(h)
		join, key = auth.Pin(join, u), auth.Pin(key, u)
		fmt.Printf("\nno pointer: these lines carry %s. When the harness URL changes, every worker and login must join again with new lines: mainplane-server join, mainplane-server key new\n", u)
	}
	fmt.Printf("\n%s\n%s", installLines(auth.Join, join), installLines(auth.Key, key))
}

// self is the URL the installed harness is reached at, for lines that carry
// it when it has no pointer.
func self(k ed25519.PrivateKey) string {
	u, err := pointer.Self(context.Background(), pointer.Encode(k))
	fatal(err)
	if u == "" {
		log.Fatal("the harness has no URL yet: its tunnel is starting; try again in a minute")
	}
	return u
}

// installLines are what token does and the lines that run the latest install
// script with it, on Linux and macOS, then on Windows: the network name and
// device code make the machine a worker, an api key logs its CLI in.
func installLines(kind, token string) string {
	label := "log in:"
	if kind == auth.Join {
		label = "connect a device:"
	}
	unix, windows := release.InstallLines(token)
	return fmt.Sprintf("%s\n  linux, macos:  %s\n  windows:       %s\n", label, unix, windows)
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

// move moves the installed harness to the user's own proxy or tunnel, or
// back to a quick tunnel.
func move(args []string) {
	var url string
	var t *server.Tunnel
	switch {
	case len(args) == 2 && args[0] == "tunnel" && args[1] == "quick":
	case len(args) == 2 && args[0] == "url":
		url = harnessURL(args[1], "the URL your proxy carries to http://localhost:8080")
	case len(args) == 3 && args[0] == "tunnel":
		if strings.TrimSpace(args[2]) == "" {
			log.Fatalf("the cloudflared token is empty: copy it from the tunnel's page in Cloudflare")
		}
		t = &server.Tunnel{URL: harnessURL(args[1], "the name Cloudflare routes to the tunnel"), Token: args[2]}
	default:
		usage()
	}
	elevate.Root(args...)
	u, err := server.Move(url, t)
	fatal(err)
	if c, err := server.Load(server.Conf); err == nil && c.NoPointer {
		fmt.Printf("harness reached at %s; with no pointer, workers and logins join again with new lines: mainplane-server join, mainplane-server key new\n", u)
		return
	}
	fmt.Printf("harness reached at %s; workers follow in about a minute\n", u)
}

// harnessURL is s, https://<host> with nothing more; what names the host.
// Workers reach the coordinator on port 443 only.
func harnessURL(s, what string) string {
	s = strings.TrimSuffix(s, "/")
	if u, err := neturl.Parse(s); err != nil || u.Hostname() == "" || s != "https://"+u.Hostname() {
		log.Fatalf("%s: the URL is https://<%s>, with no port or path", s, what)
	}
	return s
}

// join prints the lines that make a machine a worker, after a new device
// code or network name.
func join(args []string) {
	switch {
	case len(args) == 1 && args[0] == auth.Join, len(args) == 2 && (args[0] == auth.Join && args[1] == "cycle" || args[0] == "rename"):
	default:
		usage()
	}
	elevate.Root(args...)
	n, err := server.LoadNetwork(server.Dir)
	if err != nil {
		log.Fatalf("no installed harness: %v", err)
	}
	switch {
	case args[0] == "rename":
		n, err = server.Rename(args[1])
	case len(args) == 2:
		n, err = server.Cycle()
	}
	fatal(err)
	c, err := server.Load(server.Conf)
	fatal(err)
	k, err := server.Key(server.Dir)
	fatal(err)
	url := ""
	if c.NoPointer {
		url = self(k)
	}
	fmt.Print(installLines(auth.Join, n.Join(k, c.NoPointer, url)))
}

func table(args []string) {
	store := server.Auth(server.Dir)
	switch args[0] {
	case "new":
		k, err := server.Key(server.Dir)
		fatal(err)
		url := ""
		if c, err := server.Load(server.Conf); err == nil && c.NoPointer {
			url = self(k) // before the key is issued, so a harness with no URL yet leaves no name taken
		}
		secret, err := store.Issue(auth.Key, args[1])
		fatal(err)
		token := auth.Token(auth.Key, pointer.Encode(k), secret)
		if url != "" {
			token = auth.Pin(token, url)
		}
		fmt.Println(token)
		fmt.Fprintf(os.Stderr, "\n%s", installLines(auth.Key, token)) // stderr, so stdout stays the token for scripts
	case "revoke":
		fatal(store.Revoke(auth.Key, args[1]))
	case "list":
		t, err := store.Load()
		fatal(err)
		for _, e := range t[auth.Key] {
			fmt.Println(e.Name)
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
  install   quickstart                    the same, then open the quickstart in the TUI on this terminal
  install   [quickstart] --url <url>      either, reached at url, which your own proxy carries to
                                          http://localhost:8080: no tunnel
  install   <config.json>                 run it at every boot from a root-only copy of the config
                                          Either install prints the line that makes a machine a worker, with the
                                          network name and device code the first install made, and a new api key
                                          named default; the default keys before it still work until
                                          key revoke default
  tunnel    <url> <cloudflared token>     reach the installed harness at url, through a tunnel you made in Cloudflare
                                          that routes url to http://localhost:8080; workers follow
  tunnel    quick                         back to a quick tunnel
  url       <url>                         reach the installed harness at url, which your own proxy carries to
                                          http://localhost:8080; workers follow
  key       new <name> | revoke <name> | list   api keys of the installed harness: what a connector needs to call it
  join                                    the line that makes a machine a worker: the network name and device code
  join      cycle                         a new device code; the one before joins no more. Workers stay until
                                          mainplane worker remove <worker>
  rename    <name>                        a new network name, a-z, 0-9 and single -, 3 to 40 characters; workers stay
  update    [version]                     the installed harness becomes that release, the latest stable by default;
                                          prints the changelog between, restarts it, and workers follow
  uninstall                               remove the service, the binary and the admin worker; the config, sessions and
                                          auth table stay
  version                                 the release this binary was built from

key, join, rename, tunnel, url, update and uninstall ask for sudo or admin themselves.
`)
	os.Exit(2)
}
