package apps

import "golang.org/x/sys/windows"

// gone is whether no process has the pid, or the one that has it has exited
// and is only held open.
func gone(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == windows.WAIT_OBJECT_0
}
