// The composer is where the user writes to an agent (the design system's
// composer): + adds files, the mic dictates until there is text or a file,
// then it is send. Enter sends, Shift+Enter is a new line.

import { acted, api, h, icon } from "./api"

declare const MPMenu: { open: (menu: HTMLElement, at: Element) => void }
type Recognition = { lang: string; interimResults: boolean; continuous: boolean; start(): void; stop(): void; onresult: ((e: { results: ArrayLike<ArrayLike<{ transcript: string }>> }) => void) | null; onend: (() => void) | null }
const Speech = (window as unknown as { SpeechRecognition?: new () => Recognition; webkitSpeechRecognition?: new () => Recognition }).SpeechRecognition ?? (window as unknown as { webkitSpeechRecognition?: new () => Recognition }).webkitSpeechRecognition

const size = (n: number) => (n < 1e3 ? `${n} B` : n < 1e6 ? `${Math.round(n / 1e3)} KB` : `${(n / 1e6).toFixed(1)} MB`)
const kind = (f: File) => (f.name.match(/\.(\w+)$/)?.[1] ?? f.type.split("/")[1] ?? "File").toUpperCase()

export function composer(agent: string, placeholder: string): HTMLElement {
  const files: File[] = []
  // What is written and not sent is kept for its chat: it is there after a visit elsewhere and after a reload.
  const draft = "draft:" + agent
  const box = h("textarea", { rows: 1, placeholder, ariaLabel: placeholder, value: sessionStorage.getItem(draft) ?? "" })
  const act = h("button", { className: "btn icon" })
  const chips = h("div", { className: "attachments", hidden: true })
  // A reply to a dashboard's question shows what it replies to over the box, and goes with the message.
  let to = ""
  const quote = h("div", { className: "quote", hidden: true }, icon("reply"), h("span"), h("button", { type: "button", className: "btn icon ghost sm", ariaLabel: "Cancel reply", onclick: () => replying("") } as Partial<HTMLButtonElement>, icon("close")))
  const replying = (title: string) => {
    to = title
    quote.hidden = !title
    quote.querySelector("span")!.textContent = `Replying to "${title}"`
    box.focus()
  }
  addEventListener("reply", e => {
    const d = (e as CustomEvent<{ agent: string; title: string }>).detail
    if (d.agent === agent) replying(d.title)
  })
  const field = h("div", { className: "field" }, quote, chips, h("div", { className: "line" }, box, act))
  const pick = (accept: string, capture = false) => () => {
    const i = h("input", { type: "file", multiple: !capture, accept })
    if (capture) i.setAttribute("capture", "environment")
    i.onchange = () => add([...(i.files ?? [])])
    i.click()
  }
  const row = (name: string, label: string, go: () => void) => h("button", { type: "button", role: "menuitem", onclick: go } as Partial<HTMLButtonElement>, icon(name), h("span", null, label))
  const menu = h("div", { className: "menu", role: "menu", hidden: true }, ...(matchMedia("(pointer: coarse)").matches ? [row("camera", "Camera", pick("image/*", true))] : []), row("image", "Photos", pick("image/*")), row("folder", "Files", pick("")))
  const plus = h("button", { type: "button", className: "btn icon", ariaLabel: "Add" }, icon("plus"))
  plus.onclick = () => MPMenu.open(menu, plus)
  const form = h("form", { className: "composer" }, plus, field, menu)

  let listening: Recognition | null = null
  // The one button at the end of the line is send when there is something to send, else the mic.
  const update = () => {
    if (box.value) sessionStorage.setItem(draft, box.value)
    else sessionStorage.removeItem(draft)
    const ready = !!box.value.trim() || files.length > 0
    if (!ready && !Speech) {
      act.hidden = true
      return
    }
    act.hidden = false
    act.type = ready ? "submit" : "button"
    act.className = ready ? "btn icon send" : "btn icon ghost"
    act.ariaLabel = ready ? "Send" : listening ? "Stop dictation" : "Dictate"
    act.ariaPressed = ready ? null : String(!!listening)
    act.replaceChildren(icon(ready ? "arrow-up" : listening ? "stop" : "mic"))
  }
  const add = (more: File[]) => {
    for (const f of more) {
      files.push(f)
      const x = h("button", { type: "button", className: "btn icon x", ariaLabel: `Remove ${f.name}` }, icon("close"))
      const chip = f.type.startsWith("image/")
        ? h("span", { className: "attach chip img", role: "img", ariaLabel: f.name }, h("img", { src: URL.createObjectURL(f), alt: "" }), x)
        : h("span", { className: "attach chip" }, icon("file"), h("span", { className: "n" }, h("b", null, f.name), h("small", null, `${kind(f)} · ${size(f.size)}`)), x)
      x.onclick = () => {
        files.splice(files.indexOf(f), 1)
        chip.remove()
        chips.hidden = !files.length
        update()
        box.focus()
      }
      chips.append(chip)
    }
    chips.hidden = !files.length
    update()
    box.focus()
  }
  const dictate = () => {
    if (listening) return listening.stop()
    const r = new Speech!()
    r.lang = navigator.language
    r.interimResults = true
    r.continuous = true
    const before = box.value && box.value.replace(/\s*$/, " ")
    r.onresult = e => {
      box.value = before + Array.from(e.results, x => x[0].transcript).join("")
      update()
    }
    r.onend = () => {
      listening = null
      update()
    }
    listening = r
    r.start()
    update()
  }
  act.onclick = e => {
    if (act.type === "button") {
      e.preventDefault()
      dictate()
    }
  }

  form.onsubmit = async e => {
    e.preventDefault()
    if (!box.value.trim() && !files.length) return
    const typed = box.value, was = to, sending = files.splice(0)
    const text = was && typed.trim() ? `Replying to: "${was}"\n\n${typed}` : typed
    listening?.stop()
    // Each part is one message record: the text, then each file with its type.
    const body = new FormData()
    if (text.trim()) body.append("text", new Blob([text], { type: "text/plain" }))
    for (const f of sending) body.append("file", f, f.name)
    const sent = api(`/agents/${agent}/records?via=web`, sending.length ? { form: body } : { text })
    // The box is free for the next message at once, and a chat shows this one while it is on its way.
    dispatchEvent(new CustomEvent("sending", { detail: { agent, text, files: sending, sent } }))
    box.value = ""
    replying("")
    chips.replaceChildren()
    chips.hidden = true
    update()
    try {
      await sent
      acted()
    } catch (err) {
      if (!box.value) {
        box.value = typed // back in the box, unless the next message is there
        replying(was)
      }
      add(sending)
      alert(err)
    }
  }
  box.oninput = update
  box.onkeydown = e => {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return
    e.preventDefault()
    form.requestSubmit()
  }
  box.onpaste = e => {
    const pasted = [...(e.clipboardData?.files ?? [])]
    if (pasted.length) {
      e.preventDefault()
      add(pasted)
    }
  }
  // A composer kept in the page while another one of its chat was written in shows what that one left.
  form.addEventListener("show", () => {
    box.value = sessionStorage.getItem(draft) ?? ""
    update()
  })
  update()
  // The composer floats (iOS glass): what is above goes on under it, padded by its height.
  new ResizeObserver(() => form.parentElement?.style.setProperty("--composer-h", form.offsetHeight + "px")).observe(form)
  return form
}
