// The composer is where the user writes to an agent (the design system's
// composer): + adds files, the mic dictates until there is text or a file,
// then it is send. Enter sends, Shift+Enter is a new line. On a touch screen
// Return is a new line and only the send button sends.

import { acted, api, h, icon } from "./api"
import { path, paths } from "./api.gen"

declare const MPMenu: { open: (menu: HTMLElement, at: Element) => void }
type Recognition = { lang: string; interimResults: boolean; continuous: boolean; start(): void; stop(): void; onresult: ((e: { results: ArrayLike<ArrayLike<{ transcript: string }>> }) => void) | null; onend: (() => void) | null }
const Speech = (window as unknown as { SpeechRecognition?: new () => Recognition; webkitSpeechRecognition?: new () => Recognition }).SpeechRecognition ?? (window as unknown as { webkitSpeechRecognition?: new () => Recognition }).webkitSpeechRecognition

const size = (n: number) => (n < 1e3 ? `${n} B` : n < 1e6 ? `${Math.round(n / 1e3)} KB` : `${(n / 1e6).toFixed(1)} MB`)
// limit is the harness's bound on one post (harness.MaxPost).
const limit = 8 << 20

// shrink is a photo as a JPEG of at most 2000 px on its long side: a phone's
// 3-8 MB HEIC or JPEG becomes a few hundred KB. A file the browser cannot
// decode, a GIF, and a small picture go as they are.
async function shrink(f: File): Promise<File> {
  if (!f.type.startsWith("image/") || f.type === "image/gif" || f.type === "image/svg+xml") return f
  const img = await createImageBitmap(f).catch(() => null)
  if (!img) return f
  const k = Math.min(1, 2000 / Math.max(img.width, img.height))
  if (k === 1 && f.size < 1e6 && /^image\/(jpeg|png|webp)$/.test(f.type)) return img.close(), f
  const c = h("canvas", { width: Math.round(img.width * k), height: Math.round(img.height * k) })
  const g = c.getContext("2d")!
  g.fillStyle = "#fff" // what is transparent in a PNG is white, not black, in the JPEG
  g.fillRect(0, 0, c.width, c.height)
  g.drawImage(img, 0, 0, c.width, c.height)
  img.close()
  const b = await new Promise<Blob | null>(go => c.toBlob(go, "image/jpeg", 0.85))
  return b ? new File([b], f.name.replace(/\.\w+$/, "") + ".jpg", { type: "image/jpeg" }) : f
}

