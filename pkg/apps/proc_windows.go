//go:build windows

package apps

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	modiphlpapi                 = windows.NewLazySystemDLL("iphlpapi.dll")
	procSetInformationJobObject = modkernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObj  = modkernel32.NewProc("QueryInformationJobObject")
	procGetExtendedTcpTable     = modiphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	jobObjectBasicProcessIDList       = 3
	jobObjectExtendedLimitInformation = 9
	tcpTableOwnerPIDListener          = 3
	afInet                            = 2
	afInet6                           = 23
	errorInsufficientBuffer           = 122
)

type procGroup struct {
	mu      sync.Mutex
	job     windows.Handle
	rootPID uint32
}

func startProcGroup(cmd *exec.Cmd) (*procGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	r1, _, e1 := procSetInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if r1 == 0 {
		_ = windows.CloseHandle(job)
		return nil, e1
	}
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	pid := uint32(cmd.Process.Pid)
	hProc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	assignErr := windows.AssignProcessToJobObject(job, hProc)
	_ = windows.CloseHandle(hProc)
	if assignErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, assignErr
	}
	return &procGroup{job: job, rootPID: pid}, nil
}

func (pg *procGroup) kill() {
	if pg == nil {
		return
	}
	pg.mu.Lock()
	job := pg.job
	pg.job = 0
	pg.mu.Unlock()
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
}

func (pg *procGroup) ownsTCPPort(port int) bool {
	if pg == nil {
		return false
	}
	pids := pg.jobPIDs()
	if len(pids) == 0 {
		return false
	}
	owned4, foreign4 := tcp4PortStatus(port, pids)
	owned6, foreign6 := tcp6PortStatus(port, pids)
	return (owned4 || owned6) && !foreign4 && !foreign6
}

type jobProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [256]uintptr
}

func (pg *procGroup) jobPIDs() map[uint32]bool {
	pg.mu.Lock()
	job := pg.job
	pg.mu.Unlock()
	if job == 0 {
		return nil
	}
	out := map[uint32]bool{pg.rootPID: true}
	var list jobProcessIDList
	r1, _, _ := procQueryInformationJobObj.Call(
		uintptr(job),
		jobObjectBasicProcessIDList,
		uintptr(unsafe.Pointer(&list)),
		unsafe.Sizeof(list),
		0,
	)
	if r1 != 0 {
		n := min(int(list.NumberOfProcessIdsInList), len(list.ProcessIdList))
		for i := range n {
			out[uint32(list.ProcessIdList[i])] = true
		}
	}
	return out
}

type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

type mibTCP6RowOwnerPID struct {
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	State         uint32
	OwningPid     uint32
}

func ntohs16(v uint32) int {
	return int(uint16(v)<<8 | uint16(v>>8))
}

func fetchTCPTable(family uintptr) []byte {
	var size uint32 = 4096
	for range 4 {
		buf := make([]byte, size)
		r1, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0,
			family,
			tcpTableOwnerPIDListener,
			0,
		)
		if r1 == 0 {
			return buf
		}
		if r1 != errorInsufficientBuffer {
			return nil
		}
	}
	return nil
}

func tcp4PortStatus(port int, pids map[uint32]bool) (owned, foreign bool) {
	buf := fetchTCPTable(afInet)
	if len(buf) < 4 {
		return false, false
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	for i := range int(count) {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := *(*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[off]))
		if ntohs16(row.LocalPort) != port {
			continue
		}
		if !pids[row.OwningPid] {
			foreign = true
			continue
		}
		if row.LocalAddr == 0 || row.LocalAddr == 0x0100007f {
			owned = true
		}
	}
	return owned, foreign
}

func isWinLoopbackV6(addr [16]byte) bool {
	if addr == ([16]byte{}) {
		return true
	}
	v4MappedLoopback := [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}
	return addr == v4MappedLoopback
}

func tcp6PortStatus(port int, pids map[uint32]bool) (owned, foreign bool) {
	buf := fetchTCPTable(afInet6)
	if len(buf) < 4 {
		return false, false
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := int(unsafe.Sizeof(mibTCP6RowOwnerPID{}))
	for i := range int(count) {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := *(*mibTCP6RowOwnerPID)(unsafe.Pointer(&buf[off]))
		if ntohs16(row.LocalPort) != port {
			continue
		}
		if !pids[row.OwningPid] {
			foreign = true
			continue
		}
		if isWinLoopbackV6(row.LocalAddr) {
			owned = true
		}
	}
	return owned, foreign
}
