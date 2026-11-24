// The chat of one agent: every session it has had reads as one conversation.

import { type Agent, type Rec, acted, api, decode, get, h, icon, records, text } from "./api"
import { rich, typeset } from "./rich"

const shown = (r: Rec) => (r.type?.startsWith("text/") ? text(r.body) : `[${r.type}, ${r.len} bytes]`)

// Calls and the messages the user did not write read as quiet lines in the past tense, as in Codex:
// calls in a row are one "Ran 3 commands, read 2 files", such messages in a row one "Received 2 events".
// A line opens to its items, and an item to what it holds.
const chev = () => Object.assign(icon("chevron-right"), { className: "chev" })
const tools: Record<string, string[]> = { // icon, verb, one, many
  run: ["terminal", "Ran", "command", "commands"],
  read: ["file", "Read", "file", "files"],
  write: ["compose", "Wrote", "file", "files"],
  edit: ["pencil", "Edited", "file", "files"],
  apply_patch: ["code", "Patched", "file", "files"],
}
const other = ["wrench", "Called", "tool", "tools"]
const tool = (name: string) => tools[name.split("__").pop()!] ?? other
// item is one call or one message: its line, then what it holds.
const item = (label: Node[], ...more: string[]) => h("details", { className: "tool" }, h("summary", null, ...label, chev()), ...more.map(m => h("pre", { className: "code" }, m)))
// group adds an item to the line of its kind that ends the thread, or to a new one, and says again what the line holds.
const group = (into: HTMLElement, kind: "calls" | "events", it: HTMLElement) => {
  let g = into.lastElementChild as HTMLElement | null
  if (!g?.classList.contains(kind)) into.append(g = h("details", { className: `activity ${kind}` }, h("summary", null, h("span"), chev()), h("div", { className: "items" })))
  const items = g.querySelector(".items")!
  items.append(it)
  const all = [...items.children] as HTMLElement[]
  const counts = new Map<string[], number>()
  for (const i of all) counts.set(tool(i.dataset.tool ?? ""), (counts.get(tool(i.dataset.tool ?? "")) ?? 0) + 1)
  const said = kind === "events"
    ? (all.length === 1 ? "Received event" : `Received ${all.length} events`)
    : [...counts].map(([[, verb, one, many], n], i) => `${i ? verb.toLowerCase() : verb} ${n === 1 ? one : n + " " + many}`).join(", ")
  g.querySelector("summary > span")!.textContent = said
  return it
}
// divider is the quiet rule where a session goes on in a new one; what it carried is behind it.
const divider = (into: HTMLElement, carried: string) => {
  const last = into.lastElementChild
  if (last?.classList.contains("continued")) return void last.append(h("pre", { className: "code" }, carried))
  into.append(h("details", { className: "continued" }, h("summary", null, h("span", null, "Session continued")), h("pre", { className: "code" }, carried)))
}
// from is who sent a message the user did not: an agent, an app or a job. A person writes from any connector.
const from = (via = "") => (via.startsWith("agent:") ? `Agent ${via.slice(6)}` : via.startsWith("app:") ? via.slice(4) : via === "job" ? "A job" : "")
// say adds a message. Bubbles in a row from the same side and sender are one
// group, as in Messages: the sender above the first, the tail on the last. An
// agent's reply with blocks (headings, lists, code) is a column, no bubble.
const say = (into: HTMLElement, sent: boolean, body: string, who = "") => {
  const { html, blocks } = rich(body)
  const column = blocks && !sent
  const last = into.lastElementChild
  const joins = !column && last?.matches(".msg:not(.system, .rt-msg)") && last.classList.contains("sent") === sent && (last as HTMLElement).dataset.who === who
  if (joins) last!.classList.replace("tail", "mid") // a first that is no longer the last is first alone
  if (joins && last!.classList.contains("first")) last!.classList.remove("mid")
  const content = h("div", { className: column ? "rt" : blocks ? "bubble" : "bubble rt-inline" })
  if (blocks && sent) content.textContent = body // what the user wrote reads as they wrote it
  else content.innerHTML = html // markdown escapes any HTML in the text
  const m = h("div", { className: `msg tail${joins ? "" : " first"}${sent ? " sent" : ""}${column ? " rt-msg" : ""}` }, ...(who && !joins ? [h("span", { className: "note" }, who)] : []), content)
  m.dataset.who = who
  into.append(m)
  typeset(content).catch(console.error)
  return m
}

