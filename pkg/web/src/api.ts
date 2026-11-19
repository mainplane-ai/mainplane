// The harness's API as the page calls it. The key goes in a header of each
// call and is kept in this origin's storage: no cookie holds it.

export type Project = { id: string; name: string; orchestrator: string; status: string; line: string; updated: string; counter: number; working: boolean }
export type Conf = { model: string; workers: { name: string }[]; preset?: string; [more: string]: unknown }
export type Agent = { id: string; name?: string; config: Conf; apps: string[]; instructions?: string; sessions: string[]; status: string }
export type Model = { id: string; name: string; config: Conf }
export type Rec = { n: number; kind: string; id: string; len: number; type?: string; via?: string; source?: string; for?: string; body: Uint8Array }

const saved = "mainplane-key"

export const key = () => localStorage.getItem(saved) ?? ""

// login keeps the secret of a key however it was pasted: alone, or inside
// the token the CLI logs in with, mp_key_<harness>.<secret>@<url>.
export function login(k: string) {
  localStorage.setItem(saved, k.trim().replace(/^mp_key_[^.]*\./, "").replace(/@.*$/, ""))
}

export function logout(refused = false) {
  localStorage.removeItem(saved)
  if (refused) sessionStorage.setItem("refused", "1")
  location.reload()
}

type Options = { method?: string; json?: unknown; text?: string; signal?: AbortSignal }

// api is one call. An answer that is not a success is thrown as its text,
// and a refused key ends the login.
export async function api(path: string, o: Options = {}): Promise<Response> {
  const r = await fetch(path, {
    method: o.method ?? (o.json !== undefined || o.text !== undefined ? "POST" : "GET"),
    headers: { Authorization: "Bearer " + key(), ...(o.text !== undefined && { "Content-Type": "text/plain" }) },
    body: o.json !== undefined ? JSON.stringify(o.json) : o.text,
    signal: o.signal,
    cache: "no-cache", // a file of a store is asked for again and answered 304 while it is the same
  })
  if (r.status === 401) logout(true)
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText)
  return r
}

export const get = async <T>(path: string, signal?: AbortSignal) => (await api(path, { signal })).json() as Promise<T>

// records is a session's records after record n: with wait, held open until
// there is one. Each is a line of JSON, then len bytes of body, then a newline.
export async function records(session: string, after: number, wait: string, signal?: AbortSignal): Promise<Rec[]> {
  const r = await api(`/sessions/${session}/records?after=${after}${wait && "&wait=" + wait}`, { signal })
  const buf = new Uint8Array(await r.arrayBuffer()), out: Rec[] = []
  for (let i = 0; i < buf.length; ) {
    const end = buf.indexOf(10, i)
    const head = JSON.parse(text(buf.subarray(i, end)))
    out.push({ ...head, body: buf.subarray(end + 1, end + 1 + head.len) })
    i = end + head.len + 2
  }
  return out
}

export const text = (b: Uint8Array) => new TextDecoder().decode(b)

// file is one file of a project's store. Four are read at once at most: a
// dashboard of a thousand cards asks for all of them together.
let free = 4
const queue: (() => void)[] = []
export async function file(project: string, name: string, signal?: AbortSignal): Promise<ArrayBuffer> {
  // The name comes from a dashboard: it must not walk out of the store.
  if (!/^[\w.-]+(\/[\w.-]+)*$/.test(name) || name.split("/").some(s => s === "." || s === "..")) throw new Error(`file name "${name}"`)
  if (free > 0) free--
  else await new Promise<void>(go => queue.push(go))
  try {
    return await (await api(`/projects/${project}/files/${name}`, { signal })).arrayBuffer()
  } finally {
    const next = queue.shift()
    if (next) next()
    else free++
  }
}

// h is an element with its properties and children. Text is always a text
// node: nothing a model or a dashboard wrote is parsed as HTML.
export function h<K extends keyof HTMLElementTagNameMap>(tag: K, props?: Partial<HTMLElementTagNameMap[K]> | null, ...kids: (Node | string)[]): HTMLElementTagNameMap[K] {
  const e = Object.assign(document.createElement(tag), props)
  e.append(...kids)
  return e
}

// acted tells the page that the user did something an answer may follow:
// the list of projects is read again, and a chat looks for its next records.
export const acted = () => dispatchEvent(new Event("acted"))

// icon is a design-system icon by name.
export function icon(name: string) {
  const i = document.createElement("mp-icon")
  i.setAttribute("name", name)
  return i
}

// initials stand for a name in an avatar.
export const initials = (name: string) => name.split(/\s+/).filter(Boolean).slice(0, 2).map(w => w[0].toUpperCase()).join("") || "?"
