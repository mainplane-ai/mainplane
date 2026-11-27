// The page: a list of projects beside the one in view. A project is seen as
// its dashboard, with the place to write to its orchestrator always there.

import { type Agent, type Conf, type Key, type Model, type Project, acted, api, avatar, avatarColor, avatarColors, file, get, h, icon, initials, key, login, logout, saveAvatar, text } from "./api"
import { chat } from "./chat"
import { rich } from "./rich"
import { composer } from "./composer"
import { dashboard, frameURL } from "./dashboard"
import { picker } from "./picker"
import { providersPanel } from "./providers"
import { type Worker, deviceKey, install, loginLink, onboarded, onboarding, setUp, workerRows } from "./onboarding"
import lockup from "./lockup.svg"
declare const MPMenu: { open: (menu: HTMLElement, at: Element | { x: number; y: number }, target?: Element) => void }

const statuses = ["open", "waiting", "closed"]
// A project's status as a sidebar row shows it: open is the user's turn.
const rowClass: Record<string, string> = { open: "needs", waiting: "waiting", closed: "closed" }
const said: Record<string, string> = { open: "Needs you", waiting: "Waiting", closed: "Closed" }
// required are the apps every orchestrator has: the project app adds them whatever is asked, so they are not shown.
const required = ["continue", "message", "orchestrate", "project", "schedule"]
const lastModel = "mainplane-model"
const last = "mainplane-project" // the project last in view, where the page opens

const search = h("input", { type: "search", placeholder: "Search" })
const list = h("div", { className: "list side separated", role: "listbox", ariaLabel: "Projects" })
// Pins are kept in this browser only. A phone shows them as circles above the list.
const pins = h("div", { className: "pins" })
const pinned = () => JSON.parse(localStorage.getItem("pins") ?? "[]") as string[]
const pin = (id: string) => localStorage.setItem("pins", JSON.stringify(pinned().includes(id) ? pinned().filter(x => x !== id) : [...pinned(), id]))
// The list's bar (Messages): Settings, with a dot while an update waits (iOS); the filter shows one status.
let only = ""
// mark is the Mainplane icon and wordmark, its ink in the text color so it shows in dark too.
const mark = () => Object.assign(h("span", { className: "mark" }), { innerHTML: lockup })
const menuButton = (button: HTMLElement, menu: HTMLElement) => (button.onclick = () => MPMenu.open(menu, button), [button, menu])
const gear = h("button", { className: "btn icon ghost gear", ariaLabel: "Settings", onclick: () => go("/settings") }, icon("settings"))
const waiting = (v: Settings["version"]) => !!v.latest && v.latest !== v.current
const updated = (v: Settings["version"]) => { gear.classList.toggle("update", waiting(v)); gear.ariaLabel = waiting(v) ? "Settings, update available" : "Settings" }
// The harness is asked at the first view with a key, then each hour: the dot comes with a release.
let checked = false
const check = () => {
  if (key()) get<Settings>("/settings").then(s => { checked = true; updated(s.version) }, () => {}) // one that failed is asked again at the next view
}
setInterval(check, 36e5)
const filters = ["", ...statuses].map(s => h("button", { role: "menuitemradio", ariaChecked: String(!s), onclick: () => { only = s; filters.forEach((b, i) => (b.ariaChecked = String(["", ...statuses][i] === s))); filter.classList.toggle("on", !!s); drawList() } } as Partial<HTMLButtonElement>, h("span", null, s ? said[s] : "All projects")))
const filter = h("button", { className: "btn icon ghost filter", ariaLabel: "Filter", ariaHasPopup: "menu" }, icon("filter"))
const side = h("nav", { className: "sidebar" },
  h("div", { className: "bar" }, gear, mark(), ...menuButton(filter, h("div", { className: "menu", role: "menu", hidden: true }, ...filters))),
  h("div", { className: "top" }, h("label", { className: "search lg" }, icon("search"), search), h("a", { href: "/new", className: "btn icon ghost", ariaLabel: "New project" }, icon("compose"))),
  h("div", { className: "scroll" }, pins, list))
const main = h("main")
document.body.classList.add("mp")
document.body.append(side, main)

