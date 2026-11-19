// The page: a list of projects beside the one in view. A project is seen as
// its dashboard, with the place to write to its orchestrator always there.

import { type Agent, type Conf, type Model, type Project, acted, api, get, h, icon, initials, key, login, logout } from "./api"
import { chat } from "./chat"
import { composer } from "./composer"
import { dashboard } from "./dashboard"
import { picker } from "./picker"
declare const MPMenu: { open: (menu: HTMLElement, at: Element) => void }

const statuses = ["open", "waiting", "closed"]
// A project's status as a sidebar row shows it: open is the user's turn.
const rowClass: Record<string, string> = { open: "needs", waiting: "waiting", closed: "closed" }
const said: Record<string, string> = { open: "Needs you", waiting: "Waiting", closed: "Closed" }
// required are the apps every orchestrator has: the project app adds them whatever is asked.
const required = ["message", "orchestrate", "project", "schedule"]
const lastModel = "mainplane-model"

const search = h("input", { type: "search", placeholder: "Search" })
const list = h("div", { className: "list side separated", role: "listbox", ariaLabel: "Projects" })
// Pins are kept in this browser only. A phone shows them as circles above the list.
const pins = h("div", { className: "pins" })
const pinned = () => JSON.parse(localStorage.getItem("pins") ?? "[]") as string[]
const pin = (id: string) => localStorage.setItem("pins", JSON.stringify(pinned().includes(id) ? pinned().filter(x => x !== id) : [...pinned(), id]))
const side = h("nav", { className: "sidebar" },
  h("div", { className: "top" }, h("label", { className: "search lg" }, icon("search"), search), h("a", { href: "/new", className: "btn icon ghost", ariaLabel: "New project" }, icon("compose"))),
  h("h1", { className: "large-title" }, "Mainplane"),
  pins,
  list,
  h("div", { className: "foot" }, h("button", { className: "btn plain sm", onclick: () => logout() }, "Log out")))
const main = h("main")
document.body.classList.add("mp")
document.body.append(side, main)

