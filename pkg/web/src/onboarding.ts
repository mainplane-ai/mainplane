// Onboarding is the terminal app's quickstart for a new browser, as Apple's Setup Assistant: one step on the
// screen at a time, buttons only. Welcome, log in, a provider, then workers, another device and a drive, each
// optional. It keeps only that it ran: the harness's state is the progress.

import { api, get, h, icon, key } from "./api"
import { providersPanel } from "./providers"
import lockup from "./lockup.svg"

const ran = "mainplane-onboarded"
export const onboarded = () => !!localStorage.getItem(ran)

type Worker = { name: string; refused?: string; drive_server?: boolean }
type Step = "welcome" | "login" | "provider" | "sharing" | "workers" | "device" | "drive"

const script = "https://mainplane.ai/install"
const lines = (args: string): [string, string, string][] => [
  ["macOS, Linux", "curl -fsSL " + script + " | sh -s -- ", args],
  ["Windows", "& ([scriptblock]::Create((irm " + script + ".ps1))) ", args],
]

// install is the one-line install with its OS picked, the secret blurred until pointed at, and Copy.
function install(args: string): HTMLElement {
  const ls = lines(args)
  const code = h("code")
  const copy = h("button", { className: "btn plain sm", type: "button" }, icon("copy"), h("span", null, "Copy"))
  const seg = h("div", { className: "segmented fill", role: "radiogroup" }, ...ls.map(([os], i) =>
    h("button", { type: "button", role: "radio", ariaChecked: String(i === (/Win/.test(navigator.platform) ? 1 : 0)), onclick: () => pick(i) } as Partial<HTMLButtonElement>, os)))
  const pick = (i: number) => {
    seg.querySelectorAll("button").forEach((b, j) => (b.ariaChecked = String(i === j)))
    code.replaceChildren(ls[i][1], h("span", { className: "secret" }, ls[i][2]))
    copy.onclick = () => navigator.clipboard.writeText(ls[i][1] + ls[i][2]).then(() => (copy.lastElementChild!.textContent = "Copied"))
    copy.lastElementChild!.textContent = "Copy"
  }
  pick(/Win/.test(navigator.platform) ? 1 : 0)
  return h("div", { className: "install" }, seg, h("pre", null, code), h("div", { className: "actions" }, copy))
}

