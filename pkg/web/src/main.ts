// The page: a list of projects beside the one in view. A project is seen as
// its dashboard, with the place to write to its orchestrator always there.

import { type Agent, type Conf, type Model, type Project, acted, api, file, get, h, icon, initials, key, login, logout, text } from "./api"
import { chat } from "./chat"
import { composer } from "./composer"
import { dashboard } from "./dashboard"
import { picker } from "./picker"
import lockup from "./lockup.svg"
declare const MPMenu: { open: (menu: HTMLElement, at: Element | { x: number; y: number }, target?: Element) => void }

const statuses = ["open", "waiting", "closed"]
// A project's status as a sidebar row shows it: open is the user's turn.
const rowClass: Record<string, string> = { open: "needs", waiting: "waiting", closed: "closed" }
const said: Record<string, string> = { open: "Needs you", waiting: "Waiting", closed: "Closed" }
// required are the apps every orchestrator has: the project app adds them whatever is asked.
const required = ["message", "orchestrate", "project", "schedule"]
const lastModel = "mainplane-model"
const last = "mainplane-project" // the project last in view, where the page opens

const search = h("input", { type: "search", placeholder: "Search" })
const list = h("div", { className: "list side separated", role: "listbox", ariaLabel: "Projects" })
// Pins are kept in this browser only. A phone shows them as circles above the list.
const pins = h("div", { className: "pins" })
const pinned = () => JSON.parse(localStorage.getItem("pins") ?? "[]") as string[]
const pin = (id: string) => localStorage.setItem("pins", JSON.stringify(pinned().includes(id) ? pinned().filter(x => x !== id) : [...pinned(), id]))
// The list's bar (Messages): Edit opens what is about this harness and Log out; the filter shows one status.
let only = ""
// mark is the Mainplane icon and wordmark, its ink in the text color so it shows in dark too.
const mark = () => Object.assign(h("span", { className: "mark" }), { innerHTML: lockup })
const menuButton = (button: HTMLElement, menu: HTMLElement) => (button.onclick = () => MPMenu.open(menu, button), [button, menu])
const editMenu = h("div", { className: "menu", role: "menu", hidden: true },
  h("div", { className: "menu-label" }, location.host), h("hr"),
  h("button", { role: "menuitem", className: "danger", onclick: () => logout() } as Partial<HTMLButtonElement>, h("span", null, "Log out")))
const filters = ["", ...statuses].map(s => h("button", { role: "menuitemradio", ariaChecked: String(!s), onclick: () => { only = s; filters.forEach((b, i) => (b.ariaChecked = String(["", ...statuses][i] === s))); filter.classList.toggle("on", !!s); drawList() } } as Partial<HTMLButtonElement>, h("span", null, s ? said[s] : "All projects")))
const filter = h("button", { className: "btn icon ghost filter", ariaLabel: "Filter", ariaHasPopup: "menu" }, icon("filter"))
const side = h("nav", { className: "sidebar" },
  h("div", { className: "bar" }, ...menuButton(h("button", { className: "btn plain sm edit", ariaHasPopup: "menu" }, "Edit"), editMenu), mark(), ...menuButton(filter, h("div", { className: "menu", role: "menu", hidden: true }, ...filters))),
  h("div", { className: "top" }, h("label", { className: "search lg" }, icon("search"), search), h("a", { href: "/new", className: "btn icon ghost", ariaLabel: "New project" }, icon("compose"))),
  pins,
  list)
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
// A project visited keeps its views in the page, hidden while another view
// shows: the way back to one draws nothing and waits for nothing. show takes
// the view of it to show, null for none.
type Kept = { nodes: HTMLElement[]; show: (sub: string | null) => void; end: AbortController }
const kept = new Map<string, Kept>()
// most is how many projects are kept: each holds a loaded dashboard and a chat.
const most = 8
// skip is the style of a chat out of view: it has no place and its rendering
// is skipped but kept. Hidden, a long chat would be laid out again each time it shows.
const skip = "content-visibility: hidden; flex: none; height: 0"
const drop = (id: string) => {
  kept.get(id)!.end.abort()
  kept.get(id)!.nodes.forEach(n => n.remove())
  kept.delete(id)
}
// rest is what is in view that is not kept.
let rest: HTMLElement[] = []

// when is a time today, else a day.
const when = (t: string) => {
  const d = new Date(t)
  return d.toDateString() === new Date().toDateString() ? d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
}