// A login link carries the key after the #, which no server sees. It is
// saved and taken out of the address bar. A tab already on the page gets the
// link as a change of the # alone, which loads nothing: hashchange reads it.
function linked() {
  const k = location.hash.match(/^#k=(.+)$/)
  if (!k) return false
  login(decodeURIComponent(k[1]))
  history.replaceState(null, "", location.pathname)
  return true
}
linked()
addEventListener("hashchange", () => linked() && void render())

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
  // Within open, a project that is working comes after one that stopped, which more likely waits on the user.
  const rows = statuses.flatMap(s => projects.filter(p => p.status === s && (!only || s === only) && (!q || p.name.toLowerCase().includes(q))).sort((a, b) => s === "open" ? +a.working - +b.working : 0)).map(p => {
    const line = h("small", null, p.line)
    if (p.working) line.prepend(h("span", { className: "typing inline", role: "status", ariaLabel: "Working" }, h("i"), h("i"), h("i")))
    return h("a", { href: `/projects/${p.id}`, className: `row ${p.working ? "working" : rowClass[p.status]}${pinned().includes(p.id) ? " pinned" : ""}`, role: "option", ariaSelected: String(p.id === at), ariaDescription: p.working ? "Working" : said[p.status] },
      h("span", { className: "u" }), avatar(p),
      h("span", { className: "row-main" }, h("span", { className: "h" }, h("b", null, p.name), h("span", { className: "row-trail" }, when(p.updated))), line))
  })
  list.replaceChildren(...rows)
  // A pin whose project needs you shows its line in a bubble, as Messages shows an unread message.
  pins.replaceChildren(...pinned().flatMap(id => projects.filter(p => p.id === id)).map(p =>
    h("a", { href: `/projects/${p.id}`, className: `pin ${p.working ? "working" : rowClass[p.status]}` },
      ...(p.status === "open" && p.line ? [h("span", { className: "peek" }, p.line)] : []),
      avatar(p), h("small", null, h("span", { className: "u" }), p.name))))
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
  if (kind === "settings") return 1
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
  if (/^\/projects\/?$/.test(location.pathname)) history.replaceState(null, "", "/") // the list has no path of its own: /projects is home
  shown = location.pathname
  const [kind, id, sub, agent, more] = location.pathname.split("/").slice(1)
  if (kind !== "welcome" && key() && !onboarded()) await setUp()
  if (!checked) check()
  if (signal.aborted) return
  document.body.classList.toggle("out", !key() || !onboarded() || kind === "welcome")
  document.body.classList.toggle("home", /^\/(new(\/\w+)?)?$/.test(location.pathname)) // a phone shows the list under the new project sheet
  drawList()
  try {
    const nodes = await content(kind, id, sub, signal, agent, more)
    if (!signal.aborted) draw(nodes, kind === "settings" ? localStorage.getItem(last) ?? "" : kind === "projects" && (!agent || sub === "settings") ? id : "", kind === "settings" ? "" : sub) // a slow one must not draw over the view that came after it
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
function toolbar(who: string | Pick<Project, "id" | "name" | "color" | "monogram">, line: string, open: ((title: HTMLElement) => void) | null, ...buttons: HTMLElement[]): HTMLElement {
  const name = typeof who === "string" ? who : who.name
  const badge = typeof who === "string" ? h("span", { className: "avatar" }, initials(who)) : avatar(who)
  const title: HTMLElement = open
    ? h("button", { className: "title island", ariaHasPopup: "menu", onclick: () => open(title) } as Partial<HTMLButtonElement>, h("b", null, name), h("small", null, line))
    : h("span", { className: "title" }, h("b", null, name), h("small", null, line))
  return h("div", { className: "toolbar" },
    h("a", { href: "/", className: "btn icon ghost back", ariaLabel: "Projects" }, icon("chevron-left")),
    badge, title,
    h("span", { className: "spacer" }), h("span", { className: "tb-group" }, ...buttons))
}

let ob: HTMLElement | null = null // the onboarding, kept while it runs: a render keeps its step
async function content(kind: string, id: string, sub: string, signal: AbortSignal, agent = "", more = ""): Promise<HTMLElement[]> {
  if (!onboarded() || kind === "welcome") return [ob ??= onboarding(loginForm, kind === "welcome", () => { ob = null; void refresh().then(() => go(projects.length ? "/" : "/new")) })]
  if (!key()) return [loginForm()]
  if (!projects.length) await refresh()
  const none = h("div", { className: "view" }, h("div", { className: "empty" }, icon("grid"), h("h3", null, "No project open"), h("p", null, "Pick one from the list, or make a new one.")))
  if (kind === "new") return [none, await createForm(id ?? "")]
  if (kind === "settings") {
    const to = projects.find(p => p.id === localStorage.getItem(last))
    if (to && !kept.has(to.id)) keep(to)
    return [...(to ? [] : [none]), await harnessSettings(id ?? "")]
  }
  if (kind === "agents" && id) return agentView(id, `/agents/${id}`, sub, signal)
  if (kind === "projects" && sub === "agents" && agent) return agentView(agent, `/projects/${id}/agents/${agent}`, more, signal, `/projects/${id}/agents`)
  if (kind !== "projects") {
    const to = projects.find(p => p.id === localStorage.getItem(last)) ?? projects[0]
    if (!kind && to && !signal.aborted && !matchMedia("(max-width: 700px)").matches) {
      history.replaceState(null, "", `/projects/${to.id}`)
      void render() // as the project's own path: this render, which would draw for /, ends
      return []
    }
    return [none]
  }
  const p = projects.find(p => p.id === id)
  if (!p) throw new Error(`No project ${id}`)
  // A kept view that failed says so in its place: the next visit makes the project's views again.
  if (kept.get(p.id)?.nodes.some(n => n.querySelector(".error-line, .thread > .error"))) drop(p.id)
  if (!kept.has(p.id)) keep(p)
  if (sub === "files") return [await stored(p, location.pathname.split("/").slice(4).join("/"), signal)]
  return sub === "settings" ? [await settings(p, signal, location.pathname.split("/")[4])] : sub === "agents" ? [await agentsSheet(p, signal)] : []
}

// raster is a picture's type, so Open image in new tab shows it. Not SVG: opened in a tab
// it would run its scripts as this page, which holds the key.
const raster = (name: string) => ({ png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp" })[name.split(".").pop()!.toLowerCase()] ?? ""
// stored shows one file of a project's store: an image, or its text, or an HTML file as a page.
// The page runs in a dashboard's frame, under the policy the store serves its files with: no network.
async function stored(p: Project, name: string, signal: AbortSignal) {
  const b = await file(p.id, name, signal)
  if (/\.html?$/i.test(name)) {
    const frame = h("iframe", { src: frameURL + "?file", className: "dash", title: name })
    frame.setAttribute("sandbox", "allow-scripts") // the store's Sandbox policy: no popups, no key
    addEventListener("message", e => e.source === frame.contentWindow && e.data?.ready === true && frame.contentWindow!.postMessage({ html: text(new Uint8Array(b)), inset: [matchMedia("(max-width: 700px)").matches ? 60 : 0, 0] }, "*"), { signal })
    return frame
  }
  const img = h("img", { alt: name, src: URL.createObjectURL(new Blob([b], { type: raster(name) })) })
  img.style.maxWidth = "100%"
  return h("div", { className: "view" }, h("div", { className: "page" }, h("h2", null, name), /\.(png|jpe?g|gif|webp)$/i.test(name) ? img : h("pre", { className: "code" }, text(new Uint8Array(b)))))
}

// agentView is an agent's chat at the path at. One of a project's agents has back to the project's agents (a Slack thread's back).
async function agentView(id: string, at: string, sub: string, signal: AbortSignal, up = ""): Promise<HTMLElement[]> {
  const a = await get<Agent>(`/agents/${id}`, signal)
  const name = a.name || a.id
  // A project's agent wears the project's color with its own letters: the avatar is saved on the project only.
  const proj = projects.find(q => q.id === up.split("/")[2])
  const head = toolbar(proj ? { id: proj.id, color: proj.color, name } : name, `${a.status} · ${a.config.model}`, null, h("a", { href: at + "/settings", className: "btn icon ghost", ariaLabel: "Instructions" }, icon("more-vertical")))
  if (up) {
    head.classList.add("up")
    Object.assign(head.querySelector<HTMLAnchorElement>(".back")!, { href: up, ariaLabel: "Agents" })
  }
  const view = [head, chat(id, signal), composer(id, `Message ${name}`)]
  if (sub !== "settings") return view
  // The name saves when it is left or on Return (Finder); the instructions have their own Save.
  const field = h("input", { className: "input", value: a.name ?? "", placeholder: a.id, maxLength: 200, ariaLabel: "Name" })
  field.onchange = attempt(async () => {
    await api(`/agents/${a.id}/name?via=web`, { method: "PUT", text: field.value.trim() })
    acted()
    void render() // the header and the sheet show the new name
  })
  field.onkeydown = e => e.key === "Enter" && field.blur()
  return [...view, sheet("Agent", name, at, [h("div", { className: "form" }, row("Name", field)), instructions(a)], [])]
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
  let cur = p
  const head = toolbar(p, line(p), t => island(cur, t), agents, more)
  head.prepend(star)
  localStorage.setItem(last, p.id)
  addEventListener("projects", e => (e as CustomEvent<Project[]>).detail.filter(q => q.id === p.id).forEach(q => {
    cur = q
    head.querySelector(".title b")!.textContent = q.name
    head.querySelector(".title small")!.textContent = line(q)
    head.querySelector(".avatar")!.replaceWith(avatar(q))
    const box = write.querySelector("textarea")!
    box.placeholder = box.ariaLabel = `Message ${q.name}`
  }), { signal })
  const tab = (path: string, label: string) => h("button", { role: "tab", onclick: () => go(path) }, label)
  const tabs = h("div", { className: "tabs", role: "tablist" }, tab(at, "Dashboard"), tab(at + "/chat", "Chat"))
  const write = composer(p.orchestrator, `Message ${p.name}`)
  let on = false
  const dash = dashboard(p, signal), log = chat(p.orchestrator, signal, () => on)
  const nodes = [head, tabs, dash, log, write]
  let top = 1e9 // where the chat is scrolled to, which one out of view forgets: past its end is its end
  const show = (sub: string | null) => {
    if (!log.style.cssText) top = log.scrollHeight - log.scrollTop - log.clientHeight < 40 ? 1e9 : log.scrollTop
    if (!on && sub !== null) write.dispatchEvent(new Event("show"))
    on = sub !== null
    head.hidden = tabs.hidden = !on
    dash.hidden = !on || sub === "chat" || sub === "files"
    write.hidden = !on || sub === "files"
    log.style.cssText = sub === "chat" ? "" : skip
    ;["", "chat"].forEach((name, i) => (tabs.children[i].ariaSelected = String(sub === name)))
    log.scrollTop = top
    // The agents are counted again behind each view shown.
    if (on) void get<Agent[]>(at + "/agents", signal).then(list => (count.textContent = String(list.length)), () => {})
  }
  main.append(...nodes)
  show(null)
  kept.set(p.id, { nodes, show, end })
  if (kept.size > most) drop(kept.keys().next().value!) // the first kept goes
}

// agentsSheet is a project's agents as a sheet over it, as its settings are: rows as the project list has them, the last active first.
async function agentsSheet(p: Project, signal: AbortSignal): Promise<HTMLElement> {
  const at = `/projects/${p.id}`
  const list = (await get<Agent[]>(at + "/agents", signal)).sort((a, b) => +new Date(b.updated) - +new Date(a.updated))
  const rows = list.map(a => h("a", { href: `${at}/agents/${a.id}`, className: "row" }, avatar({ id: p.id, color: p.color, name: a.name || a.id }),
    h("span", { className: "row-main" }, h("span", { className: "h" }, h("b", null, a.name || a.id), h("span", { className: "row-trail" }, when(a.updated))), h("small", null, `${a.status} · ${a.config.model}`))))
  return sheet("Agents", p.name, at, [h("div", { className: "list side separated" }, ...rows)], [])
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
      avatar(p), h("span", { className: "title" }, h("b", null, p.name), h("small", null, line(p))), icon("close")),
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
    item("pencil", "Rename", () => go(`/projects/${p.id}/settings/name`)),
    item("image", "Change icon", () => go(`/projects/${p.id}/settings/appearance`)),
    item("stop", "Stop all agents", attempt(() => stopProject(p)), "danger"),
    h("hr"), item("pin", pinned().includes(p.id) ? "Unpin" : "Pin", () => { pin(p.id); drawList(); menu.querySelector(".pin-item span")!.textContent = pinned().includes(p.id) ? "Unpin" : "Pin"; document.querySelector(".toolbar .star")?.setAttribute("aria-pressed", String(pinned().includes(p.id))) }, "pin-item"),
    item("trash", "Delete", attempt(() => removeProject(p)), "danger"))
  return menu
}

const line = (p: Project) => [p.working ? "Working" : said[p.status], p.line].filter(Boolean).join(" · ")

// loginForm is the page a browser with no key opens to, a phone's home screen app too (it keeps its own storage):
// the lockup, then the key or the link `mainplane web` prints, whole. A phone can scan the link's QR code instead:
// its own camera app would open the link in the browser, not in a home screen app.
function loginForm(done: () => void = () => void render()): HTMLElement {
  const k = h("input", { className: "input lg", type: "password", required: true, placeholder: "API key or login link", autocomplete: "current-password", ariaLabel: "API key or login link" })
  const note = h("p", null, sessionStorage.getItem("refused") ? "The harness refused that key." : "Log in to this harness.")
  const enter = (v: string) => {
    sessionStorage.removeItem("refused")
    login(decodeURIComponent(v.split("#k=").pop()!))
    done()
  }
  const video = h("video", { muted: true, playsInline: true, autoplay: true })
  const box = h("div", { className: "scan", hidden: true }, video)
  const scan = h("button", { type: "button", className: "btn tinted lg", hidden: !(matchMedia("(max-width: 700px)").matches && navigator.mediaDevices?.getUserMedia) }, icon("camera"), h("span", null, "Scan QR Code"))
  let stream: MediaStream | null = null
  const stop = () => {
    stream?.getTracks().forEach(t => t.stop())
    stream = null
    box.hidden = true
    scan.lastElementChild!.textContent = "Scan QR Code"
  }
  scan.onclick = async () => {
    if (stream) return stop()
    let jsQR: (d: Uint8ClampedArray, w: number, h: number, o?: { inversionAttempts: string }) => { data: string } | null
    const cam = navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" }, audio: false })
    try {
      ;[stream, { default: jsQR }] = await Promise.all([cam, import(new URL("qr.js", import.meta.url).href)])
    } catch (e) {
      cam.then(s => s.getTracks().forEach(t => t.stop()), () => {}) // the camera opened but the decoder did not load
      note.textContent = (e as Error).name === "NotAllowedError" ? "The camera is not allowed. Allow it in the browser's settings, or paste the link." : "The camera did not open: " + (e as Error).message
      return
    }
    video.srcObject = stream
    box.hidden = false
    scan.lastElementChild!.textContent = "Stop Scanning"
    note.textContent = "Point the camera at the QR code that mainplane web shows."
    const canvas = h("canvas")
    const ctx = canvas.getContext("2d", { willReadFrequently: true })!
    // A frame at most 640 pixels wide, about 8 a second: enough for a code on a screen, light on a phone.
    const tick = () => {
      if (!stream) return
      if (!form.isConnected) return stop()
      if (video.videoWidth) {
        const f = Math.min(1, 640 / Math.max(video.videoWidth, video.videoHeight))
        canvas.width = video.videoWidth * f
        canvas.height = video.videoHeight * f
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height)
        const code = jsQR(ctx.getImageData(0, 0, canvas.width, canvas.height).data, canvas.width, canvas.height, { inversionAttempts: "dontInvert" })
        if (code?.data.includes("#k=")) return (stop(), enter(code.data))
        if (code) note.textContent = "That QR code is not a Mainplane login link."
      }
      setTimeout(tick, 120)
    }
    tick()
  }
  const form = h("form", { className: "login" }, mark(), note, box,
    k, h("button", { className: "btn filled lg" }, "Log In"), scan,
    h("small", null, "Run ", h("code", null, "mainplane web"), " on the harness for a login link and its QR code."))
  form.onsubmit = e => {
    e.preventDefault()
    stop()
    enter(k.value)
  }
  return form
}

// sheet is a dialog over the view, its buttons in its foot. Close goes back
// to where it was opened. What floats over it (a picker) goes in the scrim.
function sheet(title: string, sub: string, close: string, body: Node[], foot: Node[], ...over: Node[]): HTMLElement {
  const s = h("div", { className: "scrim", onclick: e => { if (e.target === s) go(close) } },
    h("div", { className: "sheet", role: "dialog", ariaModal: "true", ariaLabel: title },
      h("header", null, h("hgroup", null, h("h2", null, title), ...(sub ? [h("p", null, sub)] : [])), h("a", { href: close, className: "btn icon ghost", ariaLabel: "Close" }, icon("close"))),
      h("div", { className: "body" }, ...body), ...(foot.length ? [h("footer", null, ...foot)] : [])), ...over)
  addEventListener("keydown", e => e.key === "Escape" && s.isConnected && go(close))
  // On a phone (iOS): close at the top left, what the foot does at the top right.
  if (matchMedia("(max-width: 700px)").matches) s.querySelector(".sheet > header")!.append(...foot)
  return s
}

// ask is an alert of two choices, Cancel and a destructive act: true when the act is chosen.
const ask = (title: string, text: string, act: string) => new Promise<boolean>(done => {
  const end = (v: boolean) => { s.remove(); removeEventListener("keydown", esc, true); done(v) }
  const esc = (e: KeyboardEvent) => { if (e.key === "Escape") { e.stopPropagation(); end(false) } } // before the sheet's Escape, which closes it
  const cancel = h("button", { className: "btn lg", onclick: () => end(false) }, "Cancel")
  const s = h("div", { className: "scrim", onclick: e => { if (e.target === s) end(false) } },
    h("div", { className: "alert", role: "alertdialog", ariaModal: "true", ariaLabel: title }, h("h2", null, title), h("p", null, text),
      h("div", { className: "alert-actions" }, cancel, h("button", { className: "btn lg tinted danger", onclick: () => end(true) }, act))))
  document.body.append(s)
  addEventListener("keydown", esc, true)
  cancel.focus()
})

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
  // Only the apps there is a choice of: the ones every orchestrator has are kept as they are. None, and the section is hidden.
  const fixed = required.filter(r => (cur ? cur.apps : apps.map(a => a.name)).includes(r))
  const as = section("Apps", ...apps.filter(a => !required.includes(a.name)).map(a => box(a.name, !cur || cur.apps.includes(a.name))))
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

// createForm is the new project sheet, in the tabs of a project's settings: its name and model, then its workers.
async function createForm(tab: string): Promise<HTMLElement> {
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
  const panels: [string, string, HTMLElement][] = [
    ["general", "General", h("div", null, h("div", { className: "form" }, row("Name", name), h("label", { className: "form-row" }, note)), o.el[0])],
    ["workers", "Workers", h("div", null, o.el[2])],
    ...(o.el[4].hidden ? [] : [["apps", "Apps", h("div", null, o.el[4])] as [string, string, HTMLElement]]),
  ]
  return tabbed("New project", "/new", "/", panels, tab, [create], [], o.over, null)
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
async function settings(p: Project, signal: AbortSignal, tab = ""): Promise<HTMLElement> {
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
  const named = h("input", { className: "input", maxLength: 200, value: p.name, ariaLabel: "Name" })
  const told = h("textarea", { className: "input textarea", rows: 16, value: orchestrator.instructions ?? "", ariaLabel: "Orchestrator instructions" })
  // Appearance: the avatar large above its letters and its color (iOS contact). Empty letters are the name's initials.
  let col = avatarColor(p)
  const big = avatar(p)
  const mono = h("input", { className: "input", maxLength: 2, placeholder: initials(p.name), value: p.monogram ?? "", ariaLabel: "Letters" })
  mono.oninput = () => (big.textContent = mono.value.trim() || initials(p.name))
  const swatches = avatarColors.map(c => h("button", { type: "button", role: "radio", className: "avatar", ariaLabel: c, ariaChecked: String(c === col), onclick: () => {
    col = c; big.dataset.color = c; swatches.forEach(b => (b.ariaChecked = String(b.ariaLabel === c)))
  } } as Partial<HTMLButtonElement>))
  swatches.forEach((b, i) => (b.dataset.color = avatarColors[i]))
  const save = h("button", { className: "btn filled" }, "Save")
  save.onclick = attempt(async () => {
    // A project's orchestrator has its name: the web renames both (the server keeps them apart).
    const nm = named.value.trim()
    if (nm && nm !== p.name) {
      await api(`/projects/${p.id}/name?via=web`, { method: "PUT", json: { name: nm } })
      await api(`/agents/${p.orchestrator}/name?via=web`, { method: "PUT", text: nm })
    }
    if (col !== avatarColor(p) || mono.value.trim() !== (p.monogram ?? "")) await saveAvatar(p.id, col, mono.value)
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
  const halt = h("button", { className: "btn danger delete", onclick: attempt(() => stopProject(p)) }, "Stop All Agents")
  // Usage comes after the sheet opens: the first ask may wait for the catalog.
  const usage = h("div", null, h("div", { className: "empty" }, h("p", null, "Loading...")))
  get<Usage>(`/projects/${p.id}/usage`, signal).then(u => usage.replaceChildren(...usageView(u)), e => usage.replaceChildren(h("div", { className: "empty" }, h("p", null, String(e?.message ?? e)))))
  // Tabs as in Slack's channel details: one section in view, each in grouped cards. The tab is in the path, so a link can open one.
  const panels: Record<string, HTMLElement> = {
    general: h("div", null, h("div", { className: "form" }, row("Name", named), row("Status", status, "div"), row("Line", text)), o.el[0], halt, remove),
    workers: h("div", null, o.el[2]),
    ...(o.el[4].hidden ? {} : { apps: h("div", null, o.el[4]) }),
    instructions: h("div", null, h("div", { className: "form" }, h("div", { className: "form-row" }, told)), h("p", { className: "note" }, "Every session of the orchestrator starts with them.")),
    appearance: h("div", null, h("div", { className: "big-avatar" }, big), h("div", { className: "form" }, row("Letters", mono), row("Color", h("div", { className: "swatches", role: "radiogroup", ariaLabel: "Color" }, ...swatches), "div"))),
    usage,
  }
  const names: Record<string, string> = { general: "General", workers: "Workers", apps: "Apps", instructions: "Instructions", appearance: "Appearance", usage: "Usage" }
  // On a phone the settings are a page pushed over the project from the side (Slack iPhone): back where it was takes you out, the project's name between as text.
  const head = toolbar(p, "Settings", null)
  Object.assign(head.querySelector<HTMLAnchorElement>(".back")!, { href: at, ariaLabel: p.name })
  const s = tabbed(p.name, at + "/settings", at, Object.entries(panels).map(([k, el]) => [k, names[k], el]), tab === "name" ? "general" : tab, [save], [menu], o.over, head)
  if (tab === "name") requestAnimationFrame(() => { named.focus(); named.select() }) // Rename in the project's menu
  return s
}

// Usage is what GET /projects/{id}/usage says: USD at today's list prices, of the sessions on a priced model.
type Tokens = { input?: number; output?: number; cache_read?: number; cache_write?: number } // a zero count is left out
type Spend = { id: string; name: string; provider?: string; billing: "tokens" | "subscription" | "none"; cost: number; tokens: Tokens }
type Usage = { cost: number; tokens: Tokens; since?: string; agents: Spend[]; models: Spend[] }
const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 })
const usd = new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" })
const money = (n: number) => (n > 0 && n < 0.01 ? "<" + usd.format(0.01) : usd.format(n))
const all = (t: Tokens) => (t.input ?? 0) + (t.output ?? 0) + (t.cache_read ?? 0) + (t.cache_write ?? 0)
const tokens = (t: Tokens) => compact.format(all(t)) + " tokens"
const split = (t: Tokens) => ([["Input", t.input], ["Output", t.output], ["Cache read", t.cache_read], ["Cache write", t.cache_write]] as const).map(([k, n]) => k + " " + compact.format(n ?? 0)).join(" · ")

// usageView is the total large, then the agents and the models that make it up, as a phone's battery usage.
// A subscription's use and a model with no price show so in place of a cost.
function usageView(u: Usage): HTMLElement[] {
  if (!all(u.tokens)) return [h("div", { className: "empty" }, h("h3", null, "No usage yet"), h("p", null, "What this project's agents use shows here."))]
  const bill = (s: Spend) => (s.billing === "tokens" ? money(s.cost) : s.billing === "subscription" ? "Subscription" : "No price")
  const line = (s: Spend, sub: string) => h("div", { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, s.name), h("small", { title: split(s.tokens) }, sub)), h("span", { className: "amount" }, bill(s)))
  const has = (b: string) => u.models.some(m => m.billing === b)
  const since = u.since ? new Date(u.since) : null
  const day = since?.toLocaleDateString(undefined, { month: "short", day: "numeric", year: since.getFullYear() === new Date().getFullYear() ? undefined : "numeric" })
  const notes = [has("tokens") && "Estimated at today's list prices.", has("subscription") && "Use on a subscription is part of its plan's price.", has("none") && "Models with no list price, such as local ones, are not counted."]
  return [
    h("div", { className: "usage-total" }, h("b", null, money(u.cost)), h("small", { title: split(u.tokens) }, tokens(u.tokens) + (day ? " since " + day : ""))),
    ...section("Agents", ...u.agents.filter(a => all(a.tokens)).map(a => line(a, tokens(a.tokens)))),
    ...section("Models", ...u.models.map(m => line(m, [m.provider, tokens(m.tokens)].filter(Boolean).join(" · ")))),
    h("p", { className: "note" }, notes.filter(Boolean).join(" ")),
  ]
}

// harnessSettings is what is about the harness, not one project: its network and the login, its version,
// providers, the instructions every session gets, and data sharing. Each part saves on its own (iOS Settings).
type Settings = { version: { current: string; latest?: string; notes?: string }; data_sharing?: { share: boolean; at?: string; via?: string }; prompt: { lines: string[]; default?: boolean } }
async function harnessSettings(tab: string): Promise<HTMLElement> {
  const [st, root] = await Promise.all([get<Settings>("/settings"), get<{ name: string; version: string }>("/")])
  const post = (body: object) => api("/settings", { method: "POST", json: body })
  const value = (v: string) => h("span", { className: "value" }, v)
  updated(st.version)
  const version = waiting(st.version) ? h("a", { className: "value update", href: "/settings/update" }, `${st.version.current}, update to ${st.version.latest}`) : value(st.version.current)
  const general = h("div", null, h("div", { className: "form" }, row("Network", value(root.name), "div"), row("Address", value(location.host), "div"), row("Version", version, "div")),
    h("button", { className: "btn danger delete", onclick: () => logout() }, "Log Out"))

  // Software Update (iOS): the release, its notes and Update Now, or that this one is the latest.
  const { current, latest, notes } = st.version
  const update = h("div", null)
  if (!latest) update.append(h("div", { className: "empty" }, h("h3", null, "Mainplane " + current), h("p", null, "Could not check for an update.")))
  else if (latest === current) update.append(h("div", { className: "empty" }, h("h3", null, "Mainplane " + current), h("p", null, "Mainplane is up to date.")))
  else {
    const now = h("button", { className: "btn filled" }, "Update Now")
    const line = h("p", { className: "note" })
    const body = h("div", { className: "rt release-notes" })
    body.innerHTML = notes ? rich(notes).html : "" // markdown escapes any HTML in the text
    now.onclick = attempt(async () => {
      now.disabled = true
      now.textContent = "Updating..."
      line.textContent = "Mainplane restarts on " + latest + ". This page reloads when it is back."
      // A success seldom answers: the harness restarts under the call. Only an error the harness wrote is one.
      const r = await fetch("/settings", { method: "POST", headers: { Authorization: "Bearer " + key() }, body: JSON.stringify({ version: latest }) }).catch(() => null)
      if (r?.status === 500) {
        Object.assign(now, { disabled: false, textContent: "Update Now" })
        line.textContent = ""
        throw new Error((await r.text()).trim())
      }
      for (const end = Date.now() + 180e3; Date.now() < end; await new Promise(t => setTimeout(t, 2000)))
        if ((await get<{ version: string }>("/").catch(() => null))?.version === latest) return location.reload()
      throw new Error("The update did not finish in 3 minutes.")
    })
    // The notes show their start; Learn more shows them whole, in place.
    const more = h("button", { className: "btn plain sm", onclick: () => { body.classList.add("open"); more.remove() } }, "Learn more...")
    update.append(h("div", { className: "form release" },
      h("div", { className: "form-row" }, h("img", { className: "app-icon", src: document.querySelector<HTMLLinkElement>("link[rel=icon]")!.href, alt: "" }), h("span", { className: "row-main" }, h("b", null, "Mainplane " + latest), h("small", null, "Now on " + current))),
      ...(notes ? [h("div", { className: "form-row notes" }, body)] : []),
      h("div", { className: "form-row foot" }, ...(notes ? [more] : []), now)), line)
  }

  const provs = providersPanel()

  // Instructions: the lines every new session gets after the system prompt.
  const box = h("textarea", { className: "input textarea", rows: 16, value: st.prompt.lines.join("\n"), ariaLabel: "Instructions" })
  const save = h("button", { className: "btn" }, "Save Instructions")
  const reset = h("button", { className: "btn plain", hidden: !!st.prompt.default }, "Use the Default")
  let saved = box.value // unchanged, the default is not saved as the user's own: a later release's default still comes
  box.oninput = () => (save.textContent = "Save Instructions")
  save.onclick = attempt(async () => {
    const text = box.value // what is sent: an edit while it goes stays unsaved
    if (text !== saved) { await post({ prompt: { lines: text ? text.split("\n") : [] } }); saved = text; reset.hidden = false }
    if (box.value === text) save.textContent = "Saved"
  })
  reset.onclick = attempt(async () => { await post({ prompt: { default: true } }); saved = box.value = (await get<Settings>("/settings")).prompt.lines.join("\n"); reset.hidden = true })
  const told = h("div", null, h("div", { className: "form" }, h("div", { className: "form-row" }, box)),
    h("p", { className: "note" }, "Every new session starts with them, after the system prompt."), h("div", { className: "actions" }, reset, save))

  // Privacy: the words are the terminal app's (text b1), less where to change it.
  const u = st.data_sharing
  const blocked = u?.via === "DO_NOT_TRACK"
  const sw = h("input", { type: "checkbox", className: "switch", role: "switch", checked: !!u?.share && !blocked, disabled: blocked, ariaLabel: "Help improve Mainplane" })
  const since = h("p", { className: "note" })
  const when = (at?: string) => (since.textContent = blocked ? "Off: DO_NOT_TRACK is set on the harness." : sw.checked && at ? "On since " + new Date(at).toLocaleDateString() + "." : "")
  when(u?.at)
  sw.onchange = attempt(async () => {
    try { await post({ data_sharing: { share: sw.checked, via: "web", text: "b1" } }) } catch (e) { sw.checked = !sw.checked; throw e }
    when(new Date().toISOString())
  })
  const privacy = h("div", null, h("div", { className: "form" }, row("Help improve Mainplane", sw)),
    h("p", { className: "note" }, "Allow the use of your sessions to improve Mainplane and train AI models."), since)

  // Workers (as onboarding's Connect Workers): the connected ones, kept current while the tab is open, then the
  // one-line install that connects another.
  const connected = h("div"), lines = h("div"), state = h("div")
  const workers = h("div", null, h("p", { className: "section-l" }, "Connected"), connected,
    h("p", { className: "section-l" }, "Connect a Worker"), state, lines, h("p", { className: "note" }, "A worker is a machine your agents use. Run this one-line install on a machine to connect it."))
  void (async () => {
    do {
      const [ws, top, j] = await Promise.all([get<Worker[]>("/workers"), get<{ network: string }>("/"), get<{ join: string }>("/join")]).catch(() => [null, null, null])
      if (ws) {
        const named = ws.filter(w => w.name)
        connected.replaceChildren(named.length ? workerRows(named) : h("div", { className: "form" }, h("div", { className: "form-row" }, h("span", { className: "row-main" }, h("small", null, "No workers connected")))))
        state.replaceChildren(...(top!.network !== "ready" ? [h("p", { className: "note" }, top!.network === "starting" ? "The network is starting. The line works once it is ready." : "The network failed: " + top!.network)] : []))
        if (j!.join && !lines.firstChild) lines.append(install(j!.join)) // the line stays put while the workers above it change
      }
      await new Promise(t => setTimeout(t, 1500))
    } while (workers.isConnected) // in the page by the first wait
  })()

  // Devices: the keys logged in, each to revoke but this device's and the harness machine's own, then (as onboarding's
  // Another Device) a login link, a new key, so it is made only when asked for.
  const keys = h("div")
  const day = (at: string) => new Date(at).toDateString() === new Date().toDateString() ? "today" : new Date(at).toLocaleDateString(undefined, { month: "short", day: "numeric" })
  let draws = 0 // only the last draw asked for draws: an older answer must not hide a key just made
  const drawKeys = async () => {
    const n = ++draws, ks = await get<Key[]>("/keys")
    if (n === draws) keys.replaceChildren(h("div", { className: "form" }, ...ks.map(k =>
      h("div", { className: "form-row" }, icon(k.name.startsWith("web-") ? "globe" : k.name.startsWith("tui-") || k.harness ? "terminal" : "computer"),
        h("span", { className: "row-main" }, h("b", null, k.name), h("small", null, [k.harness && "The harness machine", k.created && "Added " + day(k.created), k.used && "Used " + day(k.used)].filter(Boolean).join(" \u00b7 "))),
        k.self ? h("span", { className: "row-trail" }, "This device") : k.harness ? "" : h("button", { className: "btn plain sm danger", onclick: attempt(() => revoke(k.name)) }, "Revoke")))))
  }
  const revoke = async (name: string) => {
    if (!await ask(`Revoke "${name}"?`, "That device is logged out. It needs a new login link to log in again.", "Revoke")) return
    await api("/keys/" + encodeURIComponent(name), { method: "DELETE" })
    await drawKeys()
  }
  drawKeys().catch(alert)
  const link = h("div")
  const make = h("button", { className: "btn filled" }, "Show Login Link")
  make.onclick = attempt(async () => {
    make.disabled = true
    try { link.replaceChildren(...await loginLink(await deviceKey())) } catch (e) { make.disabled = false; throw e }
    make.remove()
    await drawKeys()
  })
  const devices = h("div", null, h("p", { className: "section-l" }, "Logged In"), keys, h("p", { className: "section-l" }, "Log In Another Device"), link,
    h("p", { className: "note" }, "Your projects and chats are on every device logged in to this harness. Show the code and scan it with a phone's camera, or open the link in another browser."),
    h("div", { className: "actions" }, make))

  const head = toolbar("Settings", "", null)
  head.querySelector(".avatar")!.remove()
  const s = tabbed("Settings", "/settings", "/", [["general", "General", general], ["update", "Update", update], ["providers", "Providers", provs], ["workers", "Workers", workers], ["devices", "Devices", devices], ["instructions", "Instructions", told], ["privacy", "Privacy", privacy]], tab, [], [], [], head)
  s.querySelector("[role=tab][value=update]")!.classList.toggle("update", waiting(st.version))
  return s
}

// tabbed is a large sheet with one section in view at a time, as in Slack's channel details. The tab is in the path
// (base, base/<tab>), so a link can open one. On a phone it is a page pushed from the side under head, the foot at its top right.
// With no head, a phone keeps the sheet (New project rises from the bottom) and its tabs start the page.
function tabbed(title: string, base: string, close: string, panels: [string, string, HTMLElement][], tab: string, foot: Node[], more: Node[], over: Node[], head: HTMLElement | null): HTMLElement {
  // The first show writes no address: the view may already be gone to a later one, and its address is right.
  const show = (k: string, write = true) => {
    for (const [n, , el] of panels) el.hidden = n !== k
    tabs.querySelectorAll("button").forEach(b => (b.ariaSelected = String(b.value === k)))
    requestAnimationFrame(() => tabs.querySelector("[aria-selected=true]")!.scrollIntoView({ block: "nearest", inline: "nearest" })) // once the sheet is in the page
    if (write) history.replaceState(null, "", base + (k === panels[0][0] ? "" : "/" + k))
  }
  const tabs = h("div", { className: "tabs", role: "tablist" }, ...panels.map(([k, name]) => h("button", { role: "tab", value: k, onclick: () => show(k) } as Partial<HTMLButtonElement>, name)))
  const s = sheet(title, "", close, [...panels.map(p => p[2]), ...more], foot, ...over)
  s.querySelector(".sheet")!.classList.add("large", "tabbed")
  s.querySelector(".sheet > header")!.after(tabs)
  show(panels.some(p => p[0] === tab) ? tab : panels[0][0], false)
  if (!matchMedia("(max-width: 700px)").matches) return s
  if (!head) return (s.querySelector(".sheet > .body")!.prepend(tabs), s)
  head.append(...foot)
  s.classList.add("push")
  s.querySelector(".sheet > header")!.replaceWith(head)
  s.querySelector(".sheet > footer")?.remove()
  s.querySelector(".sheet > .body")!.prepend(tabs) // the toolbar floats over the top of the page: the tabs start the page under it
  return s
}

void render()

// A page left open (a home screen app lives for days) outlives an update of the harness, which serves its files
// only under the new build's path: when it comes back into view and its own script is gone, it loads the new build.
addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") void fetch(import.meta.url, { method: "HEAD" }).then(r => r.status === 404 && location.reload(), () => {})
})

// stopProject asks, then stops every agent of the project at once: an emergency stop. Their timers end too,
// so only a message wakes one again.
async function stopProject(p: Project) {
  if (!confirm(`Stop every agent of "${p.name}" now? Their running work and timers end. They start again only when you write to them.`)) return
  await api(`/projects/${p.id}/stop?via=web`, { method: "POST" })
  acted()
}

// removeProject asks, then deletes the project and maybe its agents.
async function removeProject(p: Project) {
  if (!confirm(`Delete the project "${p.name}" and its dashboard?`)) return
  const agents = confirm("Delete the project's agents too? Cancel keeps them.")
  await api(`/projects/${p.id}?via=web${agents ? "&agents=1" : ""}`, { method: "DELETE" })
  await refresh()
  go("/")
}
