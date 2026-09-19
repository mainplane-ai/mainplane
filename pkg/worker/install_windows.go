package worker

import (
	"fmt"
	"os"
	"path/filepath"
)

// A task at logon runs in the operator's interactive session, so what the
// worker starts can show a window. Task Scheduler stops a task after 72 hours
// unless the limit is zero. cmd carries the log redirect, and ending the task
// ends only cmd, so the worker it started is stopped by its command line,
// `"...mainplane.exe" worker`; the _ stands for the quote, which a raw string
// cannot escape. A cmdlet error is not an exit code unless it stops the script.
const task = `$ErrorActionPreference = 'Stop'
$a = New-ScheduledTaskAction -Execute cmd -Argument '/c ""%s" worker >> "%s" 2>&1"'
$t = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName mainplaned -Action $a -Trigger $t -Settings $s -Force | Out-Null
Stop-ScheduledTask -TaskName mainplaned
Get-CimInstance Win32_Process -Filter "CommandLine LIKE '%%mainplane.exe_%%worker%%' AND ProcessId != $PID" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
Start-ScheduledTask -TaskName mainplaned`

// Install makes this machine a worker at every logon: a scheduled task running
// this binary as the operator. Logs are in ~/.mainplane/mainplaned.log.
func Install(token string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := JoinPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return err
	}
	log := filepath.Join(filepath.Dir(path), "mainplaned.log")
	return run("powershell", "-NoProfile", "-Command", fmt.Sprintf(task, exe, log))
}