// drawList shows the projects that match the search, the user's turn first.
function drawList() {
  const q = search.value.trim().toLowerCase()
  const at = location.pathname.split("/")[2]
  const rows = statuses.flatMap(s => projects.filter(p => p.status === s && (!only || s === only) && (!q || p.name.toLowerCase().includes(q)))).map(p => {
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
// A right click on a row or pin opens the project's menu there (Messages, Slack); on a phone a long press does.
const rowAt = (e: Event) => {
  const a = e.target instanceof Element && e.target.closest<HTMLAnchorElement>("a.row, a.pin")
  const p = a && projects.find(p => a.pathname === `/projects/${p.id}`)
  return p ? { a, p } : null
}
const rowMenu = (p: Project, a: Element, x: number, y: number) => {
  document.querySelector("body > .menu.row-menu")?.remove()
  const m = projectMenu(p)
  m.classList.add("row-menu")
  document.body.append(m)
  MPMenu.open(m, { x, y }, a)
}
// iOS Safari sends no contextmenu for a long press, and Android sends one: a touch opens the menu by its own timer only.
let touch = false, press = 0, pressed = false, from = [0, 0]
for (const el of [list, pins]) {
  el.addEventListener("contextmenu", e => {
    const r = rowAt(e)
    if (!r) return
    e.preventDefault()
    if (!touch) rowMenu(r.p, r.a, e.clientX, e.clientY)
  })
  el.addEventListener("pointerdown", e => {
    touch = e.pointerType === "touch"
    const r = touch && rowAt(e)
    if (!r) return
    from = [e.clientX, e.clientY]
    press = setTimeout(() => { pressed = true; rowMenu(r.p, r.a, from[0], from[1]) }, 500)
  })
  el.addEventListener("pointermove", e => Math.hypot(e.clientX - from[0], e.clientY - from[1]) > 10 && clearTimeout(press))
  for (const t of ["pointerup", "pointercancel"]) el.addEventListener(t, () => clearTimeout(press))
  el.addEventListener("click", e => { if (pressed) { e.preventDefault(); e.stopPropagation(); pressed = false } }, true)
}

// refresh reads the projects and draws the list.
async function refresh() {
  if (!key()) return
  projects = await get<Project[]>("/projects") // the API lists the last updated first
  for (const id of kept.keys()) if (!projects.some(p => p.id === id)) drop(id)
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

// depth is how deep a path is in a phone's stack: the list, a project, its settings, a file or an agent, an agent's settings.
const depth = (path: string) => {
  const [kind, id, sub, agent, more] = path.split("/").slice(1)
  return kind !== "projects" || !id ? 0 : sub === "settings" || sub === "files" ? 2 : sub === "agents" && agent ? (more ? 3 : 2) : sub === "chat" ? 1.5 : 1
}
// On a phone a project's views sit side by side, list, dashboard, chat: a swipe right goes back one, a swipe left on the dashboard goes on to the chat.
// A swipe from the left edge is the browser's own back.
let swipe: { x: number; y: number; t: number } | null = null
// slides is whether the finger is on something that scrolls sideways: a wide table or code block takes the swipe.
const slides = (el: Element | null): boolean => !!el && el !== main && !el.matches("main > *") && ((el.scrollWidth > el.clientWidth + 1 && getComputedStyle(el).overflowX !== "visible") || slides(el.parentElement))
main.addEventListener("touchstart", e => {
  const t = e.touches[0]
  swipe = e.touches.length === 1 && t.clientX > 24 && !(e.target instanceof Element && (e.target.closest("textarea, input, .scrim, .menu") || slides(e.target))) ? { x: t.clientX, y: t.clientY, t: Date.now() } : null
}, { passive: true })
main.addEventListener("touchend", e => {
  const t = e.changedTouches[0], from = swipe
  swipe = null
  if (from) swiped(t.clientX - from.x, t.clientY - from.y, Date.now() - from.t)
})
addEventListener("swipe", e => swiped(...((e as CustomEvent<number[]>).detail as [number, number, number]))) // from a dashboard's frame
function swiped(dx: number, dy: number, ms: number) {
  if (!matchMedia("(max-width: 700px)").matches || Math.abs(dx) < 60 || Math.abs(dx) < 2 * Math.abs(dy) || ms > 700) return
  const [kind, id, sub, agent] = location.pathname.split("/").slice(1)
  if (kind !== "projects" || !id || (sub && sub !== "chat") || agent) return
  const at = `/projects/${id}`
  if (dx > 0 && sub === "chat") go(at)
  else if (dx < 0 && !sub) go(at + "/chat")
}
// move renders the view at the new path. On a phone, a move deeper or back up the stack
// pushes the new view in from the side or pops the old one off it, as iOS does.
function move(from: string, ui = false) {
  const d = depth(location.pathname) - depth(from)
  if (!d || ui || !document.startViewTransition || !matchMedia("(max-width: 700px)").matches || matchMedia("(prefers-reduced-motion: reduce)").matches) return void render()
  document.documentElement.classList.toggle("pop", d < 0)
  document.documentElement.classList.toggle("tab", Math.abs(d) === 0.5) // between dashboard and chat the header stays
  document.startViewTransition(render)
}
function go(path: string) {
  const from = location.pathname
  history.pushState(null, "", path)
  move(from)
}
// A popstate's from is the path last rendered. An iOS swipe back has drawn its own slide (hasUAVisualTransition).
let shown = location.pathname
addEventListener("popstate", e => move(shown, (e as PopStateEvent & { hasUAVisualTransition?: boolean }).hasUAVisualTransition))
addEventListener("go", e => go((e as CustomEvent<string>).detail)) // a dashboard opens a chat or a file
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
  shown = location.pathname
  const [kind, id, sub, agent, more] = location.pathname.split("/").slice(1)
  document.body.classList.toggle("out", !key())
  document.body.classList.toggle("home", location.pathname === "/" || location.pathname === "/new") // a phone shows the list under the new project sheet
  drawList()
  try {
    const nodes = await content(kind, id, sub, signal, agent, more)
    if (!signal.aborted) draw(nodes, kind === "projects" && !agent ? id : "", sub) // a slow one must not draw over the view that came after it
  } catch (err) {
    if (!signal.aborted) draw([toolbar("Mainplane", "", null), h("p", { className: "error-line" }, String(err))])
  }
}

// draw puts nodes in view beside the kept project at, if any, and hides the other kept ones.
function draw(nodes: HTMLElement[], at = "", sub = "") {
  rest.forEach(n => n.remove())
  main.append(...(rest = nodes))
  for (const [id, k] of kept) k.show(id === at ? sub : null)
  dispatchEvent(new Event("view"))
}

// toolbar heads a view: back (on a phone), the name and its line, then its buttons.
// With a menu, the name is a button that opens it: on a phone it is the title pill, the only way to the views.
function toolbar(name: string, line: string, open: ((title: HTMLElement) => void) | null, ...buttons: HTMLElement[]): HTMLElement {
  const title: HTMLElement = open
    ? h("button", { className: "title island", ariaHasPopup: "menu", onclick: () => open(title) } as Partial<HTMLButtonElement>, h("b", null, name), h("small", null, line))
    : h("span", { className: "title" }, h("b", null, name), h("small", null, line))
  return h("div", { className: "toolbar" },
    h("a", { href: "/", className: "btn icon ghost back", ariaLabel: "Projects" }, icon("chevron-left")),
    h("span", { className: "avatar" }, initials(name)), title,
    h("span", { className: "spacer" }), h("span", { className: "tb-group" }, ...buttons))
}

async function content(kind: string, id: string, sub: string, signal: AbortSignal, agent = "", more = ""): Promise<HTMLElement[]> {
  if (!key()) return [loginForm()]
  if (!projects.length) await refresh()
  const none = h("div", { className: "view" }, h("div", { className: "empty" }, icon("grid"), h("h3", null, "No project open"), h("p", null, "Pick one from the list, or make a new one.")))
  if (kind === "new") return [none, await createForm()]
  if (kind === "agents" && id) return agentView(id, `/agents/${id}`, sub, signal)
  if (kind === "projects" && sub === "agents" && agent) return agentView(agent, `/projects/${id}/agents/${agent}`, more, signal, `/projects/${id}/agents`)
  if (kind !== "projects") {
    const to = projects.find(p => p.id === localStorage.getItem(last)) ?? projects[0]
    if (!kind && to && !matchMedia("(max-width: 700px)").matches) {
      history.replaceState(null, "", `/projects/${to.id}`)
      return content("projects", to.id, "", signal)
    }
    return [none]
  }
  const p = projects.find(p => p.id === id)
  if (!p) throw new Error(`No project ${id}`)
  // A kept view that failed says so in its place: the next visit makes the project's views again.
  if (kept.get(p.id)?.nodes.some(n => n.querySelector(".error-line, .thread > .error"))) drop(p.id)
  if (!kept.has(p.id)) keep(p)
  if (sub === "files") return [await stored(p, location.pathname.split("/").slice(4).join("/"), signal)]
  return sub === "settings" ? [await settings(p, signal)] : []
}

// raster is a picture's type, so Open image in new tab shows it. Not SVG: opened in a tab
// it would run its scripts as this page, which holds the key.
const raster = (name: string) => ({ png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" })[name.split(".").pop()!.toLowerCase()] ?? ""
// stored shows one file of a project's store, an image or its text: what is in it is never run.
async function stored(p: Project, name: string, signal: AbortSignal) {
  const b = await file(p.id, name, signal)
  const img = h("img", { alt: name, src: URL.createObjectURL(new Blob([b], { type: raster(name) })) })
  img.style.maxWidth = "100%"
  return h("div", { className: "view" }, h("div", { className: "page" }, h("h2", null, name), /\.(png|jpe?g|gif|webp)$/i.test(name) ? img : h("pre", { className: "code" }, text(new Uint8Array(b)))))
}

// agentView is an agent's chat at the path at. One of a project's agents has back to the project's agents (a Slack thread's back).
async function agentView(id: string, at: string, sub: string, signal: AbortSignal, up = ""): Promise<HTMLElement[]> {
  const a = await get<Agent>(`/agents/${id}`, signal)
  const name = a.name || a.id
  const head = toolbar(name, `${a.status} · ${a.config.model}`, null, h("a", { href: at + "/settings", className: "btn icon ghost", ariaLabel: "Instructions" }, icon("more-vertical")))
  if (up) {
    head.classList.add("up")
    Object.assign(head.querySelector<HTMLAnchorElement>(".back")!, { href: up, ariaLabel: "Agents" })
  }
  const view = [head, chat(id, signal), composer(id, `Message ${name}`)]
  if (sub === "settings") return [...view, sheet("Agent", name, at, [instructions(a)], [h("a", { href: at, className: "btn" }, "Done")])]
  return view
}

// keep makes the views of a project and puts them in the page, hidden. They
// follow the project until it is dropped.
function keep(p: Project) {
  const end = new AbortController(), signal = end.signal
  const at = `/projects/${p.id}`
  // The agents button counts the project's agents; the count comes when the list does.
  const count = h("span", { className: "count" })
  const agents = h("a", { href: at + "/agents", className: "btn ghost agents", ariaLabel: "Agents" }, icon("people"), count)
  const more = h("a", { href: at + "/settings", className: "btn icon ghost", ariaLabel: "Settings" }, icon("more-vertical"))
  // The star pins the project (Slack's channel star); a phone has Pin in the title's menu instead.
  const star = h("button", { className: "btn icon ghost star", ariaPressed: String(pinned().includes(p.id)), ariaLabel: "Star project", onclick: () => { pin(p.id); drawList(); star.ariaPressed = String(pinned().includes(p.id)) } } as Partial<HTMLButtonElement>, icon("star"))
  const head = toolbar(p.name, line(p), t => island(p, t), agents, more)
  head.prepend(star)
  localStorage.setItem(last, p.id)
  addEventListener("projects", e => (e as CustomEvent<Project[]>).detail.filter(q => q.id === p.id).forEach(q => (head.querySelector(".title small")!.textContent = line(q))), { signal })
  const tab = (path: string, label: string) => h("button", { role: "tab", onclick: () => go(path) }, label)
  const tabs = h("div", { className: "tabs", role: "tablist" }, tab(at, "Dashboard"), tab(at + "/chat", "Chat"))
  const write = composer(p.orchestrator, `Message ${p.name}`)
  let on = false
  const dash = dashboard(p, signal), log = chat(p.orchestrator, signal, () => on), people = h("div", { className: "view" })
  const nodes = [head, tabs, dash, log, people, write]
  let top = 1e9 // where the chat is scrolled to, which one out of view forgets: past its end is its end
  const show = (sub: string | null) => {
    if (!log.style.cssText) top = log.scrollHeight - log.scrollTop - log.clientHeight < 40 ? 1e9 : log.scrollTop
    if (!on && sub !== null) write.dispatchEvent(new Event("show"))
    on = sub !== null
    head.hidden = tabs.hidden = !on
    dash.hidden = !on || sub === "chat" || sub === "agents" || sub === "files"
    write.hidden = !on || sub === "files"
    log.style.cssText = sub === "chat" ? "" : skip
    people.hidden = sub !== "agents"
    ;["", "chat"].forEach((name, i) => (tabs.children[i].ariaSelected = String(sub === name)))
    log.scrollTop = top
    // The agents are read again behind each view shown: the last list is there meanwhile.
    if (on) void get<Agent[]>(at + "/agents", signal).then(list => {
      count.textContent = String(list.length)
      people.replaceChildren(h("div", { className: "page" }, h("h2", null, "Agents"), h("div", { className: "list grouped" }, ...list.map(a =>
        h("a", { href: `${at}/agents/${a.id}`, className: `row ${a.status}` }, h("span", { className: "avatar" }, initials(a.name || a.id)), h("span", { className: "row-main" }, h("b", null, a.name || a.id), h("small", null, `${a.status} · ${a.config.model}`)), icon("chevron-right"))))))
    }, () => {})
  }
  main.append(...nodes)
  show(null)
  kept.set(p.id, { nodes, show, end })
  if (kept.size > most) drop(kept.keys().next().value!) // the first kept goes
}

// island opens a project's title on a phone into a larger island over the view (Slack iPhone's channel header, his sketch):
// the views with the one shown filled, then Pin and Delete. It grows out of the title and goes back into it.
function island(p: Project, title: HTMLElement) {
  const at = `/projects/${p.id}`
  const close = () => {
    if (!scrim.isConnected || scrim.classList.contains("closing")) return
    scrim.classList.add("closing")
    grow(true).finished.then(() => scrim.remove())
  }
  const item = (name: string, label: string, act: (e: Event) => void, cls = "") =>
    h("button", { role: "menuitem", className: `row ${cls}`, onclick: (e: Event) => { act(e); close() } } as Partial<HTMLButtonElement>, icon(name), h("span", null, label))
  const view = (name: string, label: string, path: string) => item(name, label, () => go(path), location.pathname === path ? "on" : "")
  const panel = h("div", { className: "island-panel", role: "menu", ariaLabel: p.name },
    h("button", { className: "island-head", ariaLabel: "Close", onclick: close } as Partial<HTMLButtonElement>,
      h("span", { className: "avatar" }, initials(p.name)), h("span", { className: "title" }, h("b", null, p.name), h("small", null, line(p))), icon("close")),
    view("grid", "Dashboard", at), view("message", "Chat", at + "/chat"), view("people", "Agents", at + "/agents"), view("settings", "Settings", at + "/settings"),
    h("hr"),
    item("pin", pinned().includes(p.id) ? "Unpin project" : "Pin project", () => { pin(p.id); drawList() }),
    item("trash", "Delete project", attempt(() => removeProject(p)), "danger"))
  const scrim = h("div", { className: "island-scrim", onclick: (e: Event) => { if (e.target === scrim) close() } } as Partial<HTMLDivElement>, panel)
  document.body.append(scrim)
  addEventListener("keydown", e => e.key === "Escape" && close())
  // grow clips the panel from the title's box to its own.
  const grow = (back = false) => {
    const t = title.getBoundingClientRect(), r = panel.getBoundingClientRect()
    const small = `inset(${t.top - r.top}px ${r.right - t.right}px ${r.bottom - t.bottom}px ${t.left - r.left}px round 22px)`
    const frames = [{ clipPath: small, opacity: 0.6 }, { clipPath: "inset(0 round 32px)", opacity: 1 }]
    return panel.animate(back ? frames.reverse() : frames, { duration: matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : back ? 200 : 320, easing: "cubic-bezier(.3,1,.4,1)", fill: "both" })
  }
  grow()
}

// projectMenu is a project's menu: a row's right click on a desktop, a long press on a phone.
// The views are in the project's own header; this menu changes the project: its status, its pin, or deletes it.
function projectMenu(p: Project): HTMLElement {
  const item = (name: string, label: string, act: (e: Event) => void, cls = "") => h("button", { role: "menuitem", className: cls, onclick: act } as Partial<HTMLButtonElement>, icon(name), h("span", null, label))
  const setStatus = (s: string) => attempt(async () => { await api(`/projects/${p.id}/status?via=web`, { method: "PUT", json: { status: s, line: p.line } }); await refresh() })
  const menu: HTMLElement = h("div", { className: "menu", role: "menu", hidden: true },
    h("button", { role: "menuitem", ariaHasPopup: "menu" } as Partial<HTMLButtonElement>, icon("check-circle"), h("span", null, "Set status"), Object.assign(icon("chevron-right"), { className: "chev" })),
    h("div", { className: "menu", role: "menu", hidden: true }, ...statuses.map(s => h("button", { role: "menuitemradio", ariaChecked: String(s === p.status), onclick: setStatus(s) } as Partial<HTMLButtonElement>, h("span", null, said[s])))),
    h("hr"), item("pin", pinned().includes(p.id) ? "Unpin" : "Pin", () => { pin(p.id); drawList(); menu.querySelector(".pin-item span")!.textContent = pinned().includes(p.id) ? "Unpin" : "Pin"; document.querySelector(".toolbar .star")?.setAttribute("aria-pressed", String(pinned().includes(p.id))) }, "pin-item"),
    item("trash", "Delete", attempt(() => removeProject(p)), "danger"))
  return menu
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
  const s = h("div", { className: "scrim", onclick: e => { if (e.target === s) go(close) } },
    h("div", { className: "sheet", role: "dialog", ariaModal: "true", ariaLabel: title },
      h("header", null, h("hgroup", null, h("h2", null, title), ...(sub ? [h("p", null, sub)] : [])), h("a", { href: close, className: "btn icon ghost", ariaLabel: "Close" }, icon("close"))),
      h("div", { className: "body" }, ...body), h("footer", null, ...foot)), ...over)
  addEventListener("keydown", e => e.key === "Escape" && s.isConnected && go(close))
  // On a phone (iOS): close at the top left, what the foot does at the top right.
  if (matchMedia("(max-width: 700px)").matches) s.querySelector(".sheet > header")!.append(...foot)
  return s
}

// row is a form's row: its name and its control. No help text under the name: the name says enough.
const row = (label: string, control: HTMLElement, tag: "label" | "div" = "label") =>
  h(tag, { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, label)), control)
const section = (label: string, ...rows: HTMLElement[]) => [h("p", { className: "section-l", hidden: !rows.length }, label), h("div", { className: "form", hidden: !rows.length }, ...rows)]

// options is the choice of an orchestrator's model, workers and apps: for a
// new project everything connected, for one there is what its orchestrator has.
async function options(cur?: Agent): Promise<{ el: HTMLElement[]; over: HTMLElement[]; value: () => Promise<{ config: Conf; apps: string[] }> }> {
  const [workers, apps] = await Promise.all([get<{ name: string; refused?: string }[]>("/workers"), get<{ name: string }[]>("/apps")])
  // The sheet does not wait for the models, which a provider may be slow to list: the picker is made again when they come.
  let pk = picker([], cur?.config, null)
  const listed = get<Model[]>("/models").then(models => {
    const now = picker(models, cur?.config, localStorage.getItem(lastModel))
    pk.trigger.replaceWith(now.trigger)
    pk.pops.forEach((p, i) => p.replaceWith(now.pops[i]))
    pk = now
  }, () => {})
  const box = (value: string, checked: boolean, disabled = false) => row(value, h("input", { type: "checkbox", className: "switch", role: "switch", value, checked, disabled }))
  const ticked = (set: HTMLElement) => [...set.querySelectorAll<HTMLInputElement>("input:checked:not(:disabled)")].map(i => i.value)
  const none = (what: string) => h("div", { className: "form-row" }, h("span", { className: "row-main none" }, h("b", null, what)))
  const has = cur?.config.workers ?? [] // an agent with no worker has null
  const names = new Set([...workers.filter(w => !w.refused).map(w => w.name), ...has.map(w => w.name)])
  const ws = section("Workers", ...(names.size ? [...names].map(n => box(n, !cur || has.some(w => w.name === n))) : [none("No workers connected")]))
  // The apps every orchestrator has are shown on and locked: they are kept as they are.
  const fixed = required.filter(r => (cur ? cur.apps : apps.map(a => a.name)).includes(r))
  const as = section("Apps", ...fixed.map(a => box(a, true, true)), ...apps.filter(a => !required.includes(a.name)).map(a => box(a.name, !cur || cur.apps.includes(a.name))))
  return {
    el: [h("div", { className: "form" }, row("Model", pk.trigger, "div")), ...ws, ...as],
    over: pk.pops,
    value: async () => {
      await listed
      const config = pk.value()
      if (!config) throw new Error("No model is connected: add a provider to the harness first.")
      if (config.preset) localStorage.setItem(lastModel, config.preset)
      return { config: { ...config, workers: ticked(ws[1]).map(name => ({ name })) }, apps: [...fixed, ...ticked(as[1])] }
    },
  }
}

async function createForm(): Promise<HTMLElement> {
  const o = await options()
  const name = h("input", { className: "input", placeholder: "Harbor Bakery", ariaLabel: "Name" })
  const note = h("textarea", { className: "input textarea", rows: 3, placeholder: "What is it for? (optional)", ariaLabel: "What it is for" })
  const create = h("button", { className: "btn filled", disabled: true }, "Create") // gray until the name is filled (iOS)
  name.oninput = () => { create.disabled = !name.value.trim() }
  create.onclick = attempt(async () => {
    const v = await o.value()
    const p: Project = await (await api("/projects?via=web", { json: { name: name.value, note: note.value, ...v } })).json()
    await refresh()
    go(`/projects/${p.id}/chat`) // where the orchestrator's first reply arrives
  })
  const body = [h("div", { className: "form" }, row("Name", name), h("label", { className: "form-row" }, note)), ...o.el] // every choice in view, as in iOS Settings
  return sheet("New project", "", "/", body, [create], ...o.over)
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
  // Status is a pop-up button (Apple), not a select: the browser's own list did not line up with it.
  let st = p.status
  const status = h("button", { type: "button", className: "select plain", ariaHasPopup: "menu" }, said[st])
  const choices = statuses.map(s => h("button", { role: "menuitemradio", ariaChecked: String(s === st), onclick: () => { st = s; status.textContent = said[s]; choices.forEach((b, i) => (b.ariaChecked = String(statuses[i] === s))) } } as Partial<HTMLButtonElement>, h("span", null, said[s])))
  const menu = h("div", { className: "menu", role: "menu", hidden: true }, ...choices)
  status.onclick = () => MPMenu.open(menu, status)
  const text = h("input", { className: "input", placeholder: "One line under the name", maxLength: 100, value: p.line, ariaLabel: "Line" })
  const told = h("textarea", { className: "input textarea", rows: 8, value: orchestrator.instructions ?? "", ariaLabel: "Orchestrator instructions" })
  const save = h("button", { className: "btn filled" }, "Save")
  save.onclick = attempt(async () => {
    const v = await o.value()
    if (st !== p.status || text.value !== p.line) await api(`/projects/${p.id}/status?via=web`, { method: "PUT", json: { status: st, line: text.value } })
    await api(`/agents/${p.orchestrator}/config?via=web`, { method: "PUT", json: v.config })
    await api(`/agents/${p.orchestrator}/apps?via=web`, { method: "PUT", json: v.apps })
    if (told.value !== (orchestrator.instructions ?? "")) await api(`/agents/${p.orchestrator}/instructions?via=web`, { method: "PUT", text: told.value })
    acted()
    go(at)
  })
  // Delete is the last thing in the page, full width (Apple); back or the X is the way out, so there is no Cancel.
  const remove = h("button", { className: "btn danger delete", onclick: attempt(() => removeProject(p)) }, "Delete Project")
  const body = [h("div", { className: "form" }, row("Status", status, "div"), row("Line", text)), ...o.el,
    ...section("Orchestrator instructions", h("div", { className: "form-row" }, told)), remove, menu]
  const s = sheet("Project settings", p.name, at, body, [save], ...o.over)
  s.querySelector(".sheet")!.classList.add("large")
  if (!matchMedia("(max-width: 700px)").matches) return s
  // On a phone the settings are a page pushed over the project from the side (Slack iPhone): back where it was takes you out, Save at the top right, the project's name between as text.
  const head = toolbar(p.name, "Settings", null)
  Object.assign(head.querySelector<HTMLAnchorElement>(".back")!, { href: at, ariaLabel: p.name })
  head.append(save)
  s.classList.add("push")
  s.querySelector(".sheet > header")!.replaceWith(head)
  s.querySelector(".sheet > footer")!.remove()
  return s
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
