package worker

import (
	"log"
	"net"
	"time"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to a minute, so a harness down for a day is not hammered.
const (
	redialMin = 200 * time.Millisecond
	redialMax = time.Minute
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
