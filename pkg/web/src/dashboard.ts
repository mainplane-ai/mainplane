// A project's dashboard: a page its agents made, shown where it cannot reach
// the key. It asks this page for what it needs of its project.

import { type Agent, type Project, acted, api, decode, file, get, h, icon, named, text } from "./api"

export const frameURL = new URL("frame.html", import.meta.url).href
type Stored = { name: string; size: number; modified: string; body?: string }
const bare = (f: Stored) => ({ name: f.name, size: f.size, modified: f.modified })
// tail is how many of an agent's last records a dashboard is told of: what the agent does now, not its history.
const tail = 20
const go = (path: string) => dispatchEvent(new CustomEvent("go", { detail: path })) // the page changes its view

// system puts the design system a dashboard links inside its page, fonts
// and all: the frame has an origin of its own and so no cache, and would
// download each file at every visit. This page has them in its cache, and
// reads them once.
const ds = "/web/ds/1/"
const cached = async (f: string) => fetch(ds + f, { cache: "force-cache" })
let linked: Promise<string[]> | undefined
async function system(html: string): Promise<string> {
  linked ??= (async () => {
    let [css, js] = await Promise.all(["mainplane.css", "mainplane.js"].map(async f => (await cached(f)).text()))
    for (const [url, font] of css.matchAll(/url\("\.\/(fonts\/[^"]+)"\)/g)) {
      const as = new FileReader()
      as.readAsDataURL(await (await cached(font)).blob())
      await new Promise(done => (as.onload = done))
      css = css.replace(url, `url("${as.result}")`)
    }
    return [css, js]
  })()
  const [css, js] = await linked
  return html
    .replace(`<link rel="stylesheet" href="${ds}mainplane.css">`, () => `<style>${css}</style>`)
    .replace(`<script src="${ds}mainplane.js"></script>`, () => `<script>${js.replaceAll("</script", "<\\/script")}</script>`)
}

