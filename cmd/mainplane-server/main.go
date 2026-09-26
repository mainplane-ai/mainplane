// mainplane-server is the server binary: the harness, and the credentials
// that reach it. A new key or join prints its token once; the table holds
// only hashes.
//
//	mainplane-server up   <config.json>
//	mainplane-server install [--host <name>] | <config.json>    and again at every boot, as a service; asks for sudo or admin itself
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
			addr, err := h.Address(auth.Join)
			fatal(err)
			fatal(server.Install(h.Config))
			if h.Key != "" {
				fatal(h.Auth().Set(auth.Key, hostname(), h.Key))
			}
			fmt.Printf("harness runs at every boot from %s; workers dial %s\n", server.Conf, addr)
		} else if code := install(h); code != 0 {
			os.Exit(code)
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
// secret of the api key this machine's CLI logs in with.
type handover struct {
	server.Config
	Key string `json:"key,omitempty"`
}

// config is what install's arguments name: the default config with a fresh
// key, at this machine's name or --host, or a config file.
func config(args []string) (h handover, fresh bool) {
	switch {
	case len(args) == 0 || len(args) == 2 && args[0] == "--host":
		host := hostname()
		if len(args) == 2 {
			host = args[1]
		}
		c, err := server.Default(host)
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
// environment over.
func install(h handover) int {
	f, err := os.CreateTemp("", "mainplane-server-*.json")
	fatal(err)
	defer func() { _ = os.Remove(f.Name()) }()
	fatal(json.NewEncoder(f).Encode(h))
	fatal(f.Close())
	return elevate.Run("install", f.Name())
}

// login logs this machine's CLI in to the harness just installed and says
// what comes next.
func login(h handover) {
	addr, err := h.Address(auth.Key)
	fatal(err)
	token := auth.Token(auth.Key, addr, h.Key)
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
		addr, err := c.Address(kind)
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
  install   [--host <name>]               run it at every boot, with the provider keys set in this shell; workers
                                          dial <name>, this machine's by default. Logs this machine's CLI in
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