// A login link carries the key after the #, which no server sees. It is
// saved and taken out of the address bar.
const linked = location.hash.match(/^#k=(.+)$/)
if (linked) {
  login(decodeURIComponent(linked[1]))
  history.replaceState(null, "", location.pathname)
}

let projects: Project[] = []

// when is a time today, else a day.
const when = (t: string) => {
  const d = new Date(t)
  return d.toDateString() === new Date().toDateString() ? d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
}

// drawList shows the projects that match the search, the user's turn first.
function drawList() {
  const q = search.value.trim().toLowerCase()
  const at = location.pathname.split("/")[2]
  const rows = statuses.flatMap(s => projects.filter(p => p.status === s && (!q || p.name.toLowerCase().includes(q)))).map(p => {
    const line = h("small", null, p.line)
    if (p.working) line.prepend(h("span", { className: "typing inline", role: "status", ariaLabel: "Working" }, h("i"), h("i"), h("i")))
    return h("a", { href: `/projects/${p.id}`, className: `row ${p.working ? "working" : rowClass[p.status]}${pinned().includes(p.id) ? " pinned" : ""}`, role: "option", ariaSelected: String(p.id === at), ariaDescription: p.working ? "Working" : said[p.status] },
      h("span", { className: "u" }), h("span", { className: "avatar" }, initials(p.name)),
      h("span", { className: "row-main" }, h("span", { className: "h" }, h("b", null, p.name), h("span", { className: "row-trail" }, when(p.updated))), line))
  })
  list.replaceChildren(...rows)
  // A pin whose project needs you shows its line in a bubble, as Messages shows an unread message.
  pins.replaceChildren(...pinned().flatMap(id => projects.filter(p => p.id === id)).map(p =>
    h("a", { href: `/projects/${p.id}`, className: `pin ${p.working ? "working" : rowClass[p.status]}` },
      ...(p.status === "open" && p.line ? [h("span", { className: "peek" }, p.line)] : []),
      h("span", { className: "avatar" }, initials(p.name)), h("small", null, h("span", { className: "u" }), p.name))))
}
search.oninput = drawList

// refresh reads the projects and draws the list.
async function refresh() {
  if (!key()) return
  projects = await get<Project[]>("/projects") // the API lists the last updated first
  drawList()
  dispatchEvent(new CustomEvent("projects", { detail: projects }))
}
// quietly does so while the page is seen. One that fails is left: the next may reach the harness.
const quietly = () => document.visibilityState === "visible" && void refresh().catch(() => {})
setInterval(quietly, 5000)
document.addEventListener("visibilitychange", quietly)
addEventListener("acted", quietly)

// view ends with what is in view: its listeners, its calls and its long poll.
let view = new AbortController()

function go(path: string) {
  history.pushState(null, "", path)
  void render()
}
addEventListener("popstate", () => void render())
// A click on a link of the page's own changes the view. Nothing is loaded.
document.addEventListener("click", e => {
  const a = e.target instanceof Element && e.target.closest("a")
  if (!a || a.origin !== location.origin || e.metaKey || e.ctrlKey || e.shiftKey) return
  e.preventDefault()
  go(a.pathname)
})

// attempt runs what a form or a button does, and shows what went wrong.
const attempt = (fn: () => Promise<unknown>) => (e: Event) => {
  e.preventDefault()
  fn().catch(alert)
}

async function render() {
  view.abort()
  const signal = (view = new AbortController()).signal
  const [kind, id, sub] = location.pathname.split("/").slice(1)
  document.body.classList.toggle("out", !key())
  document.body.classList.toggle("home", location.pathname === "/" || location.pathname === "/new") // a phone shows the list under the new project sheet
  drawList()
  try {
    const nodes = await content(kind, id, sub, signal)
    if (!signal.aborted) main.replaceChildren(...nodes) // a slow one must not draw over the view that came after it
  } catch (err) {
    if (!signal.aborted) main.replaceChildren(toolbar("Mainplane", "", null), h("p", { className: "error-line" }, String(err)))
  }
}

// toolbar heads a view: back (on a phone), the name and its line, then its buttons.
// With a menu, the name is a button that opens it: on a phone it is the title pill, the only way to the views.
function toolbar(name: string, line: string, menu: HTMLElement | null, ...buttons: HTMLElement[]): HTMLElement {
  const title = menu
    ? h("button", { className: "title island", ariaHaspopup: "menu", onclick: () => MPMenu.open(menu, title) } as Partial<HTMLButtonElement>, h("b", null, name), h("small", null, line))
    : h("span", { className: "title" }, h("b", null, name), h("small", null, line))
  return h("div", { className: "toolbar" },
    h("a", { href: "/", className: "btn icon ghost back", ariaLabel: "Projects" }, icon("chevron-left")),
    h("span", { className: "avatar" }, initials(name)), title, ...(menu ? [menu] : []),
    h("span", { className: "spacer" }), h("span", { className: "tb-group" }, ...buttons))
}

async function content(kind: string, id: string, sub: string, signal: AbortSignal): Promise<Node[]> {
  if (!key()) return [loginForm()]
  if (!projects.length) await refresh()
  const none = h("div", { className: "view" }, h("div", { className: "empty" }, icon("grid"), h("h3", null, "No project open"), h("p", null, "Pick one from the list, or make a new one.")))
  if (kind === "new") return [none, await createForm()]
  if (kind === "agents" && id) {
    const a = await get<Agent>(`/agents/${id}`, signal)
    const name = a.name || a.id
    const at = `/agents/${a.id}`
    const head = toolbar(name, `${a.status} · ${a.config.model}`, null, h("a", { href: at + "/settings", className: "btn icon ghost", ariaLabel: "Instructions" }, icon("more-vertical")))
    const view = [head, ...chat(id, signal), composer(id, `Message ${name}`)]
    if (sub === "settings") return [...view, sheet("Agent", name, at, [instructions(a)], [h("a", { href: at, className: "btn" }, "Done")])]
    return view
  }
  if (kind !== "projects") return [none]
  const p = projects.find(p => p.id === id)
  if (!p) throw new Error(`No project ${id}`)
  const at = `/projects/${p.id}`
  // The agents button counts the project's agents; the count comes when the list does.
  const count = h("span", { className: "count" })
  const agents = h("a", { href: at + "/agents", className: "btn ghost agents", ariaLabel: "Agents" }, icon("people"), count)
  void get<Agent[]>(at + "/agents", signal).then(l => (count.textContent = String(l.length)), () => {})
  const more = h("a", { href: at + "/settings", className: "btn icon ghost", ariaLabel: "Settings" }, icon("more-vertical"))
  const item = (name: string, label: string, act: (e: Event) => void, cls = "") => h("button", { role: "menuitem", className: cls, onclick: act } as Partial<HTMLButtonElement>, icon(name), h("span", null, label))
  const menu = h("div", { className: "menu", role: "menu", hidden: true },
    item("grid", "Dashboard", () => go(at)), item("message", "Chat", () => go(at + "/chat")), item("people", "Agents", () => go(at + "/agents")), item("settings", "Settings", () => go(at + "/settings")),
    h("hr"), item("pin", pinned().includes(p.id) ? "Unpin" : "Pin", () => { pin(p.id); drawList(); menu.querySelector(".pin-item span")!.textContent = pinned().includes(p.id) ? "Unpin" : "Pin" }, "pin-item"),
    item("trash", "Delete", attempt(() => removeProject(p)), "danger"))
  const head = toolbar(p.name, line(p), menu, agents, more)
  addEventListener("projects", e => (e as CustomEvent<Project[]>).detail.filter(q => q.id === p.id).forEach(q => (head.querySelector(".title small")!.textContent = line(q))), { signal })
  const tab = (path: string, label: string, name: string) => h("button", { role: "tab", ariaSelected: String(sub === name), onclick: () => go(path) }, label)
  const tabs = h("div", { className: "tabs", role: "tablist" }, tab(at, "Dashboard", ""), tab(at + "/chat", "Chat", "chat"))
  const write = composer(p.orchestrator, `Message ${p.name}`)
  if (sub === "chat") return [head, tabs, ...chat(p.orchestrator, signal), write]
  if (sub === "agents") {
    const list = await get<Agent[]>(at + "/agents", signal)
    return [head, tabs, h("div", { className: "view" }, h("div", { className: "page" }, h("h2", null, "Agents"), h("div", { className: "list grouped" }, ...list.map(a =>
      h("a", { href: `/agents/${a.id}`, className: `row ${a.status}` }, h("span", { className: "avatar" }, initials(a.name || a.id)), h("span", { className: "row-main" }, h("b", null, a.name || a.id), h("small", null, `${a.status} · ${a.config.model}`)), icon("chevron-right")))))), write]
  }
  const dash = [head, tabs, dashboard(p, signal), write]
  if (sub === "settings") return [...dash, await settings(p, signal)]
  return dash
}

const line = (p: Project) => [p.working ? "Working" : said[p.status], p.line].filter(Boolean).join(" · ")

function loginForm(): HTMLElement {
  const k = h("input", { className: "input lg", type: "password", required: true, placeholder: "API key", autocomplete: "current-password" })
  const form = h("form", { className: "page" }, h("h2", null, "Mainplane"), h("p", null, sessionStorage.getItem("refused") ? "The harness refused that key." : "Enter this harness's API key, or open the link that `mainplane web` prints."), k, h("div", { className: "actions" }, h("button", { className: "btn filled" }, "Log in")))
  form.onsubmit = e => {
    e.preventDefault()
    sessionStorage.removeItem("refused")
    login(k.value)
    void render()
  }
  return form
}

// sheet is a dialog over the view, its buttons in its foot. Close goes back
// to where it was opened. What floats over it (a picker) goes in the scrim.
function sheet(title: string, sub: string, close: string, body: Node[], foot: Node[], ...over: Node[]): HTMLElement {
  const s = h("div", { className: "scrim", onclick: e => e.target === s && go(close) },
    h("div", { className: "sheet", role: "dialog", ariaModal: "true", ariaLabel: title },
      h("header", null, h("hgroup", null, h("h2", null, title), h("p", null, sub)), h("a", { href: close, className: "btn icon ghost", ariaLabel: "Close" }, icon("close"))),
      h("div", { className: "body" }, ...body), h("footer", null, ...foot)), ...over)
  addEventListener("keydown", e => e.key === "Escape" && s.isConnected && go(close))
  return s
}

const row = (label: string, help: string, control: HTMLElement, tag: "label" | "div" = "label") =>
  h(tag, { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, label), help ? h("small", null, help) : ""), control)
