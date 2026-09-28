package worker

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to five seconds: a harness is restarted often while it is developed, and a
// worker that takes a minute to come back reads as a failure. A dial a second
// against a dead harness costs nothing. A handshake gets longer than the wait:
// a first connect over a relayed path can take seconds.
// A ping every 30s keeps the connection from reading as idle to the tunnel,
// which closes one silent for 100s. A pong that takes as long is a dead
// harness.
const (
	redialMin   = 200 * time.Millisecond
	redialMax   = 5 * time.Second
	dialTimeout = 30 * time.Second
	ping        = 30 * time.Second
)

// Default is the interpreter each OS ships with. The first is the default.
var Default = map[string][]string{"windows": {"pwsh"}, "linux": {"bash"}, "darwin": {"bash"}}

// Dial makes this machine a worker of the harness at url: open a WebSocket
// at url/worker, serve until it ends, open again. It never returns. A
// connection that ends within the longest wait, as a refused hello does,
// keeps the backoff; only one that held resets it.
func Dial(url string, l Local) {
	wait := redialMin
	for {
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		c, _, err := websocket.Dial(ctx, url+"/worker", nil)
		cancel()
		if err == nil {
			c.SetReadLimit(-1) // a frame is a whole file read or write; its size is the worker protocol's business
			conn := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
			log.Printf("connected to %s as %s", url, l.Name)
			start := time.Now()
			ctx, stop := context.WithCancel(context.Background())
			go keepalive(ctx, c)
			err = Serve(conn, l)
			stop()
			_ = conn.Close()
			if time.Since(start) > redialMax {
				wait = redialMin
			}
		}
		log.Printf("%v, redial in %s", err, wait)
		time.Sleep(wait)
		wait = min(2*wait, redialMax)
	}
}

// keepalive pings c until ctx ends, and closes it when a pong does not come.
func keepalive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, ping)
			err := c.Ping(pctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				_ = c.Close(websocket.StatusGoingAway, "no pong")
				return
			}
		}
	}
}
