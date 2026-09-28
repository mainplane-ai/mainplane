// mainplane-server is the server binary: the harness, and the credentials
// that reach it. A new key or join prints its token once; the table holds
// only hashes.
//
//	mainplane-server up   <config.json>
//	mainplane-server install [config.json]    and again at every boot, as a service, behind a quick tunnel; asks for sudo or admin itself
//	mainplane-server key  new <name> | revoke <name> | list    on the installed harness, as root
//	mainplane-server join new <name> | revoke <name> | list
//	mainplane-server update [version]         the installed harness becomes release version, the latest stable by default
//	mainplane-server uninstall                the service and binary go; sessions and the auth table stay
//	mainplane-server version
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/elevate"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/server"
	"github.com/mainplane-ai/mainplane/pkg/version"
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
		fmt.Printf("harness uninstalled; its config, sessions and auth table stay in %s\n", server.Dir)
	case verb == "up" && len(args) == 2:
		c, err := server.Load(args[1])
		fatal(err)
		fatal(server.Up(c))
	case verb == "install":
		h, fresh := config(args[1:])
		fatal(h.Expand())
		if elevate.Is() {
			url, err := server.Install(h.Config)
			fatal(err)
			if h.Key != "" {
				fatal(h.Auth().Set(auth.Key, hostname(), h.Key))
			}
			fmt.Printf("harness runs at every boot from %s at %s\nthat URL is a quick tunnel's: temporary, it changes when Cloudflare drops the tunnel\n", server.Conf, url)
			h.At = url
			if h.Key != "" && !fresh { // an unelevated install handed over; hand the URL back
				b, err := json.Marshal(h)
				fatal(err)
				fatal(os.WriteFile(args[1], b, 0o600))
			}
		} else {
			var code int
			if h, code = install(h); code != 0 {
				os.Exit(code)
			}
		}
		if fresh {
			login(h)
		}
	case (verb == auth.Key || verb == auth.Join) && (len(args) == 2 && args[1] == "list" || len(args) == 3 && (args[1] == "new" || args[1] == "revoke")):
		elevate.Root(args...)
		c, err := server.Load(server.Conf)
		if err != nil {
			log.Fatalf("no installed harness: %v", err)
		}
		table(c, verb, args[1:])
	default:
		usage()
	}
}

// handover is a config and, from an install that made the config itself, the
// secret of the api key this machine's CLI logs in with, and then the URL
// the elevated install reached the harness at.
type handover struct {
	server.Config
	Key string `json:"key,omitempty"`
	At  string `json:"at,omitempty"`
}

// config is what install's arguments name: the default config with a fresh
// key, or a config file.
func config(args []string) (h handover, fresh bool) {
	switch {
	case len(args) == 0:
		c, err := server.Default()
		fatal(err)
		return handover{Config: c, Key: rand.Text()}, true
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
// environment over, and reads the file back for the URL it writes there. The
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

// login logs this machine's CLI in to the harness just installed and says
// what comes next.
func login(h handover) {
	token := auth.Token(auth.Key, h.At, h.Key)
	if _, err := exec.LookPath("mainplane"); err != nil {
		fmt.Printf("no mainplane CLI on PATH; log one in with:  mainplane login %s\n", token)
	} else {
		fmt.Print("mainplane logged in to ")
		cmd := exec.Command("mainplane", "login", token)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fatal(cmd.Run())
	}
	fmt.Println("add a worker:  mainplane-server join new <name>")
}

func hostname() string {
	h, err := os.Hostname()
	fatal(err)
	return h
}

func table(c server.Config, kind string, args []string) {
	store := c.Auth()
	switch args[0] {
	case "new":
		addr, err := c.URL()
		fatal(err)
		secret, err := store.Issue(kind, args[1])
		fatal(err)
		token := auth.Token(kind, addr, secret)
		fmt.Println(token)
		if kind == auth.Join { // stderr, so stdout stays the token for scripts
			fmt.Fprintf(os.Stderr, "\nlinux, macos:  curl -fsSL %[1]s%[2]s/install.sh | sudo sh -s -- %[3]s\nwindows:       & ([scriptblock]::Create((irm %[1]s%[2]s/install.ps1))) %[3]s\n", release.DL, version.V, token)
		}
	case "revoke":
		fatal(store.Revoke(kind, args[1]))
	case "list":
		t, err := store.Load()
		fatal(err)
		for _, e := range t[kind] {
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

  up        <config.json>                 run the harness
  install                                 run it at every boot, with the provider keys set in this shell, behind a
                                          quick tunnel: a temporary URL, no domain needed. Logs this machine's CLI in
  install   <config.json>                 run it at every boot from a root-only copy of the config
  key       new <name> | revoke <name> | list   api keys of the installed harness: what a connector needs to call it
  join      new <name> | revoke <name> | list   join secrets of the installed harness: what a machine needs to become a worker
  update    [version]                     the installed harness becomes that release, the latest stable by default;
                                          prints the changelog between, restarts it, and workers follow
  uninstall                               remove the service and the binary; the config, sessions and auth table stay
  version                                 the release this binary was built from

key, join, update and uninstall ask for sudo or admin themselves.
`)
	os.Exit(2)
}
