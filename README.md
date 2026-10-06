<p align="center">
  <img src="docs/assets/mainplane-lockup.svg" alt="Mainplane" width="480">
</p>

Mainplane is an open-source agent harness built for performance at scale. Let hundreds of agents drive the devices you actually work on, while secrets and controls stay safely isolated.

## Quickstart

**1. Install mainplane-server** on any machine.

```sh
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- server
```
```powershell
# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) server
```

It prints a join token (`mp_join_...`) and an API key (`mp_key_...`), in the lines for steps 2 and 3. The machine is also your first worker, `admin`.

**2. Optional: connect more machines.** Your agents can use them.

```sh
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- mp_join_...
```
```powershell
# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) mp_join_...
```

**3. Log in from anywhere and start a session.** A logged-in machine can message agents.

```sh
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- mp_key_...
```
```powershell
# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) mp_key_...
```

Add an LLM key (`openai`, `anthropic` or `google`), then start a session on `admin`:

```sh
mainplane key openai sk-...
echo '{"model":"openai/gpt-6.1-sol","context_limit":200000,"workers":[{"name":"admin"}],"params":{"reasoning":{"effort":"medium","summary":"auto"}}}' | mainplane new
mainplane chat
```

`mainplane workers` lists the names of your workers, to add to `workers`. Each step can run again: it says when a machine is already a worker or already logged in. More in [docs/self-hosting.md](docs/self-hosting.md).

## FAQ

<details>
<summary>Do I need more than one device?</summary>

No. Everything can run on one device.
</details>

<details>
<summary>Does mainplane-server need Linux?</summary>

No. mainplane-server runs on macOS, Windows and Linux.
</details>

<details>
<summary>Do you take contributions?</summary>

We welcome feedback as GitHub issues. Pull requests are open to collaborators only.
</details>
