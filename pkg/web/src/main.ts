// The page: a list of projects beside the one in view. A project is seen as
// its dashboard, with the place to write to its orchestrator always there.

import { type Agent, type Conf, type Model, type Project, acted, api, get, h, key, login, logout } from "./api"
import { chat, composer } from "./chat"
import { dashboard } from "./dashboard"

const statuses = ["open", "waiting", "closed"]
// required are the apps every orchestrator has: the project app adds them whatever is asked.
const required = ["message", "orchestrate", "project", "schedule"]
const lastModel = "mainplane-model"

const side = h("nav"), main = h("main")
document.body.append(side, main)

// A login link carries the key after the #, which no server sees. It is
// saved and taken out of the address bar.
const linked = location.hash.match(/^#k=(.+)$/)
if (linked) {
  login(decodeURIComponent(linked[1]))
  history.replaceState(null, "", location.pathname)
}

let projects: Project[] = []

// refresh reads the projects and draws the list.
async function refresh() {
  if (!key()) return
  projects = await get<Project[]>("/projects")
  side.replaceChildren(h("a", { href: "/new" }, "New project"), h("hr"))
  for (const status of statuses) {
    const of = projects.filter(p => p.status === status) // the API lists the last updated first
    if (of.length) side.append(h("h4", null, status), ...of.flatMap(p => [h("a", { href: `/projects/${p.id}`, className: status }, p.name, p.working ? " (working)" : ""), h("small", null, p.line)]))
  }
  side.append(h("hr"), h("button", { onclick: () => logout() }, "Log out"))
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
  document.body.className = !key() ? "out" : location.pathname === "/" ? "home" : ""
  const back = h("a", { href: "/", className: "back" }, "Projects")
  try {
    const nodes = await content(kind, id, sub, signal)
    if (!signal.aborted) main.replaceChildren(back, ...nodes) // a slow one must not draw over the view that came after it
  } catch (err) {
    if (!signal.aborted) main.replaceChildren(back, h("p", { className: "error" }, String(err)))
  }
}

async function content(kind: string, id: string, sub: string, signal: AbortSignal): Promise<Node[]> {
  if (!key()) return [loginForm()]
  if (!projects.length) await refresh()
  if (kind === "new") return [await createForm()]
  if (kind === "agents" && id) return [...chat(id, signal), composer(id, "Message this agent")]
  if (kind !== "projects") return []
  const p = projects.find(p => p.id === id)
  if (!p) throw new Error(`No project ${id}`)
  const at = `/projects/${p.id}`
  const head = [
    h("h2", null, p.name),
    h("div", null, h("a", { href: at }, "Dashboard"), " | ", h("a", { href: at + "/chat" }, "Chat"), " | ", h("a", { href: at + "/agents" }, "Agents"), " | ", h("a", { href: at + "/settings" }, "Settings")),
    statusForm(p, signal),
  ]
  const write = composer(p.orchestrator, "Message the orchestrator")
  if (sub === "chat") return [...head, ...chat(p.orchestrator, signal), write]
  if (sub === "agents") {
    const agents = await get<Agent[]>(at + "/agents", signal)
    return [...head, h("ul", { className: "scroll" }, ...agents.map(a => h("li", null, h("a", { href: `/agents/${a.id}` }, a.name || a.id), ` ${a.id} · ${a.status} · ${a.config.model}`))), write]
  }
  if (sub === "settings") return [...head, await settings(p, signal), write]
  return [...head, dashboard(p, signal), write]
}

function loginForm(): HTMLElement {
  const k = h("input", { type: "password", required: true, placeholder: "API key", autocomplete: "current-password" })
  const form = h("form", null, h("h2", null, "Mainplane"), h("p", null, sessionStorage.getItem("refused") ? "The harness refused that key." : "Enter this harness's API key, or open the link that `mainplane web` prints."), k, h("button", null, "Log in"))
  form.onsubmit = e => {
    e.preventDefault()
    sessionStorage.removeItem("refused")
    login(k.value)
    void render()
  }
  return form
}

// statusForm is the user's own say on the project's status. It follows the
// project while the user is not in it.
function statusForm(p: Project, signal: AbortSignal): HTMLElement {
  const status = h("select", null, ...statuses.map(s => h("option", null, s)))
  const line = h("input", { placeholder: "line", maxLength: 100 })
  const working = h("span")
  const form = h("form", null, status, line, h("button", null, "Set status"), working)
  const show = (q: Project) => {
    working.textContent = q.working ? " working" : ""
    if (!form.contains(document.activeElement)) [status.value, line.value] = [q.status, q.line]
  }
  show(p)
  addEventListener("projects", e => (e as CustomEvent<Project[]>).detail.filter(q => q.id === p.id).forEach(show), { signal })
  form.onsubmit = attempt(async () => {
    await api(`/projects/${p.id}/status?via=web`, { method: "PUT", json: { status: status.value, line: line.value } })
    line.blur()
    acted()
  })
  return form
}

// options is the choice of an orchestrator's model, workers and apps: for a
// new project everything connected, for one there is what its orchestrator has.
async function options(cur?: Agent): Promise<{ el: HTMLElement; value: () => { config: Conf; apps: string[] } }> {
  const [models, workers, apps] = await Promise.all([get<Model[]>("/models").catch(() => []), get<{ name: string; refused?: string }[]>("/workers"), get<{ name: string }[]>("/apps")])
  const model = h("select", null, ...models.map(m => h("option", { value: m.id }, m.name)))
  // A model the catalog does not list stays as it is until another is picked.
  const has = cur && (cur.config.preset ?? cur.config.model)
  const kept = cur && !models.some(m => m.id === has)
  if (kept) model.prepend(h("option", { value: "" }, cur.config.model))
  model.value = cur ? (kept ? "" : has!) : (localStorage.getItem(lastModel) ?? "")
  if (model.selectedIndex < 0) model.selectedIndex = 0
  const box = (value: string, checked: boolean, disabled = false) => h("label", null, h("input", { type: "checkbox", value, checked, disabled }), " " + value)
  const ticked = (set: HTMLElement) => [...set.querySelectorAll<HTMLInputElement>("input:checked")].map(i => i.value)
  const names = new Set([...workers.filter(w => !w.refused).map(w => w.name), ...(cur?.config.workers.map(w => w.name) ?? [])])
  const ws = h("fieldset", null, h("legend", null, "Workers"), ...[...names].map(n => box(n, !cur || cur.config.workers.some(w => w.name === n))))
  const as = h("fieldset", null, h("legend", null, "Apps"), ...apps.map(a => (required.includes(a.name) ? box(a.name, true, true) : box(a.name, !cur || cur.apps.includes(a.name)))))
  return {
    el: h("div", null, h("label", null, "Model ", model), ws, as),
    value: () => {
      const picked = models.find(m => m.id === model.value)
      if (!picked && !cur) throw new Error("No model is connected: add a provider to the harness first.")
      const base = picked && picked.id !== has ? picked.config : cur!.config
      localStorage.setItem(lastModel, model.value)
      return { config: { ...base, workers: ticked(ws).map(name => ({ name })) }, apps: ticked(as) }
    },
  }
}

async function createForm(): Promise<HTMLElement> {
  const o = await options()
  const name = h("input", { required: true, placeholder: "Name" })
  const note = h("textarea", { rows: 3, placeholder: "What is it for? (optional)" })
  const form = h("form", { className: "scroll" }, h("h2", null, "New project"), h("label", null, name), h("label", null, note), h("details", null, h("summary", null, "Options"), o.el), h("button", null, "Create"))
  form.onsubmit = attempt(async () => {
    const v = o.value()
    const p: Project = await (await api("/projects?via=web", { json: { name: name.value, note: note.value, ...v } })).json()
    await refresh()
    go(`/projects/${p.id}/chat`) // where the orchestrator's first reply arrives
  })
  return form
}

// settings is the orchestrator's own config and apps. Its agents keep
// theirs: the orchestrator changes them when asked.
async function settings(p: Project, signal: AbortSignal): Promise<HTMLElement> {
  const o = await options(await get<Agent>(`/agents/${p.orchestrator}`, signal))
  const form = h("form", { className: "scroll" }, o.el, h("button", null, "Save"), h("hr"))
  form.onsubmit = attempt(async () => {
    const v = o.value()
    await api(`/agents/${p.orchestrator}/config?via=web`, { method: "PUT", json: v.config })
    await api(`/agents/${p.orchestrator}/apps?via=web`, { method: "PUT", json: v.apps })
    go(`/projects/${p.id}`)
  })
  form.append(h("button", {
    type: "button",
    onclick: attempt(async () => {
      if (!confirm(`Delete the project "${p.name}" and its dashboard?`)) return
      const agents = confirm("Delete the project's agents too? Cancel keeps them.")
      await api(`/projects/${p.id}?via=web${agents ? "&agents=1" : ""}`, { method: "DELETE" })
      await refresh()
      go("/")
    }),
  }, "Delete project"))
  return form
}

void render()
