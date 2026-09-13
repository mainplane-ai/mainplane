package worker

import (
	"log"
	"net"
	"time"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to five seconds: a harness is restarted often while it is developed, and a
// worker that takes a minute to come back reads as a failure. A dial a second
// against a dead harness costs nothing.
const (
	redialMin = 200 * time.Millisecond
	redialMax = 5 * time.Second
)

// Default is the interpreter each OS ships with. The first is the default.
var Default = map[string][]string{"windows": {"pwsh"}, "linux": {"bash"}, "darwin": {"bash"}}

// Dial makes this machine a worker of the harness at addr: connect, serve
// until the connection ends, connect again. It never returns.
func Dial(addr string, l Local) {
	wait := redialMin
	for {
		conn, err := net.DialTimeout("tcp", addr, redialMax)
		if err == nil {
			log.Printf("connected to %s as %s", addr, l.Name)
			wait = redialMin
			err = Serve(conn, l)
			_ = conn.Close()
		}
		log.Printf("%v, redial in %s", err, wait)
		time.Sleep(wait)
		wait = min(2*wait, redialMax)
	}
}
