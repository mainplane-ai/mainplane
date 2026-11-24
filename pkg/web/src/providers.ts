// The Providers tab of Settings: what is connected, and Add Provider (the terminal app's /providers).
// Its pages push inside the tab (iOS Settings > Mail > Accounts): the list, Add Provider, then a key,
// a sign-in or a local server, and a provider's page with its models and Disconnect.

import { type Model, api, get, h, icon } from "./api"

type Info = { name: string; shape: string; url: string; context_limit?: number }
type Cat = { name: string; shape: string; url?: string; session?: string }
type Offer = { id: string; name: string; section: string; sign_in?: boolean; keyed: Record<string, Cat> }
type Login = { state: string; error?: string; text: string; url: string; paste?: boolean }
// Connected is a provider as the list shows it: a catalog one (its group once), or the user's own server.
type Connected = { name: string; offer?: Offer; names: string[]; info: Info }

// keyPages are where a provider's key is made.
const keyPages: Record<string, string> = {
  anthropic: "https://console.anthropic.com/settings/keys",
  openai: "https://platform.openai.com/api-keys",
  google: "https://aistudio.google.com/apikey",
  openrouter: "https://openrouter.ai/keys",
  "opencode-go": "https://opencode.ai/auth",
}

const fail = (e: unknown) => alert(e instanceof Error ? e.message : e)
const nav = (label: string, sub: string, onclick: () => void, lead?: HTMLElement) =>
  h("button", { className: "form-row nav", type: "button", onclick }, ...(lead ? [lead] : []),
    h("span", { className: "row-main" }, h("b", null, label), ...(sub ? [h("small", null, sub)] : [])), Object.assign(icon("chevron-right"), { className: "chev" }))
const field = (label: string, input: HTMLInputElement) => h("label", { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, label)), input)
const input = (props: Partial<HTMLInputElement>) => h("input", { className: "input", autocomplete: "off", spellcheck: false, ...props })
const group = (label: string, ...rows: HTMLElement[]) => [h("p", { className: "section-l", hidden: !rows.length }, label), h("div", { className: "form", hidden: !rows.length }, ...rows)]
const hostOf = (url: string) => { try { return new URL(url).host } catch { return url } }

// Host is a flow that shows the pages in its own frame (the onboarding): from Add Provider on, with its own
// Back and its own button for each page's go, and done once a provider is connected.
export type Host = { show: (title: string, back: (() => void) | null, nodes: Node[], go: HTMLButtonElement | null) => void; done: () => void }

