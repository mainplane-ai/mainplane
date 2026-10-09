# Changelog

One section per release, newest first. An `-rc` tag ships the section of the release it is a candidate for.

## v0.5.2

A machine joins with a network name and a device code, `curl -fsSL https://mainplane.ai/install | sh -s -- sketchy-armadillo-431 K7QM-4ZTR`, short enough to type, in place of a join token. Workers joined before stay. Install the harness again to see its name and code: its join tokens, and their entries in the auth table, are no longer read.

- The first `mainplane-server install` gives the harness a random network name, claimed on `pointer.mainplane.ai`, and an 8-character device code, and both stay across installs. `connect a worker:` is now `connect a device:`
- `mainplane-server join` prints the lines; `join cycle` makes a new code, and workers that joined with the one before stay; `mainplane-server rename <name>` changes the name, and a name another harness holds is refused. `join new`, `join revoke`, `join list` and ephemeral join secrets are gone
- The code never travels: the machine and the harness run CPace with it at `POST /join`, and the harness answers with the join secret sealed under the key they share. Every try counts against the address's 30 a minute. The join secret derives from the code, so a cycled code refuses it and no table holds it
- `install.sh` and `install.ps1` take `<network> <device code>` and no longer take a join token. `mainplane install` and `mainplane worker` take the same two
- A node is ephemeral when it asks to be at registration, as a `tsnet` node with `Ephemeral` does, not by its secret. The offnet workflow takes `network` and `code`, and uninstalls at the end
- `mainplane-server install` prints the join and login lines as soon as the harness answers on this machine, then waits behind `initializing network`, which ends in `done`, for the tunnel and the CLI login. The lines name no URL, so a worker installed meanwhile joins once the tunnel is up. The install waited for the tunnel before it printed anything, about 20 seconds on a first install

## v0.5.1

- `mainplane-server install` on Linux or macOS with no harness there before makes the auth table the operator's. It was root's, so the harness refused every key and the install's own login failed with `GET /: 401 api key refused`

## v0.5.0

The harness install also makes its machine a worker, `admin`, and the harness's directory is split: config, keys, auth table, nodes, mesh and tunnel stay in it, and sessions move to `sessions/` under it. The `admin` config field is gone. A harness from before keeps its files in the directory its `admin` field names (`admin/` by default), which this one does not read: to keep its key, workers and sessions, stop it, move the files in that directory into the config's directory and each `.state` file into `sessions/`, run `mainplane uninstall` if the machine is a worker, so it joins again as `admin`, then run the harness install line again.

