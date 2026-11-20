// The chat of one agent: every session it has had reads as one conversation.

import { type Agent, type Rec, acted, api, decode, get, h, icon, records, text } from "./api"
import { rich, typeset } from "./rich"

const shown = (r: Rec) => (r.type?.startsWith("text/") ? text(r.body) : `[${r.type}, ${r.len} bytes]`)

// fold is a line with more behind it.
const fold = (label: string, ...more: string[]) => h("details", { className: "tool" }, h("summary", null, icon("chevron-right"), label), ...more.map(m => h("pre", { className: "code" }, m)))
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
export function chat(id: string, signal: AbortSignal, on = () => true): HTMLElement[] {
  // While the agent works the thread ends in a typing bubble, and it can be stopped.
  const typing = h("div", { className: "msg first tail", hidden: true }, h("div", { className: "bubble" }, h("span", { className: "typing", role: "status", ariaLabel: "Working" }, h("i"), h("i"), h("i"))))
  const stop = h("button", { className: "btn plain sm", hidden: true, onclick: () => post("stop?via=web")() }, "Stop")
  const retry = h("button", { className: "btn plain sm", onclick: () => post("retry")() }, "Retry")
  const post = (what: string) => () => api(`/agents/${id}/${what}`, { method: "POST" }).then(acted, alert)
  const log = h("div", { className: "view" })
  const thread = h("div", { className: "thread" })
  log.append(thread)
  const calls = new Map<string, HTMLElement>()
  // A message shows as it is sent, before the harness has it. Its record takes its place; one that fails is taken back.
  const pending = h("div")
  addEventListener("sending", e => {
    const d = (e as CustomEvent<{ agent: string; text: string; files: File[]; sent: Promise<unknown> }>).detail
    if (d.agent !== id) return
    const mine = [...(d.text.trim() ? [d.text] : []), ...d.files.map(f => `[${f.type}, ${f.size} bytes]`)].map(part => say(pending, true, part))
    log.scrollTop = log.scrollHeight
    d.sent.catch(() => mine.forEach(m => m.remove()))
  }, { signal })

  const draw = (recs: Rec[], into: HTMLElement) => {
    const bottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40
    for (const r of recs) {
      if (r.kind === "message" || r.kind === "text") day(into, r.time)
      if (r.kind === "message" && r.via === "app:project") into.append(fold("Project created", shown(r)))
      else if (r.kind === "message" && r.via === "rollover") into.append(fold("The task, carried from the session before", shown(r)))
      else if (r.kind === "message") {
        if (!from(r.via)) pending.firstElementChild?.remove()
        say(into, !from(r.via), shown(r), from(r.via))
      }
      else if (r.kind === "text") say(into, false, shown(r))
      else if (r.kind === "error") into.append(h("div", { className: "msg system error" }, shown(r)))
      else if (r.kind === "system" && r.source === "continue" && r.n > 1 && !/^[0-9a-f]{8}\s*$/.test(shown(r))) into.append(h("div", { className: "msg system" }, "New session"), fold("Carried over", shown(r)))
      else if (r.kind === "call") {
        const c = JSON.parse(text(r.body))
        const d = fold(c.name, typeof c.arguments === "string" ? c.arguments : JSON.stringify(c.arguments, null, 1))
        calls.set(r.id, d)
        into.append(d)
      } else if (r.kind === "result") calls.get(r.for ?? "")?.append(h("pre", { className: "code" }, shown(r)))
    }
    if (bottom) log.scrollTop = log.scrollHeight
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
    const more = h("button", { className: "btn plain sm", hidden: true }, "Load the session before")
    const live = h("div")
    thread.append(more, live, pending, typing) // the typing bubble is the last item while the agent works
    // A turn is one call: the next records of the session followed, which session is the live one, and whether the agent works.
    let session = "", n = 0, status = ""
    const turn = async (wait: string, signal: AbortSignal) => {
      const r = await api(`/agents/${id}/records?session=${session}&after=${n}&status=${status}${wait && "&wait=" + wait}`, { signal })
      const recs = await decode(r)
      draw(recs, live)
      n = recs.at(-1)?.n ?? n
      status = r.headers.get("Mainplane-Status")!
      typing.hidden = stop.hidden = status !== "stepping"
      retry.hidden = !stop.hidden
      return r.headers.get("Mainplane-Session")!
    }
    // The sessions before the live one are asked for beside its records, not before them.
    const [agent, first] = await Promise.all([get<Agent>(`/agents/${id}`, signal), turn("", signal)])
    session = first
    const earlier = agent.sessions.slice(0, agent.sessions.indexOf(session))
    more.hidden = !earlier.length
    more.onclick = async () => {
      const part = h("div")
      draw(await records(earlier.pop()!, 0, signal), part)
      more.after(part) // the session after it starts with a rule of its own
      more.hidden = !earlier.length
    }
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
  return [log, h("div", { className: "agent-bar" }, h("span"), stop, retry)]
}
