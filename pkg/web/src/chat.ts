// The chat of one agent: every session it has had reads as one conversation.

import { type Agent, type Rec, acted, api, get, h, icon, records, text } from "./api"

const shown = (r: Rec) => (r.type?.startsWith("text/") ? text(r.body) : `[${r.type}, ${r.len} bytes]`)

// fold is a line with more behind it.
const fold = (label: string, ...more: string[]) => h("details", { className: "tool" }, h("summary", null, icon("chevron-right"), label), ...more.map(m => h("pre", { className: "code" }, m)))
// from is who sent a message the user did not: an agent, an app or a job. A person writes from any connector.
const from = (via = "") => (via.startsWith("agent:") ? `Agent ${via.slice(6)}` : via.startsWith("app:") ? via.slice(4) : via === "job" ? "A job" : "")
// say adds a bubble. Bubbles in a row from the same side and sender are one
// group, as in Messages: the sender above the first, the tail on the last.
const say = (into: HTMLElement, sent: boolean, body: string, who = "") => {
  const last = into.lastElementChild
  const joins = last?.matches(".msg:not(.system)") && last.classList.contains("sent") === sent && (last as HTMLElement).dataset.who === who
  if (joins) last!.classList.replace("tail", "mid") // a first that is no longer the last is first alone
  if (joins && last!.classList.contains("first")) last!.classList.remove("mid")
  const m = h("div", { className: `msg tail${joins ? "" : " first"}${sent ? " sent" : ""}` }, ...(who && !joins ? [h("span", { className: "note" }, who)] : []), h("div", { className: "bubble" }, body))
  m.dataset.who = who
  into.append(m)
}

// chat follows the agent's live session with one long poll, which stops
// while the page is hidden and ends with signal. Earlier sessions load on request.
export function chat(id: string, signal: AbortSignal): HTMLElement[] {
  // While the agent works the thread ends in a typing bubble, and it can be stopped.
  const typing = h("div", { className: "msg first tail", hidden: true }, h("div", { className: "bubble" }, h("span", { className: "typing", role: "status", ariaLabel: "Working" }, h("i"), h("i"), h("i"))))
  const stop = h("button", { className: "btn plain sm", hidden: true, onclick: () => post("stop?via=web")() }, "Stop")
  const post = (what: string) => () => api(`/agents/${id}/${what}`, { method: "POST" }).then(acted, alert)
  const log = h("div", { className: "view" })
  const thread = h("div", { className: "thread" })
  log.append(thread)
  const calls = new Map<string, HTMLElement>()

  const draw = (recs: Rec[], into: HTMLElement) => {
    const bottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40
    for (const r of recs) {
      if (r.kind === "message" && r.via === "app:project") into.append(fold("Project created", shown(r)))
      else if (r.kind === "message" && r.via === "rollover") into.append(fold("The task, carried from the session before", shown(r)))
      else if (r.kind === "message") say(into, !from(r.via), shown(r), from(r.via))
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
  document.addEventListener("visibilitychange", wake, { signal })
  const visible = () => new Promise<void>(go => (document.visibilityState === "visible" ? go() : document.addEventListener("visibilitychange", () => go(), { once: true, signal })))

  const follow = async () => {
    let agent = await get<Agent>(`/agents/${id}`, signal)
    const earlier = agent.sessions.slice(0, -1)
    const more = h("button", { className: "btn plain sm", hidden: !earlier.length }, "Load the session before")
    more.onclick = async () => {
      const part = h("div")
      draw(await records(earlier.pop()!, 0, "", signal), part)
      more.after(part) // the session after it starts with a rule of its own
      more.hidden = !earlier.length
    }
    const live = h("div")
    thread.append(more, live)
    for (let session = agent.sessions.at(-1)!, n = 0; !signal.aborted; ) {
      await visible()
      poll = new AbortController()
      try {
        const recs = await records(session, n, n ? "30s" : "", AbortSignal.any([signal, poll.signal]))
        draw(recs, live)
        n = recs.at(-1)?.n ?? n
      } catch (err) {
        if (!poll.signal.aborted) throw err
      }
      // A post or a handoff may have moved the agent to a new session.
      agent = await get<Agent>(`/agents/${id}`, signal)
      typing.hidden = stop.hidden = agent.status !== "working"
      thread.append(typing) // the last item while the agent works
      if (agent.sessions.at(-1) !== session) {
        draw(await records(session, n, "", signal), live)
        session = agent.sessions.at(-1)!
        n = 0
      }
    }
  }
  follow().catch(err => signal.aborted || thread.append(h("div", { className: "error" }, String(err), " ", h("a", { href: location.pathname }, "Try again"))))
  return [log, h("div", { className: "agent-bar" }, h("span"), stop, h("button", { className: "btn plain sm", onclick: post("retry") }, "Retry"))]
}

// composer is where the user writes to the agent. Enter sends, Shift+Enter is a new line.
export function composer(agent: string, placeholder: string): HTMLElement {
  const box = h("textarea", { rows: 1, required: true, placeholder })
  const send = h("button", { className: "btn icon send", ariaLabel: "Send" }, icon("arrow-up"))
  const form = h("form", { className: "composer" }, h("div", { className: "field" }, h("div", { className: "line" }, box, send)))
  form.onsubmit = async e => {
    e.preventDefault()
    send.disabled = true
    try {
      await api(`/agents/${agent}/records?via=web`, { text: box.value })
      box.value = ""
      acted()
    } catch (err) {
      alert(err)
    }
    send.disabled = false
  }
  box.onkeydown = e => {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return
    e.preventDefault()
    form.requestSubmit()
  }
  return form
}