- `mainplane-server install` makes the machine a worker named `admin` while the tunnel starts, with the `mainplane` the install line downloads beside `mainplane-server`, which the worker install places as the machine's one CLI (on Windows only in Program Files: `install.ps1 server` no longer puts a second one in `LOCALAPPDATA`). It joins with a join secret named `admin` that no token shows, new at each install that joins it; an install again on a machine that is already `admin` keeps it as it is. `admin` is reserved like `harness`: a machine named admin joins as `admin-2`, and `mainplane-server join new admin` is refused
- `mainplane install <join token>` on a machine already a worker of that harness says `this machine is already worker <name> of this mainplane-server`, exits 0 and keeps its join token, with no sudo or UAC prompt
- The worker install line needs no sudo, `curl -fsSL .../install.sh | sh -s -- <join token>`: `mainplane install` asks for it itself. The install no longer says when the operator can use sudo
- `install.sh` and `install.ps1` take an api key, `curl -fsSL https://mainplane.ai/install | sh -s -- mp_key_...`, and log this user's CLI in, as them, not root. A machine that has a CLI, a worker's, a harness's or one installed before, uses it and gets no second copy; with no argument the script says where it is. Else `install.sh` places it in `/usr/local/bin` with sudo, so it works under `| sh`, and `install.ps1` for the user in `LOCALAPPDATA`. Any other argument is a usage error
- The harness's directory belongs to the user who installs: on Linux and macOS the harness runs as them, not root, and on Windows they are on its access list beside SYSTEM and Administrators. `admin`, whose code runs as them, and they without sudo, read and edit the config and keys
- Linux and macOS: the operator, the user the harness and `admin` run code as, is the user who ran sudo, or root in a root shell with no sudo, as on a fresh VPS. `install.sh` needs no sudo when run as root
- `mainplane-server install` with no config keeps an installed `config.json` exactly as it is; `mainplane-server install <config.json>` replaces it
- `mainplane-server uninstall` removes `admin`, and the CLI with it, with the harness
- `mainplane-server install` prints a new api key named `default` beside the default join token, each under what it does (`connect a worker:`, `log in:`) in the `mainplane.ai/install` lines that use it, one for Linux and macOS and one for Windows, not the lines of its own version. The default keys from earlier installs stay valid until `mainplane-server key revoke default`, so a reinstall logs no connector out. The CLI on the harness's machine logs in with it, except after an install from a config file, which only prints it; no key is named after the hostname
- `mainplane login` to the harness it is logged in to says `already logged in to this mainplane-server`, and to another one that it switched; it refuses a key the harness does not take. Then the CLI becomes the harness's release when it is another
- `mainplane key <provider> <key>` saves a provider key in the harness config from any logged-in machine (`PUT /providers/{name}`); the harness serves the provider at once, so a `mainplane new` right after it finds it
- `POST /sessions` with a model the harness cannot serve says why: not `provider/model`, an unknown provider (with the supported list), or a provider with no key (with the `mainplane key` line to set it)
- The model sees time and context: the system prompt states the session's start time and context limit, and every tool result ends with `time 3m12s context 48210`, the time since session start and the prompt tokens of the step that made the call. Both are built from the file, so the same file builds the same request bytes
- The system prompt and `cdp.md` are shorter and clearer. The system prompt says what the time and context note on each tool result means, and that a cut read keeps its head
- A stop's error says who stopped the session, and a retry of a failed session under its context limit adds the message `Continue` from `retry`, so the model goes on instead of stopping
- The worker list starts with `Workers:`, and with `Workers (changed):` when it changes mid-session. The AGENTS.md scan starts with one line, and the session line is only the session id, start time and context limit
- `mainplane workers <id> <name>...` (`PUT /sessions/{id}/workers`) changes a session's workers: it appends a config record with the new list and `via`, and nothing else in the config changes. The next step lists the workers to the model as a change, so an agent that adds a worker uses it in the same session
- `mainplane-server install` writes `AGENTS.md` in `admin`'s scratch when none is there: that an agent with `admin` acts as a Mainplane admin, that the CLI there is logged in with a full key, how to change a session's workers, and where the harness config and logs are
- `mainplane chat` with no id follows the session updated last
- The Gemini provider is `google`, as in `google/gemini-...` model strings (models.dev, OpenRouter), in the config and in step records; its key still comes from `GEMINI_API_KEY` at install. A config that names `gemini` must name `google` instead, or the harness does not start
- Shorter output: the harness install no longer prints its URL, a quick tunnel warning or the config path, `mainplane status` no quick tunnel warning, `mainplane login` says `logged in`, and the uninstalls say only that they uninstalled
- `mainplane uninstall` logs the worker out of its harness as the worker stops, so the machine installed again joins under its own name, not `<name>-2`
- The harness checks cloudflared against its pinned sha256 at every start, not only at download, and downloads it again when it differs
- The worker install no longer warns that a worker on the harness's machine can read the harness secrets
- A session's `run` on `harness` says it is not a worker, and that the worker on the harness's machine is `admin`
- Drives: `"drives": {"proj": {"server": "linuxbox", "workers": ["admin", "laptop"]}, "sessions": {"workers": ["judge-1"]}}` in the harness config, applied within seconds of a save. A drive's server is a Linux worker, Debian or Ubuntu; `"*"` is every persistent worker, and an ephemeral one is named. `sessions` is the harness's `sessions/`, served by `admin` on a Linux harness, read-only, named workers only. A drive that cannot be served is an error in the harness log
- A drive's server serves it over NFSv4 from `/srv/mainplane/<name>`, each client by its mesh address, and every client acts as the server's operator, so what any worker writes is the operator's. NFSv4 only, with a lease and a grace of 20 s, so a dead client's locks go, and a restarted server takes new locks, after 20 s, not 90. The first drive installs `nfs-kernel-server`. It refuses a machine that already exports something over NFS or has a program on port 445, in `mainplane status` and the harness log
- A Linux worker mounts each of its drives at `/drives/<name>`, NFSv4 hard, its server's own from its disk. A server out of reach is waited for. Drives stay mounted across restarts and updates, and are unmounted when the worker leaves the drive or the mesh, or is uninstalled
- A Windows worker maps each of its drives over SMB3 in the operator's logon session, so it shows in Explorer under This PC as `<name> (W:)`: a letter of its own from `W:` down, kept across restarts, at `\\<server>--home.mainplane.net\<name>`. It signs in as `mp-<worker>`, one user per server, with a password the harness derives from its key and sends to both ends; `cmdkey` keeps it in the operator's credential manager. With the operator logged out, the drives wait, and so does a new drive until its server has the user. A drive with open files stays mapped and says so; leaving the mesh or uninstalling closes them
- A macOS worker mounts each of its drives at `/Volumes/<name>`, where Finder lists it as `<name>` on the server `<server>--home.mainplane.net`, NFSv4.0 hard, with no credential: what it writes is the server operator's, shown by uid, and the Mac's operator can write. Mounted as on Linux: waits for a server out of reach, stays across restarts, goes when the worker leaves the drive or the mesh, or is uninstalled. Finder and `xattr` leave `._` files on the drive, since the server keeps no extended attributes
- A drive's server also runs an smbd of its own, for Windows workers, `mainplane-smbd`, with its config, passwords and state in `/var/lib/mainplane/smb` and a nologin system user per Windows client. It listens on port 445 and the kernel lets in only the mesh; Samba's own `smbd` and `nmbd`, which the first drive installs with `samba`, stay off. Byte-range locks are fcntl locks and oplocks kernel leases, so Windows and Linux clients see each other's locks and writes; a Windows deny-write open does not stop a Linux writer. A Windows worker removed from a drive loses its connection to the share at once, open files with it. Serving no drive, leaving the mesh and uninstalling remove it all but the package
- A drive's server sees each worker that mounts it; two workers on one drive do not see each other for it
- `mainplane status`, `mainplane workers` and a session's worker list show each drive's path and state; the session's list marks a drive its worker serves. A session's config no longer lists drives
- Every interpreter has `GIT_CONFIG_*` set so git trusts repositories on a drive, whose files are the server operator's
- The system prompt says how to use drives: the path on each OS, appends under a lock or one file per message, fcntl locks, never flock, SQLite without WAL, names Windows can open, build output in scratch, listings that lag, waits while a server is away, and that the sessions drive is read-only. "Admin Drive" is gone from it
- Known: on a drive's server, `flock(1)` on the drive does not see other workers' locks (knfsd holds them as POSIX locks); fcntl locks do. On Windows a file made on another worker can read as missing for about 5 s
- Mesh: a worker lowers the MSS of every TCP SYN to and from its peers to 1140, and cuts a longer IPv6 packet into fragments of at most 1200 bytes for a peer that a full-size probe does not reach, so the mesh works on a path of only 1280 bytes (a VPN, WSL2 behind a Tailscale adapter), where full packets vanished and drives hung: Windows uploads (Windows ignores the MSS), UDP and ICMP too. The harness reassembles IPv6 fragments (fork `v1.102.4-mainplane.8`)
- Mesh: netcheck no longer logs `named node "harness" has no v4 address` and `no v6 address` every round, in the harness log and each worker's (fork `v1.102.4-mainplane.7`)
- Every worker has a `js` interpreter: a pinned Bun 1.4.2 in `<scratch>/js`, one process per session like the shells, with `cdp.js`, a Chrome DevTools Protocol client that logs every message, and `cdp.md`
- Providers: no headers or no body bytes for 300 s is an error, and so is a stream that ends before its last event; both are retried while no block is out. The error a stop appends tells the model to continue, where it said `stopped via <via>`
- Sessions: a post to `/sessions/{id}/records` may carry an `Idempotency-Key` header. A key the session already holds appends nothing and returns the `n` of that post

