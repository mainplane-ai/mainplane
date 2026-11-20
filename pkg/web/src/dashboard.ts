// A project's dashboard: a page its agents made, shown where it can reach
// neither the key nor the network. It asks this page for what it needs.

import { type Project, acted, api, file, get, h, text } from "./api"

const frameURL = new URL("frame.html", import.meta.url).href
type Stored = { name: string; size: number; modified: string; body?: string }

// system puts the design system a dashboard links inside its page: the frame
// has an origin of its own and so no cache, and would download both files at
// every visit. This page has them in its cache, and reads them once.
const ds = "/web/ds/1/"
let linked: Promise<string[]> | undefined
async function system(html: string): Promise<string> {
  linked ??= Promise.all(["mainplane.css", "mainplane.js"].map(async f => (await fetch(ds + f, { cache: "force-cache" })).text()))
  const [css, js] = await linked
  return html
    .replace(`<link rel="stylesheet" href="${ds}mainplane.css">`, () => `<style>${css.replaceAll('url("./', `url("${ds}`)}</style>`) // a font is still a file
    .replace(`<script src="${ds}mainplane.js"></script>`, () => `<script>${js.replaceAll("</script", "<\\/script")}</script>`)
}

// dashboard shows the store's index.html in a sandboxed frame and answers
// its calls until signal ends. When the store changes, a changed page is
// loaded again and an unchanged one is told, so it lists its files again.
export function dashboard(p: Project, signal: AbortSignal): HTMLIFrameElement {
  const frame = h("iframe", { src: frameURL, className: "dash" })
  frame.setAttribute("sandbox", "allow-scripts")
  const files = `/projects/${p.id}/files/`
  const tell = (m: unknown, transfer: Transferable[] = []) => frame.contentWindow?.postMessage(m, "*", transfer) // "*": the frame's origin has no name
  let page: string | undefined, ready = false, counter = p.counter
  // The store's list comes with what is in its small files, so a page and its data are one call.
  let store: Promise<Stored[]>
  const read = async (name: string) => {
    const f = (await store).find(f => f.name === name)
    return f?.body === undefined ? file(p.id, name, signal) : Uint8Array.from(atob(f.body), c => c.charCodeAt(0)).buffer
  }

  const write = () => ready && page !== undefined && tell({ html: page })
  const load = async () => {
    store = get<Stored[]>(files + "?bodies=1", signal)
    const next = await system(text(new Uint8Array(await read("index.html"))))
    if (next === page) return tell({ event: "changed" })
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

  const answer = async (d: Record<string, unknown>): Promise<unknown> => {
    switch (d.call) {
      case "list":
        return (await store).filter(f => f.name.startsWith(String(d.prefix ?? ""))).map(f => ({ name: f.name, size: f.size, modified: f.modified }))
      case "read":
        return read(String(d.name))
      case "send":
        await api(`/agents/${p.orchestrator}/records?via=web`, { text: String(d.text) })
        return acted()
      case "status":
        await api(`/projects/${p.id}/status?via=web`, { method: "PUT", json: { status: d.status, line: d.line ?? "" } })
        return acted()
    }
    throw new Error(`no call "${d.call}": there is list, read, send and status`)
  }
  addEventListener("message", async e => {
    const d = e.data
    if (e.source !== frame.contentWindow || typeof d !== "object" || d === null) return
    if (d.ready === true) {
      ready = true
      write()
    } else if (typeof d.call === "string") {
      try {
        const result = await answer(d)
        tell({ id: d.id, result }, result instanceof ArrayBuffer ? [result] : [])
      } catch (err) {
        tell({ id: d.id, error: err instanceof Error ? err.message : String(err) })
      }
    }
  }, { signal })
  return frame
}
