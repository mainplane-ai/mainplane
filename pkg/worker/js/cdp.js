// A Chrome DevTools Protocol client. cdp.md beside it says what it does.
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

const here = path.dirname(fileURLToPath(import.meta.url))
const logs = path.join(here, "log")
// Every wait has one default: long enough for a slow page, short enough that
// a wait for something that will not come costs little.
const wait = 30_000
// A string longer than twice this is cut in the middle in the log. A
// screenshot's base64 or a page's HTML is megabytes; its ends say what it was.
const keep = 1000

// Domains whose events Chrome sends to a session only after <Domain>.enable
// on that session.
const gated = new Set([
  "Accessibility", "Animation", "Audits", "CSS", "Console", "DOM", "DOMStorage", "Debugger", "Fetch",
  "HeapProfiler", "Inspector", "LayerTree", "Log", "Media", "Network", "Overlay", "Page", "Profiler",
  "Runtime", "Security", "ServiceWorker",
])

// gate is the call that makes Chrome send event, or undefined when none does.
function gate(event) {
  const [domain, name] = event.split(".")
  if (domain === "Target" && ["targetCreated", "targetDestroyed", "targetInfoChanged", "targetCrashed"].includes(name)) return "Target.setDiscoverTargets"
  if (domain === "Browser" && name.startsWith("download")) return "Browser.setDownloadBehavior"
  if (event === "Page.lifecycleEvent") return "Page.setLifecycleEventsEnabled"
  if (gated.has(domain)) return `${domain}.enable`
}

// switched is whether a call that succeeded turned a gate on or off, and which.
function switched(method, params) {
  const [domain, name] = method.split(".")
  if (name === "enable" || name === "disable") return [`${domain}.enable`, name === "enable"]
  if (method === "Target.setDiscoverTargets") return [method, !!params.discover]
  if (method === "Browser.setDownloadBehavior") return [method, !!params.eventsEnabled]
  if (method === "Page.setLifecycleEventsEnabled") return [method, !!params.enabled]
}

function cut(v) {
  if (typeof v === "string") return v.length > 2 * keep ? `${v.slice(0, keep)}...[${v.length - 2 * keep} chars cut]...${v.slice(-keep)}` : v
  if (Array.isArray(v)) return v.map(cut)
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, cut(x)]))
  return v
}

export class CdpError extends Error {
  constructor(method, e) {
    super(`${method}: CDP error ${e.code}: ${e.message}${e.data ? ` (${e.data})` : ""}`)
    this.name = "CdpError"
    this.method = method
    this.code = e.code
    this.data = e.data
  }
}

let opened = 0

// connect opens a WebSocket to a DevTools endpoint and returns a Connection.
export async function connect(url, { timeoutMs = wait } = {}) {
  const ws = new WebSocket(url)
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      ws.close()
      reject(new Error(`connect ${url}: no WebSocket open in ${timeoutMs} ms`))
    }, timeoutMs)
    ws.onopen = () => {
      clearTimeout(timer)
      resolve()
    }
    ws.onerror = (e) => {
      clearTimeout(timer)
      reject(new Error(`connect ${url}: ${e.message ?? "WebSocket error"}`))
    }
  })
  return new Connection(ws, url)
}

export class Connection {
  #ws
  #fd
  #next = 1
  #pending = new Map() // id -> {sessionId, method, params, seen, resolve, reject, timer}
  #late = new Set() // ids whose send timed out
  #listeners = new Set() // {sessionId, method, fn, end}
  #gates = new Map() // sessionId ?? "" -> Set of gates on
  #detached = new Map() // sessionId -> why
  #dialogs = new Map() // sessionId -> Page.javascriptDialogOpening params
  #closed

  constructor(ws, url) {
    this.url = url
    fs.mkdirSync(logs, { recursive: true })
    this.log = path.join(logs, `${new Date().toISOString().replace(/[:.]/g, "-")}-${process.pid}-${++opened}.jsonl`)
    this.#fd = fs.openSync(this.log, "a")
    this.#ws = ws
    ws.onmessage = (e) => this.#receive(JSON.parse(String(e.data)))
    ws.onclose = (e) => this.#close(`closed (code ${e.code}${e.reason ? `, ${e.reason}` : ""})`)
  }

  send(method, params = {}, { timeoutMs = wait, sessionId } = {}) {
    const stop = this.#closed ?? (sessionId && this.#detached.get(sessionId))
    if (stop) return Promise.reject(new Error(`${method}: ${stop}`))
    const id = this.#next++
    const msg = { id, method, params, ...(sessionId && { sessionId }) }
    this.#write({ t: Date.now(), dir: "out", ...msg })
    this.#ws.send(JSON.stringify(msg))
    return new Promise((resolve, reject) => {
      const p = { sessionId, method, params, seen: [], resolve, reject }
      p.timer = setTimeout(() => {
        this.#pending.delete(id)
        this.#late.add(id)
        reject(new Error(`${method} (id ${id}) got no answer in ${timeoutMs} ms. ${this.#story(sessionId, p.seen)}An answer that comes later is in ${this.log} with "late":true.`))
      }, timeoutMs)
      this.#pending.set(id, p)
    })
  }

