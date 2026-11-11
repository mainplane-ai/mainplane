package apps

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	// readyWait bounds waiting for a daemon's loopback port on start or first request.
	readyWait = 3 * time.Second
	// restartMin and restartMax bound supervisor backoff when a daemon crashes.
	restartMin = 200 * time.Millisecond
	restartMax = 2 * time.Second
)

var safeEnvKeys = []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "SystemRoot", "USER", "LANG"}

type daemon struct {
	name    string
	dir     string
	dataDir string

	lifeMu sync.Mutex // serializes ensure, stop, enable, disable

	mu      sync.Mutex
	enabled bool
	running bool
	failed  bool
	port    int
	secret  string
	env     []string
	cancel  context.CancelFunc
	done    chan struct{}
	ready   chan struct{}
}

func newDaemon(name, dir, dataDir string) *daemon {
	return &daemon{name: name, dir: dir, dataDir: dataDir, enabled: true}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func (d *daemon) status(missing []string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return "stopped"
	}
	if len(missing) > 0 {
		return "missing_secrets"
	}
	if d.running {
		return "running"
	}
	if d.failed {
		return "failed"
	}
	return "stopped"
}

func (d *daemon) stopLocked() {
	d.mu.Lock()
	cancel := d.cancel
	done := d.done
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	d.mu.Lock()
	d.cancel, d.done, d.ready = nil, nil, nil
	d.running, d.port, d.secret, d.env = false, 0, "", nil
	d.mu.Unlock()
}

func (d *daemon) stop() {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	d.stopLocked()
}

func (d *daemon) disable() {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	d.mu.Lock()
	d.enabled = false
	d.mu.Unlock()
	d.stopLocked()
}

func (d *daemon) enable() {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	d.mu.Lock()
	d.enabled = true
	d.failed = false
	d.mu.Unlock()
}

func (d *daemon) ensure(supCtx, waitCtx context.Context, m Manifest, env []string) error {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()

	d.mu.Lock()
	if !d.enabled {
		d.mu.Unlock()
		return fmt.Errorf("app %s is stopped", d.name)
	}
	if d.cancel != nil && !slices.Equal(d.env, env) {
		d.mu.Unlock()
		d.stopLocked()
		d.mu.Lock()
	}
	if d.cancel == nil {
		if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
			d.mu.Unlock()
			return err
		}
		ctx, cancel := context.WithCancel(supCtx)
		done := make(chan struct{})
		ready := make(chan struct{})
		d.cancel, d.done, d.ready, d.env = cancel, done, ready, slices.Clone(env)
		go d.supervise(ctx, done, m.Run, slices.Clone(env))
	}
	ready := d.ready
	done := d.done
	d.mu.Unlock()

	timer := time.NewTimer(readyWait)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-done:
		return fmt.Errorf("app %s exited before listening", d.name)
	case <-waitCtx.Done():
		return waitCtx.Err()
	case <-timer.C:
		return fmt.Errorf("app %s did not listen within %s", d.name, readyWait)
	}
}

func (d *daemon) supervise(ctx context.Context, done chan struct{}, argv, extraEnv []string) {
	defer close(done)
	backoff := restartMin
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := d.runOnce(ctx, argv, extraEnv)
		if ctx.Err() != nil {
			return
		}
		log.Printf("app %s: exited: %v", d.name, err)
		d.mu.Lock()
		d.failed = true
		d.mu.Unlock()
		if time.Since(start) > 5*time.Second {
			backoff = restartMin
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, restartMax)
	}
}

func (d *daemon) runOnce(ctx context.Context, argv, extraEnv []string) error {
	if len(argv) == 0 {
		return errors.New("empty run command")
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	secret := rand.Text()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = d.dir
	var env []string
	for _, k := range safeEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"PORT="+strconv.Itoa(port),
		"DATA_DIR="+d.dataDir,
		"MAINPLANE_PROXY_SECRET="+secret,
	)
	cmd.Env = append(env, extraEnv...)

	pg, err := startProcGroup(cmd)
	if err != nil {
		return err
	}
	var waitErr error
	waitDone := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(waitDone)
	}()
	stop := context.AfterFunc(ctx, pg.kill)
	defer func() {
		stop()
		pg.kill()
	}()

	if err := waitOwnedPort(ctx, port, pg, waitDone, &waitErr); err != nil {
		pg.kill()
		<-waitDone
		return err
	}

	d.mu.Lock()
	d.port, d.secret = port, secret
	d.running, d.failed = true, false
	select {
	case <-d.ready:
	default:
		if d.ready != nil {
			close(d.ready)
		}
	}
	d.mu.Unlock()

	<-waitDone
	d.mu.Lock()
	d.running = false
	d.port, d.secret = 0, ""
	if d.cancel != nil {
		d.ready = make(chan struct{})
	}
	d.mu.Unlock()
	return waitErr
}

func waitOwnedPort(ctx context.Context, port int, pg *procGroup, waitDone <-chan struct{}, waitErr *error) error {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		select {
		case <-waitDone:
			if *waitErr != nil {
				return *waitErr
			}
			return errors.New("daemon exited before listening")
		default:
		}
		if pg.ownsTCPPort(port) {
			if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
				_ = c.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitDone:
			if *waitErr != nil {
				return *waitErr
			}
			return errors.New("daemon exited before listening")
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("daemon on %s did not listen within %s", addr, readyWait)
}

func (d *daemon) proxy(w http.ResponseWriter, r *http.Request, targetPath string, hdr http.Header) {
	d.mu.Lock()
	port, secret := d.port, d.secret
	d.mu.Unlock()
	if port == 0 {
		http.Error(w, fmt.Sprintf("app %s is not ready", d.name), http.StatusServiceUnavailable)
		return
	}
	u := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = u.Scheme
			pr.Out.URL.Host = u.Host
			pr.Out.URL.Path = targetPath
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = u.Host
			pr.Out.Header = hdr.Clone()
			pr.Out.Header.Set("X-Mainplane-Proxy-Secret", secret)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("app %s: %v", d.name, err), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}
