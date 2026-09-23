# Changelog

One section per release, newest first. An `-rc` tag ships the section of the release it is a candidate for.

## v0.1.0

The first release: a harness, workers that dial it, and a CLI.

- `mainplane-server up` runs the harness: sessions as append-only state files, fork and revert by copy, stop and retry, context limits, and five provider envelopes (Anthropic, OpenAI Responses, OpenAI Chat compatible, Gemini, Bedrock)
- `mainplane-server install <config.json>` runs the harness at every boot as root: systemd, a LaunchDaemon, or on Windows a startup task as SYSTEM with a firewall rule. It runs from a root-only copy of the config with provider values expanded, since a service has no user environment
- `mainplane-server update [version]` makes the installed harness another release, the latest stable by default, from the same signed `SHA256SUMS` workers use. It prints the changelog between the two versions and restarts the harness; workers follow at their next hello, so one command updates the fleet
- `mainplane-server key` and `join` issue api keys for connectors and join tokens for workers; the table holds only hashes
- `curl -fsSL https://dl.mainplane.ai/<version>/install.sh | sudo sh -s -- <join token>` makes a Linux or macOS machine a worker: a root service at every boot (systemd, LaunchDaemon) that runs code and touches files only as the user who installed it. On Windows, `install.ps1` sets up a logon task for the user who runs it
- `mainplane update [version]` makes the CLI the release of the harness it is logged into, or the latest stable when logged into none; any verb against a harness from another release says to run it
- Commands that need root or admin ask for it: `mainplane-server install` and `update`, and `mainplane install` on Linux and macOS, run themselves again under sudo, or behind the UAC prompt on Windows
- `mainplane login <api key>` and one CLI verb per harness route: `new`, `message`, `tail`, `chat`, `retry`, `stop`, `info`, `sessions`, `workers`, `providers`
- Every binary carries its release version (`mainplane version`, `mainplane-server version`). A worker from another release updates itself to the harness's, up or down, from a `SHA256SUMS` signed by the release key; `mainplane workers` lists a worker whose update failed, with the reason
- Install says when the operator can use sudo: code on the worker runs as the operator, so it can then act as root
