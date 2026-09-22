package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Until the Windows service lands the harness is a task at startup running as
// SYSTEM, which needs no one logged in. Install needs an elevated shell.
//
// cmd carries the log redirect, and ending the task ends only cmd, so a
// running harness is stopped by its command line, `"...mainplane-server.exe"
// up`; the _ stands for the quote, which a raw string cannot escape. The
// harness stops before the binary is placed, because Windows cannot replace a
// running one. A failed harness is started again after a minute, the
// shortest wait Task Scheduler allows. A cmdlet error is not an exit code
// unless it stops the script.
const (
	stop = `Stop-ScheduledTask -TaskName mainplane-server -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process -Filter "CommandLine LIKE '%mainplane-server.exe_ up%' AND ProcessId != $PID" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`
	task = `$ErrorActionPreference = 'Stop'
$a = New-ScheduledTaskAction -Execute cmd -Argument '/c ""%s" up "%s" >> "%s" 2>&1"'
$t = New-ScheduledTaskTrigger -AtStartup
$p = New-ScheduledTaskPrincipal -UserId SYSTEM -LogonType ServiceAccount -RunLevel Highest
$s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName mainplane-server -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null
Start-ScheduledTask -TaskName mainplane-server`
)

var (
	Dir = filepath.Join(os.Getenv("ProgramData"), "mainplane-server")
	bin = filepath.Join(os.Getenv("ProgramFiles"), "mainplane", "mainplane-server.exe")
)

// prepare stops a running harness and makes Dir, which only SYSTEM and
// Administrators may read: the config in it holds the provider keys, and
// ProgramData lets every user read by default. The reset drops every entry an
// earlier install or someone else left, so only the two grants remain.
func prepare() error {
	if err := run("powershell", "-NoProfile", "-Command", stop); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	if err := run("icacls", Dir, "/reset", "/T", "/Q"); err != nil {
		return err
	}
	return run("icacls", Dir, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F")
}

// start lets the harness's ports in and runs it at every boot. Logs are in
// Dir\mainplane-server.log.
func start() error {
	_ = run("netsh", "advfirewall", "firewall", "delete", "rule", "name=mainplane-server")
	if err := run("netsh", "advfirewall", "firewall", "add", "rule", "name=mainplane-server", "dir=in", "action=allow", "program="+bin); err != nil {
		return err
	}
	// the paths sit in a single-quoted string, where a quote is doubled
	q := strings.NewReplacer("'", "''")
	if err := run("powershell", "-NoProfile", "-Command", fmt.Sprintf(task, q.Replace(bin), q.Replace(Conf), q.Replace(filepath.Join(Dir, "mainplane-server.log")))); err != nil {
		return err
	}
	fmt.Println("note: the harness stops while this machine sleeps; powercfg /change standby-timeout-ac 0 keeps it awake on power")
	return nil
}

func restart() error {
	return run("powershell", "-NoProfile", "-Command", stop+"\nStart-ScheduledTask -TaskName mainplane-server")
}
