# Self-hosting

```
 worker, CLI --https--> Cloudflare --> cloudflared --> 127.0.0.1:8080 harness
                 |
                 +-- pointer.mainplane.ai: harness key -> current URL, signed by the key
```

The harness opens no inbound port. It runs cloudflared as a child, and
cloudflared carries one public HTTPS URL to the harness's loopback port. Tokens
carry the harness key, not the URL. A worker or the CLI finds the URL through
`pointer.mainplane.ai`, and the harness proves its key there before a secret
goes to it.

## The temporary URL

`mainplane-server install` opens a Cloudflare quick tunnel: no account, no
domain, a name like `https://four-random-words.trycloudflare.com`.

- The URL holds across harness restarts while Cloudflare keeps the tunnel,
  about 10 minutes after its last connection. After a longer outage the
  harness gets a new URL and publishes it. Workers usually find it about a
  minute after the new tunnel starts.
- Cloudflare offers quick tunnels for testing and development, with no SLA.
- A quick tunnel carries at most 200 requests at once. Each worker holds two
  open (its map poll and its relay connection), and each `mainplane chat`
  one, so about 95 workers fit. For more, use your own domain.

## Your own domain

1. In the Cloudflare dashboard, open Zero Trust, Networks, Tunnels, and create
   a tunnel of type cloudflared. Copy its token from the install command it
   shows; do not run that command.
2. On the tunnel, add a public hostname, such as `harness.example.com`, with
   service `HTTP` and URL `localhost:8080`.
3. On the harness machine:

   ```
   mainplane-server tunnel https://harness.example.com <token>
   ```

The harness restarts on the new URL and publishes it. Workers and the CLI
follow, usually in about a minute, with no new tokens. The token is stored in
the harness config, which only root can read. `mainplane-server tunnel quick`
goes back to a quick tunnel.

## The mesh

```
 worker A (mainplaned, root)                harness (mainplane-server, no root)
 TUN: mainplane0 / Mainplane / utunN        coordinator, relay: behind the tunnel
 fd7c:9a2e:4b10::N, UDP 41642               own node "harness", userspace
 hosts block: names -> addresses            control on [harness]:7000, mesh only
        \______ WireGuard, direct or through the relay ______/
```

Every worker joins a WireGuard mesh that the harness coordinates. The worker
gets an interface of its own (`mainplane0` on Linux, `Mainplane` on Windows,
`utunN` on macOS), one IPv6 address in `fd7c:9a2e:4b10::/48`, and a block in
the hosts file with a short and a long name for each peer it may reach:
`linuxbox` and `linuxbox--home.mainplane.net`. The harness joins the same mesh
in userspace, as the node `harness`, and workers reach it at port 7000 there.
Nothing listens on that port outside the mesh.

- Who sees whom: the harness sees every worker and every worker sees the
  harness. Workers joined with a normal join secret, your own machines, see
  each other. Workers joined with an ephemeral one
  (`mainplane-server join new <name> ephemeral`), for fleets, see only the
  harness. A peer outside a worker's map is not in its hosts block, and
  WireGuard drops its packets at both ends.
- An ephemeral worker leaves the mesh 3 minutes after it goes quiet.
- `mainplane worker remove <name>` takes a worker off the mesh within seconds:
  its peers lose it and its name, and the worker takes down its interface and
  hosts block. Its keys stay refused. To make it a worker again, run
  `mainplane uninstall` on it, then install again. `mainplane-server join
  revoke` refuses new joins with that secret, and leaves the workers that
  joined with it.
- Paths: two workers talk direct, UDP to UDP, when their NATs let them. This
  is usual on one LAN and common across the internet. When they cannot, their
  traffic goes through the relay in the harness, through the tunnel: it works,
  but is slower (about 40 ms more through a quick tunnel), and stops when the
  tunnel is down. The mesh keeps trying and moves to a direct path when one
  opens. `mainplane status` on a worker shows its name and address, its
  harness, and for each peer `direct <endpoint>`, `relay harness`, or `idle`
  (no traffic of late; the path is found when traffic starts). No
  inbound port is needed; a firewall that drops outbound UDP forces the relay.
- Root: the worker needs root or admin for its interface, routes and hosts
  file. The harness needs neither.
- Names and IPv6 only: a program another worker should reach must listen on
  `::`, not `0.0.0.0`, or the other worker gets "connection refused". Use
  names, not addresses.
- Tailscale on the same machine keeps working. The mesh has its own
  interface, route, UDP port and state, and changes no DNS settings. A mesh
  short name hides the same name from Tailscale's MagicDNS on that machine
  (the hosts file answers first). On Windows it is the other way round for
  names in both: Tailscale writes its names into the hosts file and Windows
  prefers its IPv4 address, so use the long name there.
- Windows: `mainplane install` downloads `wintun.dll`, the WireGuard project's
  signed TUN driver (the same one Tailscale ships), from wintun.net, pinned to
  a sha256. A worker started by hand, `mainplane worker <token>`, needs
  `wintun.dll` beside `mainplane.exe`.

## What Cloudflare can read

TLS ends at Cloudflare, in both modes. Cloudflare can read all traffic between
connectors and the harness: api keys, prompts, command output, and files. It
cannot read the mesh: a join secret goes to the coordinator inside Noise, to a
key the harness key vouches for, and workers reach the harness and each other
inside WireGuard, relayed through the tunnel or direct. Through the relay it
sees how much encrypted traffic passes between which workers, and when. If
this is not acceptable, do not self-host through a Cloudflare tunnel.

The pointer stores only the harness key, the current URL, and a signature. It
cannot redirect workers: a record needs the harness key's signature, and a
worker sends no secret until the harness at the URL proves the key.
