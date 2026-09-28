// Package relay is the DERP relay on the harness's one port. Workers reach it
// as a WebSocket at /derp through the tunnel, which passes no other upgrade.
// DERP authenticates a client by its node key, so the routes sit outside the
// api key check.
package relay

import (
	"log"
	"net/http"

	"tailscale.com/derp/derpserver"
	"tailscale.com/types/key"
)

// New returns a relay with a new node key each start. Clients learn the key
// in each DERP handshake and the relay map carries none, so nothing pins it.
func New() *derpserver.Server {
	return derpserver.New(key.NewNode(), func(f string, a ...any) { log.Printf("relay: "+f, a...) })
}

// Handle serves s on mux at /derp, and the latency probes clients send
// without UDP at /derp/probe and /derp/latency-check.
func Handle(mux *http.ServeMux, s *derpserver.Server) {
	mux.Handle("/derp", derpserver.AddWebSocketSupport(s, derpserver.Handler(s)))
	mux.HandleFunc("/derp/probe", derpserver.ProbeHandler)
	mux.HandleFunc("/derp/latency-check", derpserver.ProbeHandler)
}
