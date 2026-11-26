# Privacy and security

## Permissions

A worker is a computer that you give to your agents. An agent has full
control of every worker its session names. It runs code as the user who
connected the worker, with no confirmation step.

Many coding agents imply that they have less access than they really have.
An agent with a shell tool can read and change almost everything its user
can: files, browser profiles, SSH keys, cloud credentials. Rules and
sandboxes that try to constrain an agent with a shell are hard to make
complete. A confirmation prompt on every command teaches people to press
Enter.

Mainplane has one clear and upfront boundary instead: the worker. An agent
has full control of the workers it is given, and no access to the machines it
is not given. Nothing has to enforce rules inside a worker, so the boundary is
simple and it holds.

```
 a worker                             not a worker
 +--------------------------------+   +--------------------------------+
 | what you trust agents with     |   | what you do not                |
 | the agent has full control     |   | the agent has no access        |
 +--------------------------------+   +--------------------------------+
```

What this means for where you connect a worker:

- Connect a worker on a machine whose contents you trust agents with: a
  server, a VM, a cloud machine or a spare computer. Keep passwords, wallets
  and personal files that you do not trust agents with off it.
- The install makes the harness machine a worker, `admin`. `admin` can
  read the harness config, which holds your provider keys. Run the harness on
  a machine that you trust agents with, or
  [with no admin worker](#a-harness-with-no-admin-worker).
- A machine that only sends messages to agents needs no worker. Log in with an
  API key ([without a terminal](self-hosting.md#without-a-terminal)).
- The worker service, `mainplaned`, runs as root. Agent code runs as the user
  who connected the worker, except on a machine with only a root shell and no
  sudo, where it runs as root. The root actions the harness can
  ask for have fixed purposes: join the network, mount a drive, update to a
  signed release.
- Two workers reach each other only when a link in the harness config names
  them ([the mesh](self-hosting.md#the-mesh)).
- Agents read messages, AGENTS.md files, command output and web pages. Text in
  any of them can try to steer an agent. Give a worker only what you would
  give an agent that reads all of them.

### Your own computer as a worker

A computer you use every day holds more than you may want agents to have.
To connect it with less:

- Make a separate user account for agents and install the worker from it.
  Agent code then runs as that user and reads only what that user can read.
- Or connect a VM on it instead, and share only the folders you choose.
- A session controls only the workers in its config (`workers`, or /workers
  in the terminal app). Leave your computer out of the sessions that do not
  need it.
- Link it to no other worker. The harness still reaches it, but other
  workers do not ([the mesh](self-hosting.md#the-mesh)).
- `mainplane worker remove <name>` takes it off the mesh within seconds, and
  `mainplane uninstall` on it removes the worker.

### The web interface

- The page keeps the API key in the browser's storage for the harness's URL
  and sends it in a header. There is no cookie, so another site cannot make
  the browser call the harness, and the API allows no other origin.
- The page loads no file from any other server, and its content security
  policy allows none. Text from a model is shown as text, never as HTML.
- A project's dashboard is HTML that agents wrote. It runs in a sandboxed
  frame with an origin of its own and no network, so it cannot read the key
  or call the harness. It can ask the page for four things: list and read
  its own project's files, send a message to the project's orchestrator as
  you, and set the project's status.
- A login link from `mainplane web` holds the API key. Treat it as the key.

### A harness with no admin worker

To keep agents off the harness machine, install the harness with
`--no-admin`. The quickstart runs on `admin`, so this is the plain install:

```shell
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- server --no-admin

# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) server -NoAdmin
```

Then no agent can reach the config, the provider keys or the harness logs.
Sessions, connectors, workers and drives work as before. You lose:

- Updates from `/settings`. Update with `sudo mainplane-server update` on the
  harness machine. Workers still follow the harness.
- The sessions drive, which only `admin` serves. No session can read other
  sessions' files.
- An agent that manages Mainplane for you: one that edits the config (links,
  drives, providers), reads the harness logs and makes keys.
- Drives served by the harness machine. A Linux worker can serve them instead.
- The quickstart and the CLI on the harness machine. Set provider keys in the
  shell before the install, or add them later with `mainplane key` from a
  logged-in CLI on another machine.

## Why this boundary

Models improve, and each new model can be trusted with more. Simple mistakes
are now rare. A prompt injection in a tool result now rarely succeeds. A
boundary of trust that is a set of machines can grow with the models. Put on a
worker what you trust agents with, and keep the rest off it. When you trust
them with more, connect more.

## Network

```
 web, TUI, CLI, connectors                      model providers
        |                                              ^
        | HTTPS                                        | HTTPS
        v                                              |
 Cloudflare edge <====== cloudflared (outbound) --> harness: mainplane-server
        ^                                           127.0.0.1:8080
        |                                           coordinator, relay, mesh node "harness"
        | HTTPS: coordinator (/ts2021), relay (/derp)
        |
   +----+---------------------+
   |                          |
 worker A (mainplaned) <====> worker B (mainplaned)
   WireGuard, UDP 41642: direct, or through the relay in the harness.
   Two workers talk only when a link names both. Each talks to the harness.
```

- The harness opens no inbound port. cloudflared, which the harness runs,
  makes an outbound connection to Cloudflare and carries the harness URL to
  `127.0.0.1:8080`. [Your own URL](self-hosting.md#your-own-url) can replace it.
- The mesh is built from Tailscale's open-source code, not from Tailscale the
  service. Workers run a fork of the Tailscale client (WireGuard, NAT
  traversal, the DERP relay protocol). The harness is the coordinator, with
  protocol code from headscale, and the relay. You need no Tailscale account.
  The fork sends no logs to Tailscale and asks Tailscale's servers for no DNS,
  and the relay map names only the harness and two STUN servers.
- Tailscale on the same machine keeps working
  ([details](self-hosting.md#the-mesh)).

What listens:

- Harness: HTTP on `127.0.0.1:8080` (`http` in the config). On the mesh only,
  its node `harness` serves port 7000 to workers and 8080 to the CLI on a
  worker. Its Claude Code provider and app daemons listen on `127.0.0.1`.
- Worker: WireGuard on UDP 41642 on every address. It drops packets that are
  not from a peer in its map. A TCP port on its mesh address, for the
  Tailscale peer API. A local socket for `mainplane status`
  (`/var/run/mainplaned.sock`, `\\.\pipe\mainplaned`).
- A Linux worker that serves a drive: NFS on its mesh address, and Samba on
  port 445 with only the mesh's addresses let in.

What connects out:

- Harness: model providers, Cloudflare (the tunnel), `pointer.mainplane.ai`,
  `dl.mainplane.ai` (updates and the model list), `data.mainplane.ai` only
  when you allow sharing, STUN, and GitHub and npm for pinned downloads.
- Worker: the harness URL, `pointer.mainplane.ai`, its peers, STUN,
  `dl.mainplane.ai` (updates), GitHub (Bun) and wintun.net (Windows).
- Agents: whatever their commands reach, and `bugs.mainplane.ai` when an
  agent sends a report. The terminal app sends a crash report there only
  when you say yes.

Each service is described below.

## Data privacy

Each service below sees some of your data. For each one: why it exists, what
it sees, and how to keep it local.

### Your model provider

- **Why it exists:** the model is the agent.
- **What it sees:** the whole context of each session: messages, tool calls,
  command output, and the files and images the agent reads.
- **How to keep it local:** run the model yourself
  ([Local LLMs](local-llms.md)).

### Cloudflare quick tunnel

- **Why it exists:** it gives the harness a public HTTPS URL with no account,
  no domain and no open port, so your devices reach the harness from anywhere.
- **What it sees:** TLS ends at Cloudflare. Cloudflare can read all traffic
  between connectors and the harness: API keys, prompts, command output and
  files. It cannot read the traffic between workers, which is WireGuard
  ([details](self-hosting.md#what-cloudflare-can-read)).
- **How to keep it local:** carry the harness URL with
  [your own reverse proxy](self-hosting.md#your-own-url), on your private
  network if you like. Then no traffic goes through Cloudflare.

### pointer.mainplane.ai

- **Why it exists:** you type a network name and a device code, never a URL.
  The pointer maps the name to the harness key, and the key to the harness's
  current URL. When a quick tunnel gets a new URL, every device finds the
  harness again with no new token.
- **What it sees:** it stores the network name, the harness's public key, its
  current URL and signatures. Each device that looks up its harness sends a
  request, so the pointer sees its IP address, as with any web request. It
  does not store addresses. It holds no secret: a device sends no secret to a
  URL until the harness there proves its key.
- **How to keep it local:** install the harness with `--no-pointer`
  (`-NoPointer` on Windows). The harness then never contacts the pointer. The
  lines it prints carry the harness URL in place of a network name and device
  code, and devices find the harness at that URL only. Without the pointer and
  without a stable URL, every worker and login loses its connection when the
  URL changes, and must be added again with new lines from
  `mainplane-server join` and `mainplane-server key new`. A quick tunnel
  usually keeps its URL when the harness restarts, but Cloudflare can change
  it at any time. Using `--no-pointer` is best with [your own URL](self-hosting.md#your-own-url).

### Data sharing: data.mainplane.ai

- **Why it exists:** shared sessions show where agents fail and what to fix in
  Mainplane. They are also used to train AI models.
- **What it sees:** nothing until you allow it. When you allow it, the records
  of sessions from that time on: messages, tool calls, command output, and
  the files and images the agent reads. Known key formats and the harness's
  own provider keys are scrubbed. Other private information is not.
- **How to keep it local:** it is off by default. The TUI asks once. Check
  and change it in `/settings`. In addition, the environment variable `DO_NOT_TRACK=1` keeps it off.

### Bug reports: bugs.mainplane.ai

- **Why it exists:** agents find bugs that users never see. Every report is
  read, and fixes go out in the next releases.
- **What it sees:** an agent can POST a plain-text report. Its system prompt
  says that it can, and that a report must be anonymous. After a crash, the
  TUI asks to send its crash report: the version, OS, terminal and stack
  trace, nothing of your sessions. The server keeps each report with the IP
  address, country, network and user agent it came from.
- **How to keep it local:** tell your agents in an AGENTS.md not to report
  bugs, or block `bugs.mainplane.ai` on your workers. Answer `n` to sending crash
  reports in the TUI if asked.

### Releases: dl.mainplane.ai

- **Why it exists:** install, updates and the model list. When the harness
  updates, every worker updates to the same release when it next connects.
  The harness names a version, never the bytes: each worker checks every file
  against a `SHA256SUMS` signed by the release key built into it.
- **What it sees:** the IP address of each download and version check, and
  which file.
- **How to keep it local:** mirror the release files and install by hand
  ([air-gapped](self-hosting.md#air-gapped)).

### Other downloads and STUN

- **Why they exist:** Bun (the js interpreter) and cloudflared come from
  GitHub releases, Claude Code from npm, and `wintun.dll` from wintun.net. STUN
  at Cloudflare and Google tells each worker its public address, so two
  workers can connect directly.
- **What they see:** the IP address of each download. STUN sees the worker's
  public IP address and port.
- **How to keep it local:** each download is pinned to a version and checked
  against a pinned hash, so a mirror works. Devices on one LAN find each other
  without STUN, and the relay in the harness carries the rest.
