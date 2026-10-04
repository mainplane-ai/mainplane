package coordinator

import (
	"fmt"
	"maps"
	"slices"

	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// everyone, in a drive's workers, is every persistent worker. An ephemeral
// one is named, so a throwaway machine never gets files by default.
const everyone = "*"

// Drive is one drive in the harness config: the Linux worker that serves it
// and the workers that mount it. Sessions takes no server: admin serves it.
type Drive struct {
	Server  string   `json:"server,omitempty"`
	Workers []string `json:"workers"`
}

// SetDrives replaces the drives and wakes every map poll to send the change.
// It returns why each drive that cannot be served now is not; the rest are
// served.
func (c *Coordinator) SetDrives(ds map[string]Drive) []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drives = ds
	c.wake()
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(ds)) {
		if _, err := c.check(name); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// check is the server of the drive name, or why it has none. The caller
// holds mu.
func (c *Coordinator) check(name string) (*node, error) {
	d := c.drives[name]
	server := d.Server
	switch {
	case !worker.ValidDrive(name):
		return nil, fmt.Errorf("drive %q: a drive's name is lower case letters, digits and single dashes", name)
	case name == worker.Sessions && server != "":
		return nil, fmt.Errorf("drive %s takes no server: %s serves it", name, worker.Admin)
	case name == worker.Sessions && slices.Contains(d.Workers, everyone):
		return nil, fmt.Errorf("drive %s takes workers by name, never %q: each one reads every session", name, everyone)
	case name == worker.Sessions:
		server = worker.Admin
	case server == "":
		return nil, fmt.Errorf("drive %s names no server", name)
	}
	n := c.byName(server)
	if n == nil || server == worker.Harness || n.Hostinfo == nil || n.Hostinfo.OS != "linux" {
		return nil, fmt.Errorf("drive %s: its server %s is not a known Linux worker", name, server)
	}
	return n, nil
}

// on is whether worker n mounts drive d. The caller holds mu.
func on(d Drive, n *node) bool {
	return !n.Removed && n.Name != worker.Harness && (slices.Contains(d.Workers, n.Name) || !n.Ephemeral && slices.Contains(d.Workers, everyone))
}

// serves is whether a serves a drive that b mounts. The caller holds mu.
func (c *Coordinator) serves(a, b *node) bool {
	for name, d := range c.drives {
		if s, err := c.check(name); err == nil && s == a && on(d, b) {
			return true
		}
	}
	return false
}

// Desired is what the worker named name should serve, to which addresses,
// and mount, from where: the access rule again, for the file servers.
func (c *Coordinator) Desired(name string) worker.Desired {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := worker.Desired{Serve: []worker.Export{}, Mount: []worker.Mount{}}
	for _, dn := range slices.Sorted(maps.Keys(c.drives)) {
		d := c.drives[dn]
		s, err := c.check(dn)
		if err != nil {
			continue
		}
		if s.Name == name {
			e := worker.Export{Name: dn, Clients: []worker.Client{}}
			for _, n := range c.st.Nodes {
				if n != s && on(d, n) {
					e.Clients = append(e.Clients, worker.Client{Name: n.Name, Addr: address(n.ID)})
				}
			}
			want.Serve = append(want.Serve, e)
		}
		if n := c.byName(name); n != nil && on(d, n) {
			want.Mount = append(want.Mount, worker.Mount{Name: dn, Server: s.Name, Addr: address(s.ID)})
		}
	}
	return want
}
