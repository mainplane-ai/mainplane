# Changelog

One section per release, newest first. An `-rc` tag ships the section of the release it is a candidate for.

## v0.1.1

- `mainplane-server key` and `join` act on the installed harness and ask for root or admin themselves: `mainplane-server join new mac`. They no longer take a config path
- `mainplane workers` prints one line per worker: name, OS and architecture, interpreters, version, and connected or why it was refused
- `mainplane uninstall` removes the worker service, the CLI and the join token; `mainplane-server uninstall` removes the harness service, its binary and, on Windows, its firewall rule. Both ask for root or admin themselves. The harness's config, sessions and auth table stay, and so does each operator's `~/.mainplane`. On Windows a running binary moves to the temp folder and is deleted at the next reboot, and a folder left empty comes off PATH

## v0.1.0

The first release: a harness, workers that dial it, and a CLI.

- `mainplane-server up` runs the harness: sessions as append-only state files, fork and revert by copy, stop and retry, context limits, and five provider envelopes (Anthropic, OpenAI Responses, OpenAI Chat compatible, Gemini, Bedrock)
- `mainplane-server install <config.json>` runs the harness at every boot as root: systemd, a LaunchDaemon, or on Windows the service Mainplane Server as LocalSystem, restarted when it fails, with a firewall rule. `Get-Service mainplane-server` and services.msc show and control it; its log is `%ProgramData%\mainplane-server\mainplane-server.log`. It runs from a root-only copy of the config with provider values expanded, since a service has no user environment
- `mainplane-server update [version]` makes the installed harness another release, the latest stable by default, from the same signed `SHA256SUMS` workers use. It prints the changelog between the two versions and restarts the harness; workers follow at their next hello, so one command updates the fleet
- `mainplane-server key` and `join` issue api keys for connectors and join tokens for workers; the table holds only hashes. `join new` also prints the install command for each OS
- `curl -fsSL https://dl.mainplane.ai/<version>/install.sh | sudo sh -s -- <join token>` makes a Linux or macOS machine a worker: a root service at every boot (systemd, LaunchDaemon) that runs code and touches files only as the user who installed it. On Windows, `& ([scriptblock]::Create((irm https://dl.mainplane.ai/<version>/install.ps1))) <join token>` does the same after a UAC prompt: the service Mainplane Worker as LocalSystem, restarted when it ends, which runs code in the installing user's logon session (windows it opens show on their desktop) and fails with "operator X is not logged in on Y" while they are not. `Get-Service mainplaned` shows it; its log is `%ProgramData%\mainplane\mainplaned.log`. Without a token, both install only the CLI. `https://dl.mainplane.ai/install.sh` and `install.ps1` are the scripts of the latest stable release
- On Windows, `install.ps1` without a token puts `mainplane` on the user PATH; `mainplane install` and `mainplane-server install` put `C:\Program Files\mainplane` on the machine PATH
- `mainplane update [version]` makes the CLI the release of the harness it is logged into, or the latest stable when logged into none; any verb against a harness from another release says to run it
- Commands that need root or admin ask for it: `mainplane-server install` and `update`, and `mainplane install`, run themselves again under sudo, or behind the UAC prompt on Windows. Install expands provider variables in the shell that runs it, before elevating
- `mainplane login <api key>` and one CLI verb per harness route: `new`, `message`, `tail`, `chat`, `retry`, `stop`, `info`, `sessions`, `workers`, `providers`
- Every binary carries its release version (`mainplane version`, `mainplane-server version`). A worker from another release updates itself to the harness's, up or down, from a `SHA256SUMS` signed by the release key; `mainplane workers` lists a worker whose update failed, with the reason
- Windows binaries are Authenticode signed, publisher Alexander Yue, and carry their name and version (VERSIONINFO)
- Install says when the operator can use sudo: code on the worker runs as the operator, so it can then act as root
