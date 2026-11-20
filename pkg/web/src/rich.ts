// Rich text of a chat message: markdown by markdown.ts, math by Temml (see the
// design system's rich-text component). Raw HTML in a message stays text.
// Temml is large, so math.js loads only when a message has math in it.

import { markdown } from "./markdown"

// rich is a message's text as HTML, and whether it has blocks: a reply with
// a heading, list, table, code block, quote or display math reads as a column,
// one with only inline formatting stays a bubble.
export const rich = markdown

// typeset turns the TeX in el into MathML.
export async function typeset(el: HTMLElement) {
  const tex = el.querySelectorAll<HTMLElement>(".tex")
  if (!tex.length) return
  const { default: temml } = await import(new URL("math.js", import.meta.url).href)
  for (const e of tex) {
    const html: string = temml.renderToString(e.textContent ?? "", { displayMode: e.classList.contains("d"), throwOnError: false })
    e.innerHTML = html.replace(/ style="[^"]*"/g, "") // the policy allows no style attributes; the CSS lays it out
    e.classList.remove("tex")
  }
}

addEventListener("click", async e => {
  const b = (e.target as Element).closest?.(".cb-copy")
  const code = b?.closest(".code-block")?.querySelector("code")
  if (!b || !code) return
  await navigator.clipboard.writeText(code.textContent ?? "")
  const label = b.querySelector("span")!
  label.textContent = "Copied"
  setTimeout(() => (label.textContent = "Copy"), 1500)
})
