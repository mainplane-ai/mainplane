package worker

import (
	"fmt"
	"os"
	"path/filepath"
)

// A task at logon runs in the operator's interactive session, so what the
// worker starts can show a window. Task Scheduler stops a task after 72 hours
// unless the limit is zero. cmd carries the log redirect, and ending the task
// ends only cmd, so the worker it started is stopped by name. A cmdlet error
// is not an exit code unless it stops the script.
const task = `$ErrorActionPreference = 'Stop'
$a = New-ScheduledTaskAction -Execute cmd -Argument '/c ""%s" worker %s >> "%s" 2>&1"'
$t = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName mainplaned -Action $a -Trigger $t -Settings $s -Force | Out-Null
Stop-ScheduledTask -TaskName mainplaned
Get-CimInstance Win32_Process -Filter "CommandLine LIKE '%% worker mp_%%' AND ProcessId != $PID" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
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
	log := filepath.Join(home, ".mainplane", "mainplaned.log")
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		return err
	}
	return run("powershell", "-NoProfile", "-Command", fmt.Sprintf(task, exe, token, log))
}
