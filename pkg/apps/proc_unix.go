//go:build !windows

package apps

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type procGroup struct {
	pgid    int
	process *os.Process
}

func startProcGroup(cmd *exec.Cmd) (*procGroup, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procGroup{pgid: cmd.Process.Pid, process: cmd.Process}, nil
}

func (pg *procGroup) kill() {
	if pg != nil && pg.pgid > 0 {
		_ = syscall.Kill(-pg.pgid, syscall.SIGKILL)
		if pg.process != nil {
			_ = pg.process.Kill()
		}
	}
}

func (pg *procGroup) ownsTCPPort(port int) bool {
	if pg == nil || pg.pgid <= 0 {
		return false
	}
	if runtime.GOOS == "linux" {
		return linuxGroupOwnsPort(pg.pgid, port)
	}
	return lsofGroupOwnsPort(pg.pgid, port)
}

func lsofGroupOwnsPort(pgid, port int) bool {
	lsof := "/usr/sbin/lsof"
	if _, err := os.Stat(lsof); err != nil {
		lsof = "lsof"
	}
	out, err := exec.Command(lsof, "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpgn").Output()
	if err != nil {
		return false
	}
	wantPgrp := strconv.Itoa(pgid)
	portStr := strconv.Itoa(port)
	var curG string
	owned := false
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "g"):
			curG = line[1:]
			if curG != wantPgrp {
				return false
			}
		case strings.HasPrefix(line, "n"):
			addr := line[1:]
			if curG == wantPgrp && (addr == "127.0.0.1:"+portStr || addr == "*:"+portStr) {
				owned = true
			}
		}
	}
	return owned
}

func isLinuxLoopbackV4(localHex string) bool {
	switch localHex {
	case "00000000", "0100007F", "7F000001",
		"00000000000000000000000000000000",
		"0000000000000000FFFF00000100007F",
		"00000000000000000000FFFF7F000001":
		return true
	default:
		return false
	}
}

func linuxListenInodes(port int) (allInodes, loopInodes map[string]bool) {
	allInodes = map[string]bool{}
	loopInodes = map[string]bool{}
	wantPort := fmt.Sprintf("%04X", port)
	for _, netFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(netFile)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if i == 0 {
				continue
			}
			// /proc/net/tcp fields: 0:sl 1:local 2:rem 3:st 4:tx:rx 5:tr:tm 6:retrnsmt 7:uid 8:timeout 9:inode
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" || f[9] == "0" {
				continue
			}
			hostHex, portHex, ok := strings.Cut(strings.ToUpper(f[1]), ":")
			if !ok || portHex != wantPort {
				continue
			}
			inode := "socket:[" + f[9] + "]"
			allInodes[inode] = true
			if isLinuxLoopbackV4(hostHex) {
				loopInodes[inode] = true
			}
		}
	}
	return allInodes, loopInodes
}

func collectGroupInodes(pid, wantPgrp string, target, owned map[string]bool) {
	if pid == "" || pid[0] < '0' || pid[0] > '9' {
		return
	}
	statBytes, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return
	}
	rp := bytes.LastIndexByte(statBytes, ')')
	if rp < 0 || rp+2 >= len(statBytes) {
		return
	}
	rest := strings.Fields(string(statBytes[rp+2:]))
	if len(rest) < 3 || rest[2] != wantPgrp {
		return
	}
	fds, err := os.ReadDir(filepath.Join("/proc", pid, "fd"))
	if err != nil {
		return
	}
	for _, fd := range fds {
		if link, err := os.Readlink(filepath.Join("/proc", pid, "fd", fd.Name())); err == nil && target[link] {
			owned[link] = true
		}
	}
}

func linuxGroupOwnsPort(pgid, port int) bool {
	allInodes, loopInodes := linuxListenInodes(port)
	if len(loopInodes) == 0 {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	owned := map[string]bool{}
	wantPgrp := strconv.Itoa(pgid)
	for _, e := range entries {
		collectGroupInodes(e.Name(), wantPgrp, allInodes, owned)
	}
	for inode := range allInodes {
		if !owned[inode] {
			return false
		}
	}
	return true
}
