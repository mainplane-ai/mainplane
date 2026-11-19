// Rich text of a chat message: markdown by markdown-it, math by Temml (see the
// design system's rich-text component). Raw HTML in a message stays text.
// Temml is large, so math.js loads only when a message has math in it.

import markdownit, { type Rule } from "../vendor/markdown-it.mjs"

const md = markdownit({ linkify: true, breaks: true })
const esc = md.utils.escapeHtml

// Math is $..$ or \(..\) in a line, and $$..$$ or \[..\] as a block. A $ that
// opens has no space after it and one that closes no space before it and no
// digit after it, so "$5 and $10" stays text.
md.inline.ruler.after("escape", "math", (s, silent) => {
  const src: string = s.src, at: number = s.pos
  let m: RegExpMatchArray | null = null
  if (src.startsWith("\\(", at)) m = /^\\\(([\s\S]+?)\\\)/.exec(src.slice(at))
  else if (src[at] === "$" && src[at + 1] !== "$") m = /^\$(\S(?:[^$\n]*?[^\s\\])?)\$(?!\d)/.exec(src.slice(at))
  if (!m) return false
  if (!silent) s.push("math_inline", "", 0).content = m[1]
  s.pos += m[0].length
  return true
})
md.block.ruler.after("fence", "math_block", (s, start, end, silent) => {
  const line = (n: number) => s.src.slice(s.bMarks[n] + s.tShift[n], s.eMarks[n])
  const first = line(start), open = first.startsWith("$$") ? "$$" : first.startsWith("\\[") ? "\\[" : ""
  if (!open || s.sCount[start] - s.blkIndent >= 4) return false
  const close = open === "$$" ? "$$" : "\\]"
  let n = start, body = first.slice(2)
  while (!body.trimEnd().endsWith(close)) {
    if (++n >= end) return false
    body += "\n" + line(n)
  }
  if (silent) return true
  s.line = n + 1
  s.push("math_block", "", 0).content = body.trimEnd().slice(0, -2)
  return true
})
const r = md.renderer.rules
r.math_inline = (t, i) => `<span class="tex">${esc(t[i].content)}</span>`
r.math_block = (t, i) => `<div class="math-block tex d">${esc(t[i].content)}</div>\n`
const langs: Record<string, string> = { py: "Python", js: "JavaScript", ts: "TypeScript", sh: "Shell", bash: "Shell", zsh: "Shell", ps1: "PowerShell", pwsh: "PowerShell", go: "Go", json: "JSON", yaml: "YAML", html: "HTML", css: "CSS", sql: "SQL" }
r.fence = (t, i) => {
  const lang = t[i].info.trim().split(/\s/)[0]
  const name = langs[lang.toLowerCase()] ?? (lang ? lang[0].toUpperCase() + lang.slice(1) : "Code")
  return `<div class="code-block"><div class="cb-head"><span>${esc(name)}</span><button class="cb-copy" aria-label="Copy code"><mp-icon name="copy"></mp-icon><span>Copy</span></button></div><pre><code>${esc(t[i].content)}</code></pre></div>\n`
}
r.code_block = (t, i) => `<div class="code-block"><pre><code>${esc(t[i].content)}</code></pre></div>\n`
// The page's policy allows no style attributes: a right-aligned or numeric column gets the class num.
const cell: Rule = (t, i, o, _e, self) => {
  const right = t[i].attrGet("style")?.includes("right") || /^[-+$€£]?\s?[\d.,]+\s?%?$/.test(t[i + 1]?.content.trim() ?? "")
  t[i].attrs = right ? [["class", "num"]] : null
  return self.renderToken(t, i, o)
}
r.th_open = r.td_open = cell
r.link_open = (t, i, o, _e, self) => {
  t[i].attrSet("target", "_blank")
  t[i].attrSet("rel", "noopener noreferrer")
  return self.renderToken(t, i, o)
}

// rich is a message's text as HTML, and whether it has blocks: a reply with
// a heading, list, table, code block, quote or display math reads as a column,
// one with only inline formatting stays a bubble.
export function rich(text: string): { html: string; blocks: boolean } {
  const tokens = md.parse(text, {})
  const blocks = tokens.some(t => t.block && !/^(paragraph_(open|close)|inline)$/.test(t.type))
  return { html: md.renderer.render(tokens, md.options, {}), blocks }
}

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