  waitFor(method, { predicate = () => true, timeoutMs = wait, since, sessionId } = {}) {
    const stop = this.#closed ?? (sessionId && this.#detached.get(sessionId))
    if (stop) return Promise.reject(new Error(`waitFor ${method}: ${stop}`))
    return new Promise((resolve, reject) => {
      let timer
      let settled = false
      const seen = []
      const done = (err, v) => {
        settled = true
        clearTimeout(timer)
        this.#listeners.delete(l)
        err ? reject(err) : resolve(v)
      }
      const test = (params) => {
        if (settled) return false
        try {
          return predicate(params)
        } catch (e) {
          done(e)
        }
      }
      const l = {
        sessionId,
        method: "*",
        fn: (params, m) => (m === method && test(params) ? done(null, params) : seen.push(m === method ? `${m} (predicate false)` : m)),
        end: (why) => done(new Error(`waitFor ${method}: ${why}`)),
      }
      this.#listeners.add(l)
      if (since !== undefined) {
        const hit = this.#search(sessionId, method, since, test)
        if (settled) return
        if (hit) return done(null, hit.params)
      }
      const g = gate(method)
      if (g && !this.#gates.get(sessionId ?? "")?.has(g)) {
        return done(new Error(`waitFor ${method}: Chrome sends it only after ${g} on the same ${sessionId ? "target" : "connection"}, and this one has not sent it, so it would never arrive.`))
      }
      timer = setTimeout(() => done(new Error(`waitFor ${method}: none in ${timeoutMs} ms${since !== undefined ? `, nor in the log since ${since}` : ""}. ${this.#story(sessionId, seen)}`)), timeoutMs)
    })
  }

  on(method, fn, { sessionId } = {}) {
    const l = { sessionId, method, fn }
    this.#listeners.add(l)
    return () => this.#listeners.delete(l)
  }

  async attach(targetId, { timeoutMs = wait } = {}) {
    const r = await this.send("Target.attachToTarget", { targetId, flatten: true }, { timeoutMs })
    return new Target(this, targetId, r.sessionId)
  }

  close() {
    this.#ws.close()
  }

  [Symbol.for("nodejs.util.inspect.custom")]() {
    return `Connection ${this.url}${this.#closed ? ` ${this.#closed}` : ""}, log ${this.log}`
  }

  #receive(m) {
    const late = m.id !== undefined && this.#late.delete(m.id)
    this.#write({ t: Date.now(), dir: "in", ...(late && { late }), ...m })
    if (m.id !== undefined) {
      const p = this.#pending.get(m.id)
      if (!p) return
      this.#pending.delete(m.id)
      clearTimeout(p.timer)
      if (m.error) return p.reject(new CdpError(p.method, m.error))
      const s = switched(p.method, p.params)
      if (s) {
        const key = p.sessionId ?? ""
        if (!this.#gates.has(key)) this.#gates.set(key, new Set())
        s[1] ? this.#gates.get(key).add(s[0]) : this.#gates.get(key).delete(s[0])
      }
      return p.resolve(m.result)
    }
    const sid = m.sessionId
    if (m.method === "Page.javascriptDialogOpening") this.#dialogs.set(sid, m.params)
    if (m.method === "Page.javascriptDialogClosed") this.#dialogs.delete(sid)
    for (const p of this.#pending.values()) if (p.sessionId === sid) p.seen.push(m.method)
    for (const l of this.#listeners) {
      if (l.sessionId !== sid || (l.method !== "*" && l.method !== m.method)) continue
      try {
        l.fn(m.params, m.method)
      } catch (e) {
        console.error(`listener for ${m.method} threw: ${e?.stack ?? e}`)
      }
    }
    if (m.method === "Target.detachedFromTarget") this.#end(m.params.sessionId, `target ${m.params.targetId} detached (closed, crashed, or detached by a call)`)
  }

  // #end fails everything waiting on sessionId, or on the whole connection
  // when sessionId is undefined.
  #end(sessionId, why) {
    if (sessionId) this.#detached.set(sessionId, why)
    for (const [id, p] of this.#pending) {
      if (sessionId && p.sessionId !== sessionId) continue
      this.#pending.delete(id)
      clearTimeout(p.timer)
      p.reject(new Error(`${p.method}: ${why}`))
    }
    for (const l of this.#listeners) if (l.end && (!sessionId || l.sessionId === sessionId)) l.end(why)
  }

  #close(why) {
    this.#closed = `connection ${this.url} ${why}`
    this.#end(undefined, this.#closed)
    fs.closeSync(this.#fd)
  }

  #write(m) {
    if (!this.#closed) fs.writeSync(this.#fd, JSON.stringify(cut(m)) + "\n")
  }

  // #search is the first event in the log since t that matches.
  #search(sessionId, method, t, test) {
    for (const line of fs.readFileSync(this.log, "utf8").split("\n")) {
      if (!line.includes(`"${method}"`)) continue
      const m = JSON.parse(line)
      if (m.dir === "in" && m.method === method && m.sessionId === sessionId && m.t >= t && test(m.params)) return m
    }
  }

  // #story is what a timeout should know: what did arrive, and what blocks.
  #story(sessionId, seen) {
    const counts = Object.entries(Object.groupBy(seen, (m) => m)).map(([m, a]) => (a.length > 1 ? `${m} x${a.length}` : m))
    const d = this.#dialogs.get(sessionId)
    return [
      counts.length ? `Events that arrived on this ${sessionId ? "target" : "connection"} meanwhile: ${counts.join(", ")}.` : `No events arrived on this ${sessionId ? "target" : "connection"} meanwhile.`,
      d && `A JavaScript ${d.type} is open on it ("${d.message}"); page JavaScript waits until Page.handleJavaScriptDialog.`,
    ].filter(Boolean).join(" ") + " "
  }
}

