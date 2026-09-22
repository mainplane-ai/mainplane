package worker

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/release"
)

// A failed version is not fetched again for ten minutes: the harness asks on
// every redial, and a broken release would otherwise cost a binary download
// every five seconds, while a fixed one still arrives within minutes.
const cooldown = 10 * time.Minute

// failed is the last update that failed; the worker outlives connections.
var failed struct {
	sync.Mutex
	v   string
	err error
	at  time.Time
}

// update makes the running binary release v's and returns the path the
// service runs. While v is cooling down it answers with the last failure.
func update(v string) (string, error) {
	failed.Lock()
	defer failed.Unlock()
	if failed.v == v && time.Since(failed.at) < cooldown {
		return "", fmt.Errorf("%w (retry in %s)", failed.err, time.Until(failed.at.Add(cooldown)).Round(time.Second))
	}
	exe, err := os.Executable()
	if err == nil {
		err = release.Install(exe, "mainplane", v)
	}
	if err != nil {
		failed.v, failed.err, failed.at = v, err, time.Now()
	}
	return exe, err
}
