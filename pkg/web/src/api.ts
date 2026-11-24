// The harness's API as the page calls it. The key goes in a header of each
// call and is kept in this origin's storage: no cookie holds it.

export type Project = { id: string; name: string; orchestrator: string; status: string; line: string; color?: string; monogram?: string; updated: string; counter: number; working: boolean }
export type Conf = { model: string; workers: { name: string }[] | null; preset?: string; [more: string]: unknown }
export type Agent = { id: string; name?: string; config: Conf; apps: string[]; instructions?: string; sessions: string[]; status: string; updated: string }
export type Model = { id: string; name: string; provider: string; config: Conf; settings?: { name: string; values: { name: string; params: Record<string, unknown> }[] }[] }
export type Rec = { n: number; kind: string; id: string; len: number; type?: string; via?: string; source?: string; for?: string; time: string; body: Uint8Array }

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

type Options = { method?: string; json?: unknown; text?: string; form?: FormData; body?: BodyInit; signal?: AbortSignal }

// api is one call. An answer that is not a success is thrown as its text,
// and a refused key ends the login.
export async function api(path: string, o: Options = {}): Promise<Response> {
  const r = await fetch(path, {
    method: o.method ?? (o.json !== undefined || o.text !== undefined || o.form ? "POST" : "GET"),
    headers: { Authorization: "Bearer " + key(), ...(o.text !== undefined && { "Content-Type": "text/plain" }) },
    body: o.json !== undefined ? JSON.stringify(o.json) : (o.form ?? o.text ?? o.body),
    signal: o.signal,
    cache: "no-cache", // a file of a store is asked for again and answered 304 while it is the same
  })
  if (r.status === 401) logout(true)
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText)
  return r
}

export const get = async <T>(path: string, signal?: AbortSignal) => (await api(path, { signal })).json() as Promise<T>

// records is a session's records after record n.
export const records = async (session: string, after: number, signal?: AbortSignal) => decode(await api(`/sessions/${session}/records?after=${after}`, { signal }))

// decode is the records of an answer. Each is a line of JSON, then len bytes of body, then a newline.
export async function decode(r: Response): Promise<Rec[]> {
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

// named is a file's name that a dashboard gave: it must not walk out of the store.
export function named(name: unknown): string {
  if (typeof name !== "string" || !/^[\w.-]+(\/[\w.-]+)*$/.test(name) || name.split("/").some(s => s === "." || s === "..")) throw new Error(`file name "${name}"`)
  return name
}

// file is one file of a project's store. Four are read at once at most: a
// dashboard of a thousand cards asks for all of them together.
let free = 4
const queue: (() => void)[] = []
export async function file(project: string, name: string, signal?: AbortSignal): Promise<ArrayBuffer> {
  named(name)
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

// avatarColors are the palette tokens of pkg/design/tokens.json, in picker order.
export const avatarColors = ["slate", "sky", "indigo", "sage", "ochre", "terracotta", "rose", "plum"] as const
export type AvatarColor = (typeof avatarColors)[number]

// avatarColor is a project's saved color, or a steady one picked from its id.
export function avatarColor(p: Pick<Project, "id" | "color">): AvatarColor {
  if (p.color && (avatarColors as readonly string[]).includes(p.color)) return p.color as AvatarColor
  let hash = 0
  for (let i = 0; i < p.id.length; i++) hash = (hash * 31 + p.id.charCodeAt(i)) >>> 0
  return avatarColors[hash % avatarColors.length]
}

// avatar is the round monogram badge for a project.
export function avatar(p: Pick<Project, "id" | "name" | "color" | "monogram">): HTMLElement {
  const el = h("span", { className: "avatar" }, p.monogram || initials(p.name))
  el.dataset.color = avatarColor(p)
  return el
}

// saveAvatar stores a project's custom color and monogram (empty resets each to its default).
export const saveAvatar = async (id: string, color: string, monogram: string) =>
  (await api(`/projects/${id}/avatar`, { method: "PUT", json: { color, monogram } })).json() as Promise<Project>