const section = (label: string, ...rows: HTMLElement[]) => [h("p", { className: "section-l", hidden: !rows.length }, label), h("div", { className: "form", hidden: !rows.length }, ...rows)]

// options is the choice of an orchestrator's model, workers and apps: for a
// new project everything connected, for one there is what its orchestrator has.
async function options(cur?: Agent): Promise<{ el: HTMLElement[]; over: HTMLElement[]; value: () => { config: Conf; apps: string[] } }> {
  const [models, workers, apps] = await Promise.all([get<Model[]>("/models").catch(() => []), get<{ name: string; refused?: string }[]>("/workers"), get<{ name: string }[]>("/apps")])
  const pk = picker(models, cur?.config, localStorage.getItem(lastModel))
  const box = (value: string, checked: boolean, disabled = false) => row(value, disabled ? "Every orchestrator has it." : "", h("input", { type: "checkbox", className: "switch", role: "switch", value, checked, disabled }))
  const ticked = (set: HTMLElement) => [...set.querySelectorAll<HTMLInputElement>("input:checked")].map(i => i.value)
  const names = new Set([...workers.filter(w => !w.refused).map(w => w.name), ...(cur?.config.workers.map(w => w.name) ?? [])])
  const ws = section("Workers", ...[...names].map(n => box(n, !cur || cur.config.workers.some(w => w.name === n))))
  const as = section("Apps", ...apps.map(a => (required.includes(a.name) ? box(a.name, true, true) : box(a.name, !cur || cur.apps.includes(a.name)))))
  return {
    el: [h("div", { className: "form" }, row("Model", "Plans the work and talks with you.", pk.trigger, "div")), ...ws, ...as],
    over: pk.pops,
    value: () => {
      const config = pk.value()
      if (!config) throw new Error("No model is connected: add a provider to the harness first.")
      if (config.preset) localStorage.setItem(lastModel, config.preset)
      return { config: { ...config, workers: ticked(ws[1]).map(name => ({ name })) }, apps: ticked(as[1]) }
    },
  }
}

