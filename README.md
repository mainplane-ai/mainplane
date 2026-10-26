<picture>
  <source media="(max-width: 700px), (hover: none) and (pointer: coarse)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/hero-mobile.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/hero-desktop.svg" alt="Mainplane, with the biplane flying over the clouds" width="960">
</picture>
<h3>The open-source agent ecosystem</h3>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/architecture-3d-dark.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/architecture-3d-light.svg" alt="The Mainplane Architecture: connectors reach Mainplane Server, which holds secrets and controls, observability and the file server; the network joins the server and every worker" width="960">
</picture>
<br/><br/>

# Self-Hosting Quickstart

Run this command on your server machine (any always-on machine will do):

```shell
# Linux, macOS
curl -fsSL https://mainplane.ai/install | sh -s -- server quickstart

# Windows
& ([scriptblock]::Create((irm https://mainplane.ai/install.ps1))) server quickstart
```

The quickstart walks you through adding a provider, workers (computers your agents control) and connectors (ways to message your agents).

Read more in [docs/self-hosting.md](docs/self-hosting.md).
<br/><br/>

# Features
### Always-on
- Mainplane turns any computers into an always-on local AI shed. Agents keep working when your laptop is closed.

### Bring your keys and subscriptions
- Use the regular limits of a Claude Code or ChatGPT Codex subscription, or an API key with any model.

### Chat from anywhere
- Use Mainplane's first-class TUI or the sessions API. All messages stay synced. 

### Private mesh network 
- Mainplane connects agents and computers with fine-grained security controls. No Tailscale needed.

### Shared file drives
- A remote filesystem that multiple agents can access concurrently without risk of de-sync. 

### Context and cost efficient
- Mainplane uses just 3k tokens of system context. Agents see their own context usage and optimize it. 

### Browser and computer use
- Mainplane drives real browsers with their logins and has native computer use.

### Control plane security
- API keys stay on your server isolated from the other workers where agents run.

### Durable and observable sessions
- Agent state = one readable file that can be resumed if stopped.

### Performant and open-source
- Mainplane runs as one Go binary, source code is under MIT license.


# FAQ

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

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/mainplane-ai/github-media/main/footer-still-dark.svg">
  <img src="https://raw.githubusercontent.com/mainplane-ai/github-media/main/footer-still-light.svg" alt="A billboard turns through what Mainplane agents do, while the biplane tows a banner: Now you're flying!" width="960">
</picture>
