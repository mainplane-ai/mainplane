package worker

import (
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"os/user"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Sessions is the drive of the harness's state files, read-only everywhere,
// and HarnessDir the directory of a harness on Linux, the only harness whose
// admin can serve it. Every path of a drive is a constant of the worker's
// OS, so the harness names drives and never a path that root acts on.
const (
	Sessions   = "sessions"
	HarnessDir = "/var/lib/mainplane-server"
)

// A drive that waits or failed is tried again this often; a mounted one has
// its server probed as often, so status says when it waits.
const reconcileEvery = 10 * time.Second

// A drive's name is a path element on every OS and, for Windows, a share
// name: the letters of a worker's name.
var driveName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func ValidDrive(name string) bool { return driveName.MatchString(name) }

// Desired is what the harness wants of a worker's drives, whole: the drives
// it serves and to which clients, and the drives it mounts. A worker that
// serves a drive and mounts it has it from its own disk.
type Desired struct {
	Serve []Export `json:"serve"`
	Mount []Mount  `json:"mount"`
}

type Export struct {
	Name    string   `json:"name"`
	Clients []Client `json:"clients"`
}

type Client struct {
	Name string     `json:"name"`
	Addr netip.Addr `json:"addr"`
	SMB  *SMB       `json:"smb,omitempty"` // a Windows client's user on this server
}

type Mount struct {
	Name   string     `json:"name"`
	Server string     `json:"server"`
	Addr   netip.Addr `json:"addr"`
	SMB    *SMB       `json:"smb,omitempty"` // how a Windows worker signs in to Server
}

// SMB is a Windows worker's user on one server, the same for each of that
// server's drives, since Windows holds one credential per server. The
// harness derives the password from its key, so neither end stores it
// before the harness sends it. Host is the server's long name, for a mount:
// on Windows a short name that is also a Tailscale name resolves there.
type SMB struct {
	Host     string `json:"host,omitempty"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// Drive is one drive as the worker reports it.
type Drive struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Serve bool   `json:"serve,omitempty"` // served from Path, not mounted there
	State string `json:"state"`           // mounted, serving, waiting, error
	Error string `json:"error,omitempty"` // why it waits or failed, in the OS's words
}

const (
	Mounted = "mounted"
	Serving = "serving"
	Waiting = "waiting"
	Failed  = "error"
)

func (d Drive) String() string {
	s := d.State
	if d.Error != "" {
		s += ": " + d.Error
	}
	return fmt.Sprintf("%s %s %s", d.Name, d.Path, s)
}

// drives reconciles what the harness wants against what this machine
// serves and mounts, for the life of the process: connections come and go,
// mounts stay. Until the harness first says what it wants nothing is
// touched, so a worker that restarts or updates keeps its mounts.
type drives struct {
	op   *user.User // owns every file of the drives it serves
	kick chan struct{}
	run  sync.Mutex // one reconcile at a time

	mu      sync.Mutex
	want    *Desired
	dropped bool // a frame after drop would bring a drive back
	have    []Drive
	send    func(Frame) error // the connection to the harness, while there is one
}

func newDrives(op *user.User) (*drives, error) {
	if op == nil { // a worker run by hand is its own operator
		var err error
		if op, err = user.Current(); err != nil {
			return nil, err
		}
	}
	d := &drives{op: op, kick: make(chan struct{}, 1)}
	go d.loop()
	return d, nil
}

// set takes the harness's desired drives frame and reconciles at once.
func (d *drives) set(body []byte) error {
	var want Desired
	if err := json.Unmarshal(body, &want); err != nil {
		return err
	}
	d.mu.Lock()
	if !d.dropped {
		d.want = &want
	}
	d.mu.Unlock()
	select {
	case d.kick <- struct{}{}:
	default:
	}
	return nil
}

func (d *drives) loop() {
	for {
		select {
		case <-d.kick:
		case <-time.After(reconcileEvery):
		}
		d.run.Lock()
		d.mu.Lock()
		want := d.want
		d.mu.Unlock()
		if want != nil {
			d.report(reconcile(*want, d.op))
		}
		d.run.Unlock()
	}
}

// report keeps what reconcile found and, when it changed, logs it and tells
// the harness.
func (d *drives) report(have []Drive) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.Equal(have, d.have) {
		return
	}
	for _, r := range have {
		if !slices.Contains(d.have, r) {
			log.Printf("drive %v", r)
		}
	}
	d.have = have
	if d.send != nil {
		_ = d.send(Frame{Header: Header{Kind: Status, Drives: have}}) // a failed send is the connection gone; Serve sees it
	}
}

// attach sends hello with the drives as they are, and every change after it
// as a status frame on send, until detach.
func (d *drives) attach(hello Frame, send func(Frame) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	hello.Drives = d.have
	if err := send(hello); err != nil {
		return err
	}
	d.send = send
	return nil
}

func (d *drives) detach() {
	d.mu.Lock()
	d.send = nil
	d.mu.Unlock()
}

// drop ends every drive on this machine, for a worker that left the mesh or
// is uninstalled: nothing reconciles them again.
func (d *drives) drop() {
	d.run.Lock()
	defer d.run.Unlock()
	d.mu.Lock()
	d.want, d.dropped = nil, true
	d.mu.Unlock()
	if err := dropDrives(d.op); err != nil {
		log.Printf("drives: %v", err)
	}
}

// text is the drives for mainplane status.
func (d *drives) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var b strings.Builder
	for _, r := range d.have {
		fmt.Fprintf(&b, "drive %v\n", r)
	}
	return b.String()
}
