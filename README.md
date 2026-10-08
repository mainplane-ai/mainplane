<picture>
  <source media="(max-width: 700px), (hover: none) and (pointer: coarse)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/hero-mobile.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/hero-desktop.svg" alt="Mainplane, with the biplane flying over the clouds" width="960">
</picture>
<h3>Mainplane is an open-source agent harness built for performance at scale.</h3>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/architecture-dark.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/architecture-light.svg" alt="The Mainplane Architecture: connectors reach Mainplane Server, which holds secrets and controls, observability and the file server; the network joins the server and every worker" width="960">
</picture>
<br/><br/>

<a>
> Run hundreds of durable agents with computer/browser use <br/>
> Connect your own devices, your agents can drive them remotely <br/>
> Secrets and controls stay safely isolated  <br/>
> Chat from anywhere, everything syncs  <br/>
</a>

## Quickstart

**1. Install mainplane-server** on any machine.

```shell
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- server

# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) server
```

It prints commands with a join token (`mp_join_...`) and an API key (`mp_key_...`), save these.
<br/><br/>

**2. Optional: connect more machines.** Your agents can use them.

```shell
# Linux, macOS - use join token from step 1
curl -fsSL https://mainplane.ai/install | sh -s -- mp_join_...

# Windows - use join token from step 1
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) mp_join_...
```
<br/>

**3. Log in from anywhere and start a session.** A logged-in machine can message agents.
 
```shell
# Linux, macOS - use api key from step 1
curl -fsSL https://mainplane.ai/install | sh -s -- mp_key_...

# Windows - use api key from step 1
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) mp_key_...
```

Add an LLM key (`openai`, `anthropic` or `google`), then start a session on `admin`:

```shell
mainplane key openai sk-...
echo '{"model":"openai/gpt-6.1-sol","context_limit":200000,"workers":[{"name":"admin"}],"params":{"reasoning":{"effort":"medium","summary":"auto"}}}' | mainplane new
mainplane chat
```

More in [docs/self-hosting.md](docs/self-hosting.md).
<br/><br/>

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

<br/><br/>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/footer-dark.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/footer-light.svg" alt="A billboard turns through what Mainplane agents do, while the biplane tows a banner: Now you're flying!" width="960">
</picture>
