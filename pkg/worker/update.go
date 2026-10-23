package worker

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/release"
)

// A failed version is fetched again after a wait that starts at retryMin and
// doubles up to retryMax. The harness asks on every redial, about every five
// seconds: a short outage costs half a minute, and a broken release costs one
// binary download per ten minutes, not one per redial.
const (
	retryMin = 30 * time.Second
	retryMax = 10 * time.Minute
)

// failed is the last update that failed; the worker outlives connections.
var failed struct {
	sync.Mutex
	v    string
	err  error
	at   time.Time
	wait time.Duration
}

// update makes the running binary release v's and returns the path the
// service runs. While v waits for its retry it answers with the last failure.
func update(v string) (string, error) {
	failed.Lock()
	defer failed.Unlock()
	if failed.v == v && time.Since(failed.at) < failed.wait {
		return "", fmt.Errorf("%w (retry in %s)", failed.err, time.Until(failed.at.Add(failed.wait)).Round(time.Second))
	}
	exe, err := os.Executable()
	if err == nil {
		err = release.Install(exe, "mainplane", v)
	}
	if err != nil {
		wait := retryMin
		if failed.v == v {
			wait = min(2*failed.wait, retryMax)
		}
		failed.v, failed.err, failed.at, failed.wait = v, err, time.Now(), wait
	}
	return exe, err
}
