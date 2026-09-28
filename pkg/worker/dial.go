package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/coder/websocket"

	"github.com/mainplane-ai/mainplane/pkg/pointer"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to five seconds: a harness is restarted often while it is developed, and a
// worker that takes a minute to come back reads as a failure. A dial a second
// against a dead harness costs nothing. A handshake gets longer than the wait:
// a first connect over a relayed path can take seconds.
// A ping every 30s keeps the connection from reading as idle to the tunnel,
// which closes one silent for 100s. A pong that takes as long is a dead
// harness. A worker asks the pointer at most once a minute: a harness that is
// down would otherwise draw a lookup from every worker at every redial.
const (
	redialMin   = 200 * time.Millisecond
	redialMax   = 5 * time.Second
	dialTimeout = 30 * time.Second
	ping        = 30 * time.Second
	lookupWait  = time.Minute
)

// Default is the interpreter each OS ships with. The first is the default.
var Default = map[string][]string{"windows": {"pwsh"}, "linux": {"bash"}, "darwin": {"bash"}}

// Dial makes this machine a worker of the harness with key: prove the harness
// at the last URL it had, or find it through the pointer, open a WebSocket at
// url/worker, serve until it ends, open again. It never returns. A
// connection that ends within the longest wait, as a refused hello does,
// keeps the backoff; only one that held resets it.
func Dial(key string, l Local) {
	wait, url, asked := redialMin, "", time.Time{}
	for {
		err := errors.New("no harness URL: the pointer is asked again within a minute")
		if url != "" {
			err = pointer.Prove(context.Background(), key, url)
		}
		if err != nil && time.Since(asked) > lookupWait {
			asked = time.Now()
			var u string
			if u, err = pointer.Find(context.Background(), key, ""); err == nil {
				url = u
			}
		}
		var c *websocket.Conn
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			c, _, err = websocket.Dial(ctx, url+"/worker", nil)
			cancel()
		}
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