// dashboard shows the store's index.html in a sandboxed frame and answers
// its calls until signal ends. When the store changes, a changed page is
// loaded again and an unchanged one is told, so it lists its files again.
// What is returned holds the frame and has no box of its own: hidden, it
// keeps the frame loaded.
export function dashboard(p: Project, signal: AbortSignal): HTMLElement {
  const frame = h("iframe", { src: frameURL, className: "dash" })
  frame.setAttribute("sandbox", "allow-scripts allow-popups allow-popups-to-escape-sandbox") // no allow-same-origin: the key is out of its reach
  const holder = h("div", null, frame)
  holder.style.display = "contents"
  const at = `/projects/${p.id}`, files = at + "/files/"
  const tell = (m: unknown, transfer: Transferable[] = []) => frame.contentWindow?.postMessage(m, "*", transfer) // "*": the frame's origin has no name
  let page: string | undefined, ready = false, counter = p.counter
  // The store's list comes with what is in its small files, so a page and its data are one call.
  let store: Promise<Stored[]>, was = new Map<string, string>()
  const read = async (name: string) => {
    const f = (await store).find(f => f.name === name)
    return f?.body === undefined ? file(p.id, name, signal) : Uint8Array.from(atob(f.body), c => c.charCodeAt(0)).buffer
  }

  const write = () => ready && page !== undefined && tell({ html: page, inset: [matchMedia("(max-width: 700px)").matches ? 60 : 0, 80] })
  const load = async () => {
    store = get<Stored[]>(files + "?bodies=1", signal)
    const next = await system(text(new Uint8Array(await read("index.html"))))
    const now = await store, old = was
    was = new Map(now.map(f => [f.name, f.modified + f.size]))
    // A page that stays is told which files are new or changed and which are gone.
    if (next === page) return tell({ event: "changed", changed: now.filter(f => old.get(f.name) !== was.get(f.name)).map(bare), deleted: [...old.keys()].filter(n => !was.has(n)) })
    const first = page === undefined
    page = next
    if (first) return write()
    ready = false
    frame.src = frameURL // the frame starts over and says it is ready
  }
  const show = (err: unknown) => signal.aborted || frame.replaceWith(h("p", { className: "error-line" }, String(err)))
  load().catch(show)

  addEventListener("projects", e => {
    const now = (e as CustomEvent<Project[]>).detail.find(q => q.id === p.id)
    if (!now || now.counter === counter) return
    counter = now.counter
    load().catch(show)
  }, { signal })

  // calls is all a dashboard can ask, and each stays in this project: the
  // paths have its id, a file's name does not leave its store, a message goes
  // to its orchestrator, and an agent opened is one of its agents.
  const calls: Record<string, (d: Record<string, unknown>) => unknown> = {
    list: async d => (await store).filter(f => f.name.startsWith(String(d.prefix ?? ""))).map(bare),
    read: d => read(String(d.name)),
    write: d => {
      if (typeof d.body !== "string" && !(d.body instanceof ArrayBuffer) && !(d.body instanceof Blob)) throw new Error("body: a string, an ArrayBuffer or a Blob")
      return api(files + named(d.name), { method: "PUT", body: d.body }).then(acted)
    },
    delete: d => api(files + named(d.name), { method: "DELETE" }).then(acted),
    send: d => api(`/agents/${p.orchestrator}/records?via=web`, { text: d.data === undefined ? String(d.text) : JSON.stringify(d.data) }).then(acted),
    status: d => api(at + "/status?via=web", { method: "PUT", json: { status: d.status, line: d.line ?? "" } }).then(acted),
    agents: async () => Promise.all((await get<Agent[]>(at + "/agents", signal)).map(async a => {
      const r = await api(`/agents/${a.id}/records?tail=${tail}`, { signal })
      const recent = (await decode(r)).filter(r => ["message", "text", "call", "error"].includes(r.kind)).map(r => ({ n: r.n, kind: r.kind, time: r.time, via: r.via, text: /^text\/|json/.test(r.type ?? "") ? text(r.body.subarray(0, 400)) : "" }))
      return { id: a.id, name: a.name, model: a.config.model, status: a.status, updated: a.updated, session: r.headers.get("Mainplane-Session"), recent }
    })),
    open: async d => {
      if (d.file !== undefined) return go(files + named(d.file))
      const a = (await get<Agent[]>(at + "/agents", signal)).find(a => a.id === d.agent)
      if (!a) throw new Error(`no agent "${d.agent}" in this project`)
      // With n, the chat opens at that record of the session, as agents tells them.
      return go((a.id === p.orchestrator ? at + "/chat" : "/agents/" + a.id) + (d.n === undefined ? "" : `#${a.sessions.includes(String(d.session)) ? d.session : a.sessions.at(-1)}.${Number(d.n)}`))
    },
    // reply puts a question's title over the chat box under the dashboard, and the cursor in the box.
    reply: d => void dispatchEvent(new CustomEvent("reply", { detail: { agent: p.orchestrator, title: String(d.title) } })),
  }
  addEventListener("message", async e => {
    const d = e.data
    if (e.source !== frame.contentWindow || typeof d !== "object" || d === null) return
    if (d.image instanceof ImageBitmap) lightbox(d.image, String(d.alt ?? ""))
    else if (Array.isArray(d.swipe)) dispatchEvent(new CustomEvent("swipe", { detail: d.swipe.map(Number) }))
    else if (d.ready === true) {
      ready = true
      write()
    } else if (typeof d.call === "string") {
      try {
        if (d.project !== undefined && d.project !== p.id) throw new Error("a dashboard reaches its own project only")
        if (!Object.hasOwn(calls, d.call)) throw new Error(`no call "${d.call}": there is ${Object.keys(calls).join(", ")}`)
        const result = await calls[d.call](d)
        tell({ id: d.id, result }, result instanceof ArrayBuffer ? [result] : [])
      } catch (err) {
        tell({ id: d.id, error: err instanceof Error ? err.message : String(err) })
      }
    }
  }, { signal })
  return holder
}

// lightbox shows a dashboard's picture large over everything: whole, or as wide as it fits and scrolled when it is tall.
// Escape, the X or a click beside the picture closes it.
function lightbox(b: ImageBitmap, alt: string) {
  const pic = h("canvas", { width: b.width, height: b.height, role: "img", ariaLabel: alt || "Picture" })
  pic.getContext("bitmaprenderer")!.transferFromImageBitmap(b)
  const close = () => { box.remove(); removeEventListener("keydown", esc) }
  const esc = (e: KeyboardEvent) => { if (e.key === "Escape") close() }
  const x = h("button", { className: "btn icon", ariaLabel: "Close", onclick: close }, icon("close"))
  const box = h("div", { className: "lightbox", role: "dialog", ariaModal: "true", ariaLabel: alt || "Picture", onclick: e => e.target !== pic && close() }, pic, x)
  box.classList.toggle("tall", pic.height > 3 * pic.width) // taller than a phone's screen: a page, which shown whole is too small to read
  addEventListener("keydown", esc)
  document.body.append(box)
  x.focus() // the click left the focus in the dashboard's frame, where Escape does not reach this page
}