export function providersPanel(host?: Host): HTMLElement {
  const panel = h("div", { className: "providers" })
  let offers: Offer[] = []
  let infos: Info[] = []

  // show puts a page in the tab; a page under the list has the way back to the page it came from. A page's go
  // button (Connect) goes under it, gray until what it needs is filled in (iOS).
  const show = (title: string, back: (() => void) | null, nodes: Node[], go: HTMLButtonElement | null = null) => {
    if (host) return host.show(title, back, nodes, go)
    panel.replaceChildren(...(back ? [h("div", { className: "page-head" },
      h("button", { className: "btn plain sm up", type: "button", onclick: back }, icon("chevron-left"), "Back"), h("h3", null, title))] : []), ...nodes,
      ...(go ? [h("div", { className: "actions" }, go)] : []))
    panel.closest(".body")?.scrollTo(0, 0)
  }
  const connect = (form: HTMLFormElement, ready: () => boolean) => {
    const go = h("button", { className: "btn filled", type: "button", disabled: true, onclick: () => form.requestSubmit() }, "Connect")
    form.oninput = () => (go.disabled = !ready())
    return go
  }
  const succeed = () => (host ? host.done() : void reload())
  const load = async () => ([offers, infos] = await Promise.all([get<Offer[]>("/catalog/providers"), get<Info[]>("/providers")]))
  const connected = (): Connected[] => {
    const out: Connected[] = []
    for (const info of infos) {
      const offer = offers.find(o => info.name in o.keyed)
      const had = offer && out.find(c => c.offer === offer)
      if (had) had.names.push(info.name)
      else out.push({ name: offer?.name ?? info.name, offer, names: [info.name], info })
    }
    return out
  }
  const kind = (c: Connected) => (c.offer ? (c.offer.sign_in ? "Subscription" : "API key") : "Local server · " + hostOf(c.info.url))

  // list is the tab's first page: the connected providers, then Add Provider.
  const list = () => {
    const cs = connected()
    show("", null, [...(cs.length ? group("Connected", ...cs.map(c => nav(c.name, kind(c), () => detail(c)))) : [h("div", { className: "empty compact" }, h("h3", null, "No Providers"), h("p", null, "Connect a provider to use its models."))]),
      h("div", { className: "form add" }, nav("Add Provider", "", add, icon("plus")))])
  }
  const reload = async () => { await load(); list() }

  // add is the catalog in the terminal app's sections, with search, then the user's own server.
  const add = () => {
    const q = h("input", { type: "search", placeholder: "Search", ariaLabel: "Search providers" })
    const sections = h("div")
    const draw = () => {
      const s = q.value.trim().toLowerCase()
      const shown = offers.filter(o => o.name.toLowerCase().includes(s))
      const names = [...new Set(shown.map(o => o.section))]
      const local = !s || "local server ollama lm studio vllm".includes(s)
      sections.replaceChildren(...names.flatMap(n => group(n, ...shown.filter(o => o.section === n).map(o =>
        nav(o.name, Object.keys(o.keyed).some(k => infos.some(i => i.name === k)) ? "Connected" : "", () => (o.sign_in ? signin(o) : keyPage(o)))))),
        ...group("Your Own", ...(local ? [nav("Local Server", "Ollama, LM Studio, vLLM", localPage)] : [])))
    }
    q.oninput = draw
    draw()
    show("Add Provider", host ? null : list, [h("label", { className: "search lg" }, icon("search"), q), sections])
  }

  // keyPage asks for a key (Bedrock: and its region) and sets it on each provider of the group.
  const keyPage = (o: Offer) => {
    const k = input({ type: "password", placeholder: "Required", ariaLabel: "API key", autocomplete: "new-password" })
    const region = input({ placeholder: "us-east-1", ariaLabel: "Region" })
    const bedrock = Object.values(o.keyed).some(p => p.shape === "bedrock")
    const page = keyPages[o.id]
    const form = h("form", null, h("div", { className: "form" }, field("API Key", k), ...(bedrock ? [field("Region", region)] : [])),
      h("p", { className: "note" }, "The key is kept on your harness only. ", ...(page ? ["Get one at ", h("a", { href: page, target: "_blank", rel: "noopener" }, hostOf(page)), "."] : [])))
    const go = connect(form, () => !!k.value.trim())
    form.onsubmit = e => {
      e.preventDefault()
      if (!k.value.trim()) return k.focus()
      go.disabled = true
      Promise.all(Object.entries(o.keyed).map(([id, p]) => api("/providers/" + id, { method: "PUT", json: { shape: p.shape, url: p.url, session: p.session, key: k.value.trim(), region: region.value.trim() || (bedrock ? "us-east-1" : "") } })))
        .then(succeed, e => { go.disabled = false; fail(e) })
    }
    show("Key for " + o.name, add, [form], go)
    k.focus()
  }

  // signin runs a subscription's sign-in: open the vendor's page, then paste back what it ends on.
  // The words tell what each kind of ending looks like: a code on the vendor's page, or a page that does not load.
  const signin = (o: Offer) => {
    const [id, p] = Object.entries(o.keyed)[0]
    const status = h("p", { className: "note" }, "Starting the sign-in...")
    const steps = h("ol", { className: "steps" })
    const paste = input({ ariaLabel: "Pasted text" })
    const open = h("a", { className: "btn filled open", target: "_blank", rel: "noopener", hidden: true })
    const form = h("form", { hidden: true }, h("div", { className: "form" }, h("div", { className: "form-row" }, paste)))
    const go = connect(form, () => !!paste.value.trim())
    let live = true
    const end = (st: Login) => {
      live = false
      if (st.state === "done") return succeed()
      status.textContent = st.error ?? "The sign-in failed."
      form.hidden = open.hidden = go.hidden = true
      steps.replaceChildren()
      status.after(h("div", { className: "actions" }, h("button", { className: "btn", type: "button", onclick: () => signin(o) }, "Try Again")))
    }
    // The harness may end it with no paste (a browser on its own machine): watch it the whole time.
    const watch = async () => {
      while (live && steps.isConnected) {
        await new Promise(t => setTimeout(t, 1500))
        const st = await get<Login>("/providers/" + id + "/login").catch(() => null)
        if (live && st && st.state !== "pending") end(st)
      }
    }
    form.onsubmit = e => {
      e.preventDefault()
      const text = paste.value.trim()
      if (!text) return paste.focus()
      go.disabled = true
      status.textContent = "Signing in..."
      api("/providers/" + id + "/login/code", { json: { code: text } }).catch(e => { go.disabled = false; status.textContent = ""; fail(e) })
    }
    show("Sign in to " + o.name, add, [steps, open, form, status], go)
    api("/providers/" + id + "/login", { json: { shape: p.shape, url: p.url } }).then(r => r.json() as Promise<Login>).then(st => {
      if (st.state !== "pending") return end(st)
      const loopback = new URL(st.url).searchParams.get("redirect_uri")?.startsWith("http://")
      const vendor = o.name.split(" ")[0]
      // The steps come first, so they are read before the person leaves for the vendor's site.
      Object.assign(open, { href: st.url, textContent: "Open " + vendor, hidden: false })
      steps.replaceChildren(
        h("li", null, "Open " + vendor + " below and sign in."),
        ...(loopback
          ? [h("li", null, "You end on a page that does not load. That is expected: the address of that page is the sign-in."), h("li", null, "Copy the address from the address bar, come back, and paste it here.")]
          : [h("li", null, vendor + " shows a code. Copy it, come back, and paste it here.")]))
      paste.placeholder = loopback ? "http://127.0.0.1:1455/auth/callback?code=..." : "Code"
      form.hidden = false
      status.textContent = ""
      void watch()
    }, e => { status.textContent = ""; end({ state: "failed", error: e instanceof Error ? e.message : String(e), text: "", url: "" }) })
  }

  // localPage connects the user's own OpenAI-compatible server, then lists its models.
  const localPage = () => {
    const name = input({ placeholder: "ollama", ariaLabel: "Name", required: true, pattern: "[a-z0-9][a-z0-9_.-]*" })
    const url = input({ type: "url", placeholder: "http://gpu-box:11434/v1", ariaLabel: "Address", required: true })
    const k = input({ type: "password", placeholder: "Optional", ariaLabel: "API key", autocomplete: "new-password" })
    const limit = input({ type: "number", placeholder: "30000", ariaLabel: "Context limit", min: "1000" })
    const form = h("form", null, h("div", { className: "form" }, field("Name", name), field("Address", url), field("API Key", k), field("Context Limit", limit)),
      h("p", { className: "note" }, "A server that speaks the OpenAI chat API, at an address the harness can reach. Its models show under Custom."))
    const go = connect(form, () => !!name.value.trim() && !!url.value.trim())
    form.onsubmit = e => {
      e.preventDefault()
      const n = name.value.trim()
      if (infos.some(i => i.name === n) || offers.some(o => n in o.keyed)) return fail(`The name "${n}" is taken.`)
      go.disabled = true
      api("/providers/" + n, { method: "PUT", json: { shape: "openai-chat", url: url.value.trim(), key: k.value.trim(), context_limit: Number(limit.value) || 0 } })
        .then(load).then(() => (host ? host.done() : detail(connected().find(c => c.name === n)!)), e => { go.disabled = false; fail(e) })
    }
    show("Local Server", add, [form], go)
    name.focus()
  }

  // detail is a provider's page: what it is, its models, and Disconnect.
  const detail = (c: Connected) => {
    const models = h("div")
    const out = h("button", { className: "btn danger delete", type: "button" }, "Disconnect")
    out.onclick = () => {
      if (!confirm(`Disconnect ${c.name}? ${c.offer?.sign_in ? "Its sign-in is deleted from the harness, and sessions" : "Sessions"} on its models stop working.`)) return
      out.disabled = true
      Promise.all(c.names.map(n => api("/providers/" + n, { method: "DELETE" }))).then(reload, e => { out.disabled = false; fail(e) })
    }
    show(c.name, list, [h("div", { className: "form" },
      h("div", { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, "Type")), h("span", { className: "value" }, c.offer ? kind(c) : "Local server")),
      ...(c.offer ? [] : [h("div", { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, "Address")), h("span", { className: "value" }, c.info.url.replace(/\/chat\/completions$/, "")))])), models, out])
    const names = c.offer
      ? get<Model[]>("/models").then(ms => ms.filter(m => c.names.includes(m.config.model.split("/")[0])).map(m => m.name))
      : get<string[]>("/providers/" + c.info.name + "/models")
    names.then(ns => models.replaceChildren(...group("Models", ...ns.map(n => h("div", { className: "form-row" }, h("span", { className: "row-main" }, h("b", null, n)))))),
      e => models.replaceChildren(h("p", { className: "note" }, "Could not list its models: " + (e instanceof Error ? e.message : e))))
  }

  load().then(host ? add : list, e => panel.replaceChildren(h("p", { className: "note" }, "Could not load the providers: " + (e instanceof Error ? e.message : e))))
  return panel
}