export function onboarding(loginForm: (done: () => void) => HTMLElement, done: () => void): HTMLElement {
  const root = h("div", { className: "onboard" })
  let step: Step = key() ? "provider" : sessionStorage.getItem("refused") ? "login" : "welcome"
  let providers = -1 // how many were connected at the last poll; -1 before the first
  let workers: Worker[] = []
  let network = "", join = "", minted = ""
  let sharing = false // the harness has not been asked to share data yet

  const servers = () => workers.filter(w => !w.refused && w.drive_server).map(w => w.name)
  const order = (): Step[] => ["welcome", ...(key() ? [] : ["login" as const]), "provider", ...(sharing ? ["sharing" as const] : []), "workers", "device", ...(servers().length ? ["drive" as const] : [])]
  const move = (d: number) => {
    const o = order(), i = o.indexOf(step)
    if (i + d >= o.length) return finish()
    step = o[Math.max(0, i + d)]
    draw()
  }
  const finish = () => { localStorage.setItem(ran, "1"); done() }

  // page is one step: its title and words in the middle, then at the foot any other choice over Back and the
  // step's own button, side by side and the same size (Apple's Setup Assistant).
  const page = (title: string, words: string, body: Node[], next: HTMLElement, ...more: HTMLElement[]) => root.replaceChildren(
    h("div", { className: "ob-step" }, h("h1", null, title), ...(words ? [h("p", null, words)] : []), ...body), foot(next, ...more))
  const foot = (next: HTMLElement, ...more: HTMLElement[]) => h("div", { className: "ob-foot" }, ...more,
    h("div", { className: "ob-row" }, h("button", { className: "btn lg", type: "button", onclick: () => move(-1) }, "Back"), next))
  const button = (label: string, onclick: () => void, filled = true) =>
    h("button", { className: "btn lg " + (filled ? "filled" : "plain"), type: "button", onclick }, label)
  const fail = (e: unknown) => alert(e instanceof Error ? e.message : e)

  let live: (() => void) | null = null // redraws the live part of the step in view after a poll
  const draw = () => {
    live = null
    root.closest("main")?.scrollTo(0, 0)
    switch (step) {
      case "welcome": {
        root.replaceChildren(h("div", { className: "ob-step welcome" }, Object.assign(h("span", { className: "mark" }), { innerHTML: lockup })),
          h("div", { className: "ob-foot" }, button("Get Started", () => move(1))))
        return
      }
      case "login": {
        const form = loginForm(() => { step = "provider"; draw(); void poll() })
        root.replaceChildren(form, foot(button("Log In", () => (form as HTMLFormElement).requestSubmit())))
        return
      }
      case "provider": {
        const next = button("Continue", () => move(1))
        live = () => (next.disabled = providers < 1)
        page("Connect a Model Provider", "Your agents think with its models. Connect one to begin.", [providersPanel()], next)
        break
      }
      case "sharing": {
        const set = (share: boolean) => api("/settings", { json: { data_sharing: { share, via: "web", text: "b1" } } }).then(() => { sharing = false; step = "workers"; draw() }, fail)
        page("Help Improve Mainplane", "Allow the use of your sessions to improve Mainplane and train AI models. You can change this in Settings.", [],
          button("Allow", () => set(true)), button("Don't Allow", () => set(false), false))
        break
      }
      case "workers": {
        const lines = h("div"), box = h("div")
        const next = button("Continue", () => move(1))
        const later = button("Set Up Later", () => move(1), false)
        live = () => {
          const ws = workers.filter(w => w.name)
          if (join && !lines.firstChild) lines.append(install(join)) // the lines stay put while the workers under them change
          box.replaceChildren(
            ...(network && network !== "ready" ? [h("p", { className: "note" }, network === "starting" ? "The network is starting. The lines work once it is ready." : "The network failed: " + network)] : []),
            ...(ws.length ? [h("p", { className: "section-l" }, "Connected"), h("div", { className: "form" }, ...ws.map(w =>
              h("div", { className: "form-row" }, icon("computer"), h("span", { className: "row-main" }, h("b", null, w.name), ...(w.refused ? [h("small", null, "Refused: " + w.refused)] : [])))))] : []))
          next.disabled = !ws.some(w => !w.refused)
          later.hidden = !next.disabled
        }
        page("Connect Workers", "A worker is a machine your agents use. Run this one-line install on a machine to connect it.", [lines, box], next, later)
        break
      }
      case "device": {
        const box = h("div", null, h("p", { className: "note" }, minted ? "" : "Making an API key..."))
        // The login link as a QR code for a phone's camera, and as text to copy, its key blurred until pointed at.
        const show = async () => {
          const at = location.origin + "/#k=", link = at + encodeURIComponent(minted)
          const { renderSVG } = await import(new URL("qrcode.js", import.meta.url).href) as { renderSVG: (s: string, o?: { pixelSize?: number }) => string }
          const qr = Object.assign(h("div", { className: "qr" }), { innerHTML: renderSVG(link, { pixelSize: 4 }) })
          const copy = h("button", { className: "btn plain sm", type: "button" }, icon("copy"), h("span", null, "Copy Link"))
          copy.onclick = () => navigator.clipboard.writeText(link).then(() => (copy.lastElementChild!.textContent = "Copied"))
          box.replaceChildren(qr, h("div", { className: "install" }, h("pre", null, h("code", null, at, h("span", { className: "secret" }, encodeURIComponent(minted)))), h("div", { className: "actions" }, copy)))
        }
        page("Use Mainplane on Another Device", "Your projects and chats are on every device logged in to this harness. Scan this code with a phone's camera, or open the link in another browser.",
          [box], button("Continue", () => move(1)))
        if (minted) void show()
        else mint().then(show).catch(e => box.replaceChildren(h("p", { className: "note" }, "Could not make the login link: " + (e instanceof Error ? e.message : e))))
        break
      }
      case "drive": {
        const make = button("Create Drive", () => {
          const s = servers()
          make.disabled = true
          api("/drives/shared", { method: "PUT", json: { server: s.includes("admin") ? "admin" : s[0], workers: ["*"] } }).then(finish, e => { make.disabled = false; fail(e) })
        })
        page("Host a Shared Drive", "Mainplane can host files shared among all your workers. Add a shared drive for all workers?", [], make, button("Not Now", finish, false))
        break
      }
    }
    live?.()
  }

  // mint makes the API key for another device: the first web-N free, as the terminal app makes tui-N.
  let minting: Promise<void> | null = null
  const mint = () => (minting ??= (async () => {
    for (let i = 1; ; i++) {
      const r = await fetch("/keys", { method: "POST", headers: { Authorization: "Bearer " + key() }, body: JSON.stringify({ name: "web-" + i }) })
      if (r.status === 409) continue
      if (!r.ok) { minting = null; throw new Error((await r.text()).trim() || r.statusText) }
      minted = (await r.json()).key
      return
    }
  })())

  // poll reads what the steps show every second and a half while the onboarding is on screen. A provider
  // connected on its step moves on, as in the terminal app.
  let polling = false
  const poll = async () => {
    if (polling || !key()) return
    polling = true
    const st = await get<{ data_sharing?: unknown; version: { current: string } }>("/settings").catch(() => null)
    sharing = !!st && !st.data_sharing && st.version.current !== "dev"
    do {
      const [ps, ws, top, j] = await Promise.all([get<unknown[]>("/providers"), get<Worker[]>("/workers"), get<{ network: string }>("/"), get<{ join: string }>("/join")]).catch(() => [null, null, null, null])
      if (ps) {
        const more = providers >= 0 && ps.length > providers
        ;[providers, workers, network, join] = [ps.length, ws!, top!.network, j!.join]
        if (more && step === "provider") move(1)
        else live?.()
      }
      await new Promise(t => setTimeout(t, 1500))
    } while (root.isConnected)
    polling = false
  }

  draw()
  void poll()
  return root
}
