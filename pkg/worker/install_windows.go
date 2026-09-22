package worker

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Until the Windows service lands the install is the user's own: no admin,
// the binary under LOCALAPPDATA, the token in scratch, and code runs as the
// user who installed.
//
// A task at logon runs in the operator's interactive session, so what the
// worker starts can show a window. Task Scheduler stops a task after 72 hours
// unless the limit is zero. cmd carries the log redirect, and ending the task
// ends only cmd, so a running worker is stopped by its command line,
// `"...mainplane.exe" worker`; the _ stands for the quote, which a raw string
// cannot escape. Workers stop before the binary is placed, because Windows
// cannot replace a running one. A cmdlet error is not an exit code unless it
// stops the script.
const (
	stop = `Stop-ScheduledTask -TaskName mainplaned -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process -Filter "CommandLine LIKE '%mainplane.exe_%worker%' AND ProcessId != $PID" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`
	task = `$ErrorActionPreference = 'Stop'
$a = New-ScheduledTaskAction -Execute cmd -Argument '/c ""%s" worker >> "%s" 2>&1"'
$t = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName mainplaned -Action $a -Trigger $t -Settings $s -Force | Out-Null
Start-ScheduledTask -TaskName mainplaned`
)

func scratch() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".mainplane"), err
}

// Installed is what install left for the task: the join token. Code runs as
// the worker's own user, so there is no operator to switch to.
func Installed() (string, *user.User, error) {
	dir, err := scratch()
	if err != nil {
		return "", nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "join"))
	return string(b), nil, err
}

// Install makes this machine a worker at every logon: a scheduled task running
// the binary as the operator. Logs are in ~/.mainplane/mainplaned.log.
func Install(token string) error {
	dir, err := scratch()
	if err != nil {
		return err
	}
	if err := run("powershell", "-NoProfile", "-Command", stop); err != nil {
		return err
	}
	bin := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "mainplane", "mainplane.exe")
	if err := place(bin); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "join"), []byte(token), 0o600); err != nil {
		return err
	}
	// the paths sit in a single-quoted string, where a quote is doubled
	q := strings.NewReplacer("'", "''")
	return run("powershell", "-NoProfile", "-Command", fmt.Sprintf(task, q.Replace(bin), q.Replace(filepath.Join(dir, "mainplaned.log"))))
}