async function createForm(): Promise<HTMLElement> {
  const o = await options()
  const name = h("input", { className: "input", required: true, placeholder: "Harbor Bakery", ariaLabel: "Name" })
  const note = h("textarea", { className: "input textarea", rows: 3, placeholder: "What is it for? (optional)", ariaLabel: "What it is for" })
  const create = h("button", { className: "btn filled" }, "Create")
  create.onclick = attempt(async () => {
    if (!name.reportValidity()) return
    const v = o.value()
    const p: Project = await (await api("/projects?via=web", { json: { name: name.value, note: note.value, ...v } })).json()
    await refresh()
    go(`/projects/${p.id}/chat`) // where the orchestrator's first reply arrives
  })
  const body = [h("div", { className: "form" }, row("Name", "", name), h("div", { className: "form-row" }, note)), h("details", { className: "disclosure" }, h("summary", null, "Options"), ...o.el)]
  return sheet("New project", "An orchestrator plans it with you.", "/", body, [h("a", { href: "/", className: "btn" }, "Cancel"), create], ...o.over)
}

// instructions is the text every session of the agent starts with. The
// agent's live session is told of a change; no new session is made for it.
function instructions(a: Agent): HTMLElement {
  const box = h("textarea", { className: "input textarea", rows: 8, value: a.instructions ?? "", ariaLabel: "Instructions" })
  const save = h("button", { className: "btn" }, "Save instructions")
  const form = h("form", { className: "stack" }, h("small", { className: "field-help" }, "Every session of this agent starts with them."), box, h("div", { className: "actions" }, save))
  box.oninput = () => (save.textContent = "Save instructions")
  form.onsubmit = attempt(async () => {
    await api(`/agents/${a.id}/instructions?via=web`, { method: "PUT", text: box.value })
    save.textContent = "Saved"
  })
  return form
}

// settings is the project's status and its orchestrator's own config, apps
// and instructions. Its agents keep theirs: the orchestrator changes them when asked.
async function settings(p: Project, signal: AbortSignal): Promise<HTMLElement> {
  const orchestrator = await get<Agent>(`/agents/${p.orchestrator}`, signal)
  const o = await options(orchestrator)
  const at = `/projects/${p.id}`
  const status = h("select", { className: "select plain" }, ...statuses.map(s => h("option", { value: s }, said[s])))
  status.value = p.status
  const text = h("input", { className: "input", placeholder: "One line under the name", maxLength: 100, value: p.line, ariaLabel: "Line" })
  const save = h("button", { className: "btn filled" }, "Save")
  save.onclick = attempt(async () => {
    const v = o.value()
    if (status.value !== p.status || text.value !== p.line) await api(`/projects/${p.id}/status?via=web`, { method: "PUT", json: { status: status.value, line: text.value } })
    await api(`/agents/${p.orchestrator}/config?via=web`, { method: "PUT", json: v.config })
    await api(`/agents/${p.orchestrator}/apps?via=web`, { method: "PUT", json: v.apps })
    acted()
    go(at)
  })
  const remove = h("button", {
    className: "btn danger",
    onclick: attempt(() => removeProject(p)),
  }, "Delete")
  const body = [h("div", { className: "form" }, row("Status", "Open means it is your turn.", status), row("Line", "", text)), ...o.el, h("details", { className: "disclosure" }, h("summary", null, "Orchestrator instructions"), instructions(orchestrator))]
  return sheet("Project settings", p.name, at, body, [remove, h("a", { href: at, className: "btn" }, "Cancel"), save], ...o.over)
}

void render()

// removeProject asks, then deletes the project and maybe its agents.
async function removeProject(p: Project) {
  if (!confirm(`Delete the project "${p.name}" and its dashboard?`)) return
  const agents = confirm("Delete the project's agents too? Cancel keeps them.")
  await api(`/projects/${p.id}?via=web${agents ? "&agents=1" : ""}`, { method: "DELETE" })
  await refresh()
  go("/")
}