export class Target {
  constructor(connection, targetId, sessionId) {
    this.connection = connection
    this.targetId = targetId
    this.sessionId = sessionId
  }
  send(method, params, opts) {
    return this.connection.send(method, params, { ...opts, sessionId: this.sessionId })
  }
  waitFor(method, opts) {
    return this.connection.waitFor(method, { ...opts, sessionId: this.sessionId })
  }
  on(method, fn) {
    return this.connection.on(method, fn, { sessionId: this.sessionId })
  }
  detach() {
    return this.connection.send("Target.detachFromTarget", { sessionId: this.sessionId })
  }
  [Symbol.for("nodejs.util.inspect.custom")]() {
    return `Target ${this.targetId}, session ${this.sessionId}, on ${this.connection.url}`
  }
}

// findBrowsers reads DevToolsActivePort in every user data dir this OS's
// Chromium browsers use by default, and in each dir under <scratch>/browsers.
// It opens no connection.
export function findBrowsers() {
  const home = os.homedir()
  const local = process.env.LOCALAPPDATA ?? path.join(home, "AppData", "Local")
  const roaming = process.env.APPDATA ?? path.join(home, "AppData", "Roaming")
  const mac = path.join(home, "Library", "Application Support")
  const cfg = path.join(home, ".config")
  const dirs = {
    win32: [
      path.join(local, "Google", "Chrome", "User Data"), path.join(local, "Google", "Chrome Beta", "User Data"),
      path.join(local, "Google", "Chrome SxS", "User Data"), path.join(local, "Chromium", "User Data"),
      path.join(local, "Microsoft", "Edge", "User Data"), path.join(local, "BraveSoftware", "Brave-Browser", "User Data"),
      path.join(local, "Vivaldi", "User Data"), path.join(roaming, "Opera Software", "Opera Stable"),
    ],
    darwin: [
      path.join(mac, "Google", "Chrome"), path.join(mac, "Google", "Chrome Beta"), path.join(mac, "Google", "Chrome Canary"),
      path.join(mac, "Chromium"), path.join(mac, "Microsoft Edge"), path.join(mac, "BraveSoftware", "Brave-Browser"),
      path.join(mac, "Arc", "User Data"), path.join(mac, "Vivaldi"), path.join(mac, "com.operasoftware.Opera"), path.join(mac, "Comet"),
    ],
    linux: [
      path.join(cfg, "google-chrome"), path.join(cfg, "google-chrome-beta"), path.join(cfg, "google-chrome-unstable"),
      path.join(cfg, "chromium"), path.join(cfg, "microsoft-edge"), path.join(cfg, "BraveSoftware", "Brave-Browser"),
      path.join(cfg, "vivaldi"), path.join(cfg, "opera"),
    ],
  }[process.platform] ?? []
  const own = path.join(here, "..", "browsers")
  if (fs.existsSync(own)) dirs.push(...fs.readdirSync(own).map((d) => path.join(own, d)))
  return dirs.flatMap((userDataDir) => {
    const f = path.join(userDataDir, "DevToolsActivePort")
    if (!fs.existsSync(f)) return []
    const [port, p] = fs.readFileSync(f, "utf8").trim().split("\n")
    return [{ userDataDir, wsUrl: `ws://127.0.0.1:${port}${p}`, mtime: fs.statSync(f).mtime.toISOString() }]
  })
}
