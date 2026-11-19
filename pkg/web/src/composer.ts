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
  const box = h("textarea", { rows: 1, placeholder, ariaLabel: placeholder })
  const act = h("button", { className: "btn icon" })
  const chips = h("div", { className: "attachments", hidden: true })
  const field = h("div", { className: "field" }, chips, h("div", { className: "line" }, box, act))
  const pick = (accept: string, capture = false) => () => {
    const i = h("input", { type: "file", multiple: !capture, accept })
    if (capture) i.setAttribute("capture", "environment")
    i.onchange = () => add([...(i.files ?? [])])
    i.click()
  }
  const row = (name: string, label: string, go: () => void) => h("button", { role: "menuitem", onclick: go } as Partial<HTMLButtonElement>, icon(name), h("span", null, label))
  const menu = h("div", { className: "menu", role: "menu", hidden: true }, ...(matchMedia("(pointer: coarse)").matches ? [row("camera", "Camera", pick("image/*", true))] : []), row("image", "Photos", pick("image/*")), row("folder", "Files", pick("")))
  const plus = h("button", { type: "button", className: "btn icon", ariaLabel: "Add" }, icon("plus"))
  plus.onclick = () => MPMenu.open(menu, plus)
  const form = h("form", { className: "composer" }, plus, field, menu)

  let listening: Recognition | null = null
  // The one button at the end of the line is send when there is something to send, else the mic.
  const update = () => {
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
    listening?.stop()
    act.disabled = true
    try {
      // Each part is one message record: the text, then each file with its type.
      const body = new FormData()
      if (box.value.trim()) body.append("text", new Blob([box.value], { type: "text/plain" }))
      for (const f of files) body.append("file", f, f.name)
      await api(`/agents/${agent}/records?via=web`, files.length ? { form: body } : { text: box.value })
      box.value = ""
      files.length = 0
      chips.replaceChildren()
      chips.hidden = true
      acted()
    } catch (err) {
      alert(err)
    }
    act.disabled = false
    update()
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
  update()
  return form
}
