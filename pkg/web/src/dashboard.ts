// A project's dashboard: a page its agents made, shown where it can reach
// neither the key nor the network. It asks this page for what it needs.

import { type Project, acted, api, file, get, h } from "./api"

const frameURL = new URL("frame.html", import.meta.url).href

// dashboard shows the store's index.html in a sandboxed frame and answers
// its calls until signal ends. When the store changes, a changed page is
// loaded again and an unchanged one is told, so it lists its files again.
export function dashboard(p: Project, signal: AbortSignal): HTMLIFrameElement {
  const frame = h("iframe", { src: frameURL })
  frame.setAttribute("sandbox", "allow-scripts")
  const files = `/projects/${p.id}/files/`
  const tell = (m: unknown, transfer: Transferable[] = []) => frame.contentWindow?.postMessage(m, "*", transfer) // "*": the frame's origin has no name
  let page: string | undefined, ready = false, counter = p.counter

  const write = () => ready && page !== undefined && tell({ html: page })
  const load = async () => {
    const next = await (await api(files + "index.html", { signal })).text()
    if (next === page) return tell({ event: "changed" })
    const first = page === undefined
    page = next
    if (first) return write()
    ready = false
    frame.src = frameURL // the frame starts over and says it is ready
  }
  const show = (err: unknown) => signal.aborted || frame.replaceWith(h("p", { className: "error" }, String(err)))
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
        return get(`${files}?prefix=${encodeURIComponent(String(d.prefix ?? ""))}`, signal)
      case "read":
        return file(p.id, String(d.name), signal)
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