// On a phone, while a field has the keyboard up, the page is the visual viewport: iOS lays the keyboard over
// the page and pans it, so the page takes the height and the place of what is still seen, and the composer
// sits right on the keyboard (app.css). With no field in focus the page is the screen: an iOS home-screen
// app can report a stale visual viewport, and a page taller than the screen does not scroll its list.
const vv = window.visualViewport
// fingers is a phone or tablet in hand: no hover, a finger. An iPad with a trackpad or mouse hovers, so Enter sends there.
const fingers = () => matchMedia("(pointer: coarse) and (hover: none)").matches
const typing = () => document.activeElement?.matches("textarea, input:not([type=checkbox], [type=radio], [type=range], [type=button], [type=submit], [type=file], [type=color]), [contenteditable]") ?? false
const fit = () => {
  const s = document.documentElement.style
  if (!vv || !typing()) return s.removeProperty("--vv-h"), s.removeProperty("--vv-top")
  s.setProperty("--vv-h", vv.height + "px")
  s.setProperty("--vv-top", vv.offsetTop + "px")
}
vv?.addEventListener("resize", fit)
vv?.addEventListener("scroll", fit)
addEventListener("focusin", fit)
addEventListener("focusout", () => setTimeout(fit)) // after the focus has moved on
// A swipe down on a chat or a composer, or a tap on a thread, puts the keyboard away (Messages).
let touch: { y: number; x: number; on: Element } | null = null
addEventListener("touchstart", e => {
  const t = e.touches[0]
  touch = e.touches.length === 1 && e.target instanceof Element && e.target.closest("main") ? { x: t.clientX, y: t.clientY, on: e.target } : null
}, { passive: true })
addEventListener("touchmove", e => {
  const t = e.touches[0], box = document.activeElement
  if (!touch || !(box instanceof HTMLTextAreaElement) || !box.closest(".composer")) return
  const dy = t.clientY - touch.y
  if (dy > 30 && dy > 2 * Math.abs(t.clientX - touch.x)) box.blur(), (touch = null)
}, { passive: true })
addEventListener("touchend", e => {
  const t = e.changedTouches[0], from = touch, box = document.activeElement
  touch = null
  if (!from || !(box instanceof HTMLTextAreaElement) || !box.closest(".composer")) return
  if (Math.hypot(t.clientX - from.x, t.clientY - from.y) < 10 && from.on.closest(".thread") && !from.on.closest("a, button, summary, input, textarea, [role=button]")) box.blur()
}, { passive: true })

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
  // A touch on Send does not take the focus from the box, so the keyboard stays up for the next message.
  // A mouse click is as it was: the focus goes to the button.
  let keep = false
  act.onpointerdown = e => {
    keep = act.type === "submit" && e.pointerType !== "mouse" && document.activeElement === box
    if (keep) e.preventDefault()
  }
  act.onclick = e => {
    if (keep) box.focus()
    keep = false
    if (act.type === "button") {
      e.preventDefault()
      dictate()
    }
  }

  // One message's posts end before the next one's begin: its last post wakes the agent to all of its parts.
  let queue: Promise<unknown> = Promise.resolve()
  form.onsubmit = async e => {
    e.preventDefault()
    if (!box.value.trim() && !files.length) return
    const typed = box.value, was = to, sending = files.splice(0)
    const text = was && typed.trim() ? `Replying to: "${was}"\n\n${typed}` : typed
    listening?.stop()
    // Each part is one post and one message record: the text, then each file with its type, photos shrunk.
    // A post at a time stays far under the bound of one; all but the last say more follow, so the agent
    // wakes once, to all of them. left is what has not reached the harness.
    let left: (string | File)[] = [...(text.trim() ? [text] : []), ...sending]
    const sent = queue.then(async () => {
      for (const [i, f] of sending.entries()) left[left.length - sending.length + i] = await shrink(f) // one at a time: a phone has little memory for big pictures
      const big = left.find(p => typeof p !== "string" && p.size > limit) as File | undefined
      if (big) throw new Error(`${big.name} is ${size(big.size)}, over the limit of ${limit >> 20} MiB for one post`)
      while (left.length) {
        const p = left[0], body = new FormData()
        if (typeof p !== "string") body.append("file", p, p.name)
        // A post that hangs (a phone that lost its network) ends in two minutes, so the next message is not held up for good.
        const signal = AbortSignal.timeout(120e3)
        await api(`${path(paths.AgentRecords, agent)}?via=web${left.length > 1 ? "&more=1" : ""}`, typeof p === "string" ? { text: p, signal } : { form: body, signal })
        left.shift()
      }
    })
    queue = sent.catch(() => {})
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
      // What did not go is back in the composer: the text unless the next message is there, and the files.
      if (left[0] === text && !box.value) {
        box.value = typed
        replying(was)
      }
      add(sending.slice(sending.length - left.filter(p => typeof p !== "string").length))
      const some = left.length < sending.length + (text.trim() ? 1 : 0) ? " (what went before it waits for the rest)" : ""
      alert(`Not sent: ${(err as Error).message}${some}`)
    }
  }
  box.oninput = update
  box.onfocus = () => (box.enterKeyHint = fingers() ? "enter" : "send") // the on-screen key reads return, or send
  box.onkeydown = e => {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing || fingers()) return
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
  // A kept project's composer hidden behind another view says nothing: its 0 would unpad the one shown.
  new ResizeObserver(() => form.hidden || form.parentElement?.style.setProperty("--composer-h", form.offsetHeight + "px")).observe(form)
  return form
}
