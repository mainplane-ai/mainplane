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

	mu      sync.Mutex
	enabled bool
	running bool
	failed  bool
	port    int
	secret  string
	cancel  context.CancelFunc
	done    chan struct{}
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

func (d *daemon) stop() {
	d.mu.Lock()
	cancel := d.cancel
	done := d.done
	d.cancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (d *daemon) disable() {
	d.mu.Lock()
	d.enabled = false
	d.mu.Unlock()
	d.stop()
}

func (d *daemon) enable() {
	d.mu.Lock()
	d.enabled = true
	d.failed = false
	d.mu.Unlock()
}

func (d *daemon) ensure(parent context.Context, m Manifest, env []string) error {
	d.mu.Lock()
	if !d.enabled {
		d.mu.Unlock()
		return fmt.Errorf("app %s is stopped", d.name)
	}
	if d.cancel != nil {
		port := d.port
		d.mu.Unlock()
		return waitPort(parent, port)
	}
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		d.mu.Unlock()
		return err
	}
	port, err := freePort()
	if err != nil {
		d.mu.Unlock()
		return err
	}
	secret := rand.Text()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	d.port, d.secret, d.cancel, d.done = port, secret, cancel, done
	d.mu.Unlock()

	go d.supervise(ctx, done, m.Run, port, secret, env)
	return waitPort(parent, port)
}

func (d *daemon) supervise(ctx context.Context, done chan struct{}, argv []string, port int, secret string, extraEnv []string) {
	defer close(done)
	backoff := restartMin
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := d.runOnce(ctx, argv, port, secret, extraEnv)
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

func (d *daemon) runOnce(ctx context.Context, argv []string, port int, secret string, extraEnv []string) error {
	if len(argv) == 0 {
		return errors.New("empty run command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = d.dir
	setProcGroup(cmd)
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
	if err := cmd.Start(); err != nil {
		return err
	}
	d.mu.Lock()
	d.running, d.failed = true, false
	d.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { killProcGroup(cmd.Process) })
	err := cmd.Wait()
	stop()
	killProcGroup(cmd.Process)
	d.mu.Lock()
	d.running = false
	d.mu.Unlock()
	return err
}

func waitPort(ctx context.Context, port int) error {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(readyWait)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("daemon on %s did not listen within %s", addr, readyWait)
}

func (d *daemon) proxy(w http.ResponseWriter, r *http.Request, targetPath string, hdr http.Header) {
	d.mu.Lock()
	port, secret := d.port, d.secret
	d.mu.Unlock()
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
