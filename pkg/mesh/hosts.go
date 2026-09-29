package mesh

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"tailscale.com/tailcfg"
	"tailscale.com/types/netmap"
)

// Names come from a block in the hosts file, which every OS reads before
// DNS, so no resolver is touched. Other tools' lines stay as they are.
const (
	begin = "# mainplane begin"
	end   = "# mainplane end"
	// Every long name is one label under it: one wildcard covers them all.
	domain = "mainplane.net"
)

func hostsPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

// block is a line for every node in nm, this one too: its address, its
// name, and its long name, <name>--<project>.mainplane.net.
func block(nm *netmap.NetworkMap) string {
	if nm == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range append([]tailcfg.NodeView{nm.SelfNode}, nm.Peers...) {
		if n.Valid() && n.Addresses().Len() > 0 {
			fmt.Fprintf(&b, "%s %s %s--%s.%s\n", n.Addresses().At(0).Addr(), n.Name(), n.Name(), nm.Domain, domain)
		}
	}
	return begin + "\n" + b.String() + end + "\n"
}

// hosts puts blk in the hosts file in place of the block there, or removes
// the block when blk is empty. The new file replaces the old in one rename,
// so no reader sees it half written.
func hosts(blk string) error {
	f := hostsPath()
	old, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	s := string(old)
	// An editor may have saved the block with CRLF, as Windows hosts files are.
	if i := strings.Index(s, begin); i >= 0 {
		if j := strings.Index(s[i:], end); j >= 0 {
			s = s[:i] + strings.TrimPrefix(strings.TrimPrefix(s[i+j+len(end):], "\r"), "\n")
		}
	}
	if blk != "" && s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s += blk; s == string(old) {
		return nil
	}
	if err := os.WriteFile(f+".mainplane", []byte(s), 0o644); err != nil {
		return err
	}
	return os.Rename(f+".mainplane", f)
}
