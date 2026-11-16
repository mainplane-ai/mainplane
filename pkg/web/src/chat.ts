// The chat of one agent: every session it has had reads as one conversation.

import { type Agent, type Rec, acted, api, get, h, records, text } from "./api"

const shown = (r: Rec) => (r.type?.startsWith("text/") ? text(r.body) : `[${r.type}, ${r.len} bytes]`)

// fold is a line with more behind it.
const fold = (label: string, ...more: string[]) => h("details", null, h("summary", null, label), ...more.map(m => h("pre", null, m)))

// chat follows the agent's live session with one long poll, which stops
// while the page is hidden and ends with signal. Earlier sessions load on request.
export function chat(id: string, signal: AbortSignal): HTMLElement[] {
  const state = h("span")
  const post = (what: string) => () => api(`/agents/${id}/${what}`, { method: "POST" }).then(acted, alert)
  const log = h("div", { className: "scroll" })
  const calls = new Map<string, HTMLElement>()

  const draw = (recs: Rec[], into: HTMLElement) => {
    const bottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40
    for (const r of recs) {
      if (r.kind === "message" && r.via === "app:project") into.append(fold("Project created", shown(r)))
      else if (r.kind === "message" && r.via === "rollover") into.append(fold("The task, carried from the session before", shown(r)))
      else if (r.kind === "message") into.append(h("div", { className: "message" }, h("small", null, r.via ?? ""), shown(r)))
      else if (r.kind === "text") into.append(h("div", { className: "text" }, shown(r)))
      else if (r.kind === "error") into.append(h("div", { className: "error" }, shown(r)))
      else if (r.kind === "system" && r.source === "continue" && r.n > 1 && !/^[0-9a-f]{8}\s*$/.test(shown(r))) into.append(h("hr"), fold("New session", shown(r)))
      else if (r.kind === "call") {
        const c = JSON.parse(text(r.body))
        const d = fold(c.name, typeof c.arguments === "string" ? c.arguments : JSON.stringify(c.arguments, null, 1))
        calls.set(r.id, d)
        into.append(d)
      } else if (r.kind === "result") calls.get(r.for ?? "")?.append(h("pre", null, shown(r)))
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
    const more = h("button", { hidden: !earlier.length }, "Load the session before")
    more.onclick = async () => {
      const part = h("div")
      draw(await records(earlier.pop()!, 0, "", signal), part)
      more.after(part, h("hr"))
      more.hidden = !earlier.length
    }
    const live = h("div")
    log.append(more, live)
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
      state.textContent = `${agent.name || agent.id}: ${agent.status} `
      if (agent.sessions.at(-1) !== session) {
        draw(await records(session, n, "", signal), live)
        session = agent.sessions.at(-1)!
        n = 0
      }
    }
  }
  follow().catch(err => signal.aborted || log.append(h("div", { className: "error" }, String(err), " ", h("a", { href: location.pathname }, "Try again"))))
  return [h("div", null, state, h("button", { onclick: post("stop?via=web") }, "Stop"), h("button", { onclick: post("retry") }, "Retry")), log]
}

// composer is where the user writes to the agent. Enter sends, Shift+Enter is a new line.
export function composer(agent: string, placeholder: string): HTMLElement {
  const box = h("textarea", { rows: 2, required: true, placeholder })
  const send = h("button", null, "Send")
  const form = h("form", { className: "composer" }, box, send)
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