// day is the line above a message after a new day or a pause of an hour, as in Messages: "Today 9:41 AM".
const day = (into: HTMLElement, time: string) => {
  const t = new Date(time), prev = new Date(into.dataset.time ?? 0)
  into.dataset.time = time
  if (t.toDateString() === prev.toDateString() && +t - +prev < 3600e3) return
  const days = Math.round((new Date().setHours(0, 0, 0, 0) - new Date(time).setHours(0, 0, 0, 0)) / 864e5)
  const name = days === 0 ? "Today" : days === 1 ? "Yesterday" : days < 7 ? t.toLocaleDateString(undefined, { weekday: "long" }) : t.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(t.getFullYear() !== new Date().getFullYear() && { year: "numeric" }) })
  into.append(h("div", { className: "day" }, h("b", null, name), " ", t.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })))
}

// chat follows the agent with one long poll, which stops while the page is
// hidden or on says the chat is not in view, and ends with signal. Earlier
// sessions load on request.
export function chat(id: string, signal: AbortSignal, on = () => true): HTMLElement {
  // While the agent works the thread ends in a typing bubble, with a round Stop at the right of its row.
  const stop = h("button", { className: "btn icon stop", ariaLabel: "Stop", onclick: () => post("stop?via=web")() }, icon("stop"))
  const typing = h("div", { className: "msg first tail typing-row", hidden: true }, h("div", { className: "bubble" }, h("span", { className: "typing", role: "status", ariaLabel: "Working" }, h("i"), h("i"), h("i"))), stop)
  const post = (what: string) => () => api(`/agents/${id}/${what}`, { method: "POST" }).then(acted, alert)
  const log = h("div", { className: "view" })
  const thread = h("div", { className: "thread" })
  log.append(thread)
  // The chat stays at its end, as in Messages: what is sent, what comes, the typing bubble, a picture that loads.
  // It stays where it is while the user has scrolled up to read.
  let end = true
  const toEnd = () => (log.scrollTop = log.scrollHeight)
  log.addEventListener("scroll", () => (end = log.scrollHeight - log.scrollTop - log.clientHeight < 40), { passive: true })
  new ResizeObserver(() => end && toEnd()).observe(thread, { box: "border-box" }) // border-box: the composer growing pads the thread
  const calls = new Map<string, HTMLElement>()
  // A message shows as it is sent, before the harness has it. Its record takes its place; one that fails is taken back.
  const pending = h("div")
  addEventListener("sending", e => {
    const d = (e as CustomEvent<{ agent: string; text: string; files: File[]; sent: Promise<unknown> }>).detail
    if (d.agent !== id) return
    const mine = [...(d.text.trim() ? [d.text] : []), ...d.files.map(f => `[${f.type}, ${f.size} bytes]`)].map(part => say(pending, true, part))
    end = true // what you send takes you to the end
    toEnd()
    d.sent.catch(() => mine.forEach(m => m.remove()))
  }, { signal })

  const draw = (recs: Rec[], into: HTMLElement) => {
    for (const r of recs) {
      const user = r.kind === "message" && !from(r.via) && r.via !== "rollover"
      if (user || r.kind === "text") day(into, r.time)
      if (user) {
        pending.firstElementChild?.remove()
        say(into, true, shown(r))
      } else if (r.kind === "message" && r.via === "rollover") divider(into, shown(r))
      else if (r.kind === "message") group(into, "events", item([h("span", null, r.via === "app:project" ? "Project created" : from(r.via))], shown(r)))
      else if (r.kind === "text") say(into, false, shown(r))
      // A handoff ends its session with the id of the next one, then the error that says why: the next session's rule shows it.
      else if (r.kind === "error" && into.dataset.handoff !== String(r.n - 1)) into.append(h("div", { className: "msg system error" }, shown(r)))
      else if (r.kind === "system" && r.source === "continue" && /^[0-9a-f]{8}\s*$/.test(shown(r))) into.dataset.handoff = String(r.n)
      else if (r.kind === "system" && r.source === "continue" && r.n > 1) delete into.dataset.handoff, divider(into, shown(r))
      else if (r.kind === "call") {
        const c = JSON.parse(text(r.body)), a = typeof c.arguments === "string" ? JSON.parse(c.arguments || "{}") : c.arguments ?? {}
        const name = String(c.name).split("__").pop()!
        const it = item([icon(tool(name)[0]), h("span", null, name)], JSON.stringify(a, null, 1))
        it.dataset.tool = name
        calls.set(r.id, group(into, "calls", it))
      } else if (r.kind === "result") calls.get(r.for ?? "")?.append(h("pre", { className: "code" }, shown(r)))
    }
  }

  let poll = new AbortController()
  const wake = () => poll.abort()
  addEventListener("acted", wake, { signal })
  addEventListener("view", wake, { signal })
  document.addEventListener("visibilitychange", wake, { signal })
  // The poll waits while the chat is not seen; what wakes it has it look again.
  const visible = async () => {
    while (document.visibilityState !== "visible" || !on()) await new Promise(go => (poll = new AbortController()).signal.addEventListener("abort", go))
  }

  const follow = async () => {
    const top = h("div") // the earlier sessions go after it
    const live = h("div")
    thread.append(top, live, pending, typing) // the typing bubble is the last item while the agent works
    // A turn is one call: the next records of the session followed, which session is the live one, and whether the agent works.
    let session = "", n = 0, status = ""
    const turn = async (wait: string, signal: AbortSignal) => {
      const r = await api(`/agents/${id}/records?session=${session}&after=${n}&status=${status}${wait && "&wait=" + wait}`, { signal })
      const recs = await decode(r)
      draw(recs, live)
      n = recs.at(-1)?.n ?? n
      status = r.headers.get("Mainplane-Status")!
      typing.hidden = status !== "stepping"
      return r.headers.get("Mainplane-Session")!
    }
    // The sessions before the live one are asked for beside its records, not before them.
    const [agent, first] = await Promise.all([get<Agent>(`/agents/${id}`, signal), turn("", signal)])
    live.dataset.session = session = first
    const earlier = agent.sessions.slice(0, agent.sessions.indexOf(session))
    // The session before loads as the top of the chat comes near, as in Messages, and what the user reads stays in place.
    let loading = Promise.resolve()
    const before = () => (loading = loading.then(async () => {
      if (!earlier.length) return
      const part = h("div")
      draw(await records((part.dataset.session = earlier.pop()!), 0, signal), part)
      const below = log.scrollHeight - log.scrollTop
      top.after(part) // the session after it starts with a rule of its own
      if (!end) log.scrollTop = log.scrollHeight - below
    }))
    const near = new IntersectionObserver(async ([e]) => {
      if (!e.isIntersecting || !earlier.length) return
      await before()
      near.unobserve(top) // the top may still be near: observing again asks once more
      near.observe(top)
    }, { root: log, rootMargin: "800px 0px 0px 0px" })
    near.observe(top)
    signal.addEventListener("abort", () => near.disconnect())
    // seek shows the record the address names, as #<session>.<n>, under the sessions back to its own.
    const seek = async () => {
      const [, s, n] = location.hash.match(/^#(\w+)\.(\d+)$/) ?? []
      if (!s || !on()) return
      while (earlier.includes(s)) await before()
      ;[...thread.querySelectorAll<HTMLElement>(`[data-session="${s}"] > [data-n]`)].find(e => +e.dataset.n! >= +n)?.scrollIntoView({ block: "center" }) // a record that drew nothing: the next that did
    }
    addEventListener("view", () => void seek(), { signal })
    await seek()
    while (!signal.aborted) {
      await visible()
      poll = new AbortController()
      try {
        const now = await turn("30s", AbortSignal.any([signal, poll.signal]))
        // A post or a handoff may have moved the agent to a new session: what is left of this one is read first.
        if (now !== session) {
          await turn("", signal)
          session = now
          n = 0
        }
      } catch (err) {
        if (!poll.signal.aborted) throw err
      }
    }
  }
  follow().catch(err => signal.aborted || thread.append(h("div", { className: "error" }, String(err), " ", h("a", { href: location.pathname }, "Try again"))))
  return log
}
