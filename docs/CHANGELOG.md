# Changelog

One section per release, newest first. An `-rc` tag ships the section of the release it is a candidate for.

## v0.4.0

- The harness install prints a join token named `default`, good for any number of machines, and the lines that install a worker with it on Linux, macOS and Windows. Each install makes a new one; the one before joins no more machines. The name in `mainplane-server join new <name>` is the secret's, for revoke; a worker is named by its hostname
- Installs print less: no checksum lines, a spinner while the harness and its tunnel start, then a wordmark, the version, and what to do next
- Sessions: `context` in a create body and in the config record is `context_limit`, and `prompt` in session info is `context_used`. A session made before reads a limit of 0 and fails at its next step: start a new one
- `mainplane status` starts with the harness the CLI is logged in to: its version and URL. The worker's own row says `harness unresponsive` where it said `map poll down`
- `mainplane workers` shows a worker's version only when it is not the harness's
- Windows: the UAC prompt names Mainplane Worker

## v0.3.0

Workers join a WireGuard mesh that the harness coordinates, and reach the harness over it. A v0.2.0 worker dials `/worker`, which is gone, so it cannot reach a v0.3.0 harness and cannot update itself: on each worker, `mainplane uninstall`, then install again with its join token. Tokens and the harness config carry over. See `docs/self-hosting.md`, "The mesh".

- Every worker gets an interface of its own (`mainplane0`, `Mainplane` on Windows, `utunN` on macOS), an address in `fd7c:9a2e:4b10::/48`, UDP port 41642, and a hosts block with `<name>` and `<name>--home.mainplane.net` for each peer. It runs beside Tailscale. The harness runs the coordinator and the relay behind its tunnel, and joins the mesh itself, in userspace, as `harness`
- The worker's control connection goes to `harness` port 7000 inside the mesh. The `/worker` WebSocket is gone. Workers need root (they did already as a service)
- Workers talk direct when their NATs allow, else through the relay in the harness, over the tunnel. `mainplane status` on a worker shows its name and address, its harness, and each peer's address and path: direct or relay
- Worker names are mesh names now: the hostname's first label in lower case, made unique (`alexanders-mac-mini`, `box-2`). Session configs must use them
- Your own workers see each other and the harness. `mainplane-server join new <name> ephemeral` makes a join secret whose workers see only the harness and leave the mesh 3 minutes after they go quiet
- `mainplane worker remove <name>` takes a worker off the mesh in seconds; it takes down its interface and hosts block. `join revoke` still only refuses new joins
- On Windows, `mainplane install` downloads `wintun.dll` 0.14.1 from wintun.net, checked against its sha256
- The `mainplane` binary grows from about 7 MB to about 18 MB, and `mainplane-server` from about 8 MB to about 23 MB: both carry Tailscale's network engine, from the fork `github.com/mainplane-ai/tailscale`

## v0.2.0

The harness needs no network setup: a Cloudflare tunnel is its only way in. Tokens, configs and workers from v0.1.x do not carry over. Uninstall the v0.1.x harness with its own `mainplane-server uninstall`, which on Windows also removes its port 7811 firewall rule, then install the harness and every worker again.

- `mainplane-server install` opens a Cloudflare quick tunnel: a temporary `https://*.trycloudflare.com` URL, no account, domain or inbound port. The harness listens on loopback only, and runs cloudflared, pinned and checked against its sha256, as a child. `host` and `--host` are gone
- Workers dial a WebSocket at `/worker` on the harness's one port; port 7811 and its Windows firewall rule are gone
- Tokens carry the harness key, not an address. Workers and the CLI find the harness through `pointer.mainplane.ai`, and the harness proves its key before a secret goes to it. A harness that moves keeps its tokens: workers and the CLI follow
- `mainplane-server tunnel <url> <cloudflared token>` moves the harness to a tunnel you made in Cloudflare, on your own domain; `mainplane-server tunnel quick` goes back. See `docs/self-hosting.md`, which also says what Cloudflare can read
- 30 refused api keys or join secrets a minute get a 429 that says when to try again

## v0.1.1

- One line makes a machine the harness: `curl -fsSL https://mainplane.ai/install | sh -s -- server [host]`, or on Windows `& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) server [host]`. Run it without sudo; it asks for root or admin itself. It installs the CLI and `mainplane-server`, starts the harness with the provider keys set in the shell (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, or `AWS_BEARER_TOKEN_BEDROCK` with `AWS_REGION`), logs the CLI in to it, and prints how to add a worker. Workers dial `host`, the machine's name by default
- `mainplane-server install` with no config writes the default one: workers on port 7811, connectors on 8080, sessions under the harness's own folder, and every provider whose key is set. `--host <name>` sets the name tokens carry. It issues an api key named after the machine and logs that machine's CLI in; installing again replaces that key. With no provider key set it stops and names the variables it looked for
- Install waits until the harness answers on its HTTP port, and fails with a pointer to its log when it does not
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