## v0.4.0

Workers no longer see each other by default: each sees only the harness. To let two reach each other, name them in the harness config: `"links": [["alexanders-mac-mini", "linuxbox"]]`.

- Links and provider keys in the harness config apply within seconds of a save; the rest of the config at the next start
- The harness installs with no provider key set; it says where to add one
- A worker installed on the harness's machine by an admin warns that it can read and modify the harness secrets
- A new worker asks the pointer again after 5s, then 10s, up to a minute, where it waited a minute: a worker installed right after its harness joins in seconds
- The CLI on a worker calls its harness over the mesh, off the tunnel, when the worker follows the harness it is logged in to. The harness serves the API on its mesh node at port 8080
- `mainplane status` on a worker whose harness does not answer says so, with its URL, and what to do if the harness was uninstalled or installed again
- The harness install prints a join token named `default`, good for any number of machines, and the lines that install a worker with it on Linux, macOS and Windows. Each install makes a new one; the one before joins no more machines. The name in `mainplane-server join new <name>` is the secret's, for revoke; a worker is named by its hostname
- Installs print less: no checksum lines, a spinner while the harness and its tunnel start, then a wordmark, the version, and what to do next
- Sessions: `context` in a create body and in the config record is `context_limit`, and `prompt` in session info is `context_used`. A session made before reads a limit of 0 and fails at its next step: start a new one
- Sessions: `params` in a create body sets vendor fields on every request, as a JSON merge patch in the vendor's own names: `{"output_config":{"effort":"high"}}` for Anthropic, `{"reasoning":{"effort":"high"}}` for OpenAI, `{"provider":{"order":["deepinfra"]}}` for OpenRouter. The vendor judges them. A field the harness builds from the session, such as `messages` or `tools`, fails the create. A copy keeps the params
- Providers send only the fields the harness builds from the session; every model setting comes from `params`. Anthropic and Bedrock no longer get a default `max_tokens` (`maxTokens`) or thinking, OpenAI no reasoning effort, Gemini no thinking config. Anthropic requires `max_tokens`, so a session without it fails at its first step with Anthropic's error. Example for Opus: `{"max_tokens":16384,"thinking":{"type":"adaptive"}}`; for Haiku 4.5: `{"max_tokens":16384}`
- Step records carry `sent`, when the request went out. The `cache` header keeps `marks` and has `ttl` only for Anthropic and Bedrock, whose lifetime the request fixes, counted from `sent`
- `mainplane status` starts with the harness the CLI is logged in to: its version and URL. The worker's own row says `harness unresponsive` where it said `map poll down`
- `mainplane status` on a machine with no worker says `this machine is not a worker`, and exits 0
- `mainplane login` says `logged in to mainplane-server at <url>`
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
