// mainplane-server is the server binary: the harness, and the credentials
// that reach it. A new key or join prints its token once; the table holds
// only hashes.
//
//	mainplane-server up   <config.json>
//	mainplane-server install <config.json>    and again at every boot, as a service; asks for sudo or admin itself
//	mainplane-server key  <config.json> new <name> | revoke <name> | list
//	mainplane-server join <config.json> new <name> | revoke <name> | list
//	mainplane-server update [version]         the installed harness becomes release version, the latest stable by default
//	mainplane-server version
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/elevate"
	"github.com/mainplane-ai/mainplane/pkg/server"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version.V)
		return
	}
	if len(os.Args) >= 2 && len(os.Args) <= 3 && os.Args[1] == "update" {
		elevate.Root(os.Args[1:]...)
		fatal(server.Update(strings.Join(os.Args[2:], "")))
		return
	}
	if len(os.Args) < 3 {
		usage()
	}
	if os.Args[1] == "install" {
		elevate.Root(os.Args[1:]...)
	}
	c, err := server.Load(os.Args[2])
	fatal(err)
	switch verb, args := os.Args[1], os.Args[3:]; {
	case verb == "up" && len(args) == 0:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fatal(server.Harness(ctx, c))
	case verb == "install" && len(args) == 0:
		fatal(server.Install(c))
		fmt.Printf("harness runs at every boot from %s\n", server.Conf)
	case verb == auth.Key || verb == auth.Join:
		table(c, verb, args)
	default:
		usage()
	}
}

func table(c server.Config, kind string, args []string) {
	store := c.Auth()
	switch {
	case len(args) == 2 && args[0] == "new":
		addr, err := c.Address(kind)
		fatal(err)
		secret, err := store.Issue(kind, args[1])
		fatal(err)
		fmt.Println(auth.Token(kind, addr, secret))
	case len(args) == 2 && args[0] == "revoke":
		fatal(store.Revoke(kind, args[1]))
	case len(args) == 1 && args[0] == "list":
		t, err := store.Load()
		fatal(err)
		for _, e := range t[kind] {
			fmt.Println(e.Name)
		}
	default:
		usage()
	}
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mainplane-server <verb> <config.json> ...

  up                                  run the harness
  install                             run it at every boot from a root-only copy of the config
  key   new <name> | revoke <name> | list   api keys: what a connector needs to call the harness
  join  new <name> | revoke <name> | list   join secrets: what a machine needs to become a worker

  mainplane-server update [version]   the installed harness becomes that release, the latest stable by default;
                                      prints the changelog between, restarts it, and workers follow
  mainplane-server version            the release this binary was built from
`)
	os.Exit(2)
}
