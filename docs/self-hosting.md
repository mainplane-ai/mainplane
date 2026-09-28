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
  harness gets a new URL and publishes it. Workers find it within a minute of
  the new tunnel starting.
- Cloudflare offers quick tunnels for testing and development, with no SLA.
- A quick tunnel carries at most 200 requests at once. Each worker holds one
  open, and so does each `mainplane chat`. For more, use your own domain.

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

The harness restarts on the new URL and publishes it. Workers and the CLI follow
within a minute, with no new tokens. The token is stored in the harness config,
which only root can read. `mainplane-server tunnel quick` goes back to a quick
tunnel.

## What Cloudflare can read

TLS ends at Cloudflare, in both modes. Cloudflare can read all traffic between
workers, connectors, and the harness: api keys, join secrets, prompts, command
output, and files. If this is not acceptable, do not self-host through a
Cloudflare tunnel.

The pointer stores only the harness key, the current URL, and a signature. It
cannot redirect workers: a record needs the harness key's signature, and a
worker sends no secret until the harness at the URL proves the key.
