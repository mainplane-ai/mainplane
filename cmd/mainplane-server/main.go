// mainplane-server is the server binary: one subcommand per role.
//
//	mainplane-server harness <config.json>
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mainplane-ai/mainplane/pkg/server"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "harness" {
		fmt.Fprintln(os.Stderr, "usage: mainplane-server harness <config.json>")
		os.Exit(2)
	}
	c, err := server.Load(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Harness(ctx, c); err != nil {
		log.Fatal(err)
	}
}
