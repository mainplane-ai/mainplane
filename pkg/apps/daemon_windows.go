package apps

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// inherit is what a daemon takes of the harness's environment, where it is
// set. Nothing else: a provider key may be there. Windows programs read far
// more of it than Unix ones: without SYSTEMROOT no socket opens, runtimes
// find their caches and settings by the user's folders and their own
// install by the machine's, and a shell needs COMSPEC and PATHEXT to start
// anything.
var inherit = []string{
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "OS", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS",
	"PATH", "PATHEXT",
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "HOME", "USERNAME", "APPDATA", "LOCALAPPDATA",
	"PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"TEMP", "TMP",
}

// tree is a daemon's job object: its first process and all that one
// started, however deep, whether or not the first still runs.
type tree struct{ job windows.Handle }

// start runs cmd in a job object of its own, which the end of cmd's context
// kills whole. Windows has no process groups to signal, and finds a tree by
// its root only while the root lives; a job holds every descendant. The
// process starts suspended and runs once it is in the job, so it starts
// nothing outside it. The job kills what is in it when its handle closes,
// so no daemon outlives the harness. A daemon gets no console window.
func start(cmd *exec.Cmd) (tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return tree{}, err
	}
	t := tree{job}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.sweep()
		return t, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	cmd.Cancel = t.kill
	if err = cmd.Start(); err != nil {
		t.sweep()
		return t, err
	}
	if err = t.adopt(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.sweep()
	}
	return t, err
}

// adopt puts the suspended process in the job and lets it run. os/exec
// keeps no handle of the first thread, so the threads are found by a
// snapshot of the system's.
func (t tree) adopt(p *os.Process) error {
	var err error
	if herr := p.WithHandle(func(h uintptr) { err = windows.AssignProcessToJobObject(t.job, windows.Handle(h)) }); herr != nil || err != nil {
		return fmt.Errorf("process %d did not join its job: %w", p.Pid, cmp.Or(herr, err))
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	resumed := 0
	e := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snap, &e); err == nil; err = windows.Thread32Next(snap, &e) {
		if e.OwnerProcessID != uint32(p.Pid) {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, e.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("process %d has no thread to resume: %w", p.Pid, err)
	}
	return nil
}

// kill ends every process in the job.
func (t tree) kill() error { return windows.TerminateJobObject(t.job, 1) }

// sweep closes the job, which kills what an exited daemon left behind.
func (t tree) sweep() { _ = windows.CloseHandle(t.job) }
