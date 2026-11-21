// Markdown of a chat message as HTML for the rich-text component: CommonMark
// with GFM tables and strikethrough, links only to safe schemes, and raw HTML
// always escaped. It gives what markdown-it gives with breaks and linkify on,
// so a line break is a <br> and a bare http(s) URL or email is a link. Math is
// $..$ in a line and $$..$$ or \[..\] as a block, left as TeX in a .tex
// element for Temml. Reference definitions are not supported.

// maxNesting is how deep quotes, lists and links go before the rest is dropped,
// as in markdown-it, so a hostile message cannot exhaust the stack.
const maxNesting = 100
// maxAutofill caps the empty cells a table adds to short rows, as in
// markdown-it: a wide header over many short rows would grow the page.
const maxAutofill = 65536

const esc = (s: string) => s.replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!)
const space = (c: string | undefined) => c === " " || c === "\t"
const trim = (s: string) => s.replace(/^[ \t\n\r]+|[ \t\n\r]+$/g, "")
const langs: Record<string, string> = { py: "Python", js: "JavaScript", ts: "TypeScript", sh: "Shell", bash: "Shell", zsh: "Shell", ps1: "PowerShell", pwsh: "PowerShell", go: "Go", json: "JSON", yaml: "YAML", html: "HTML", css: "CSS", sql: "SQL" }
const copy = `<button class="cb-copy" aria-label="Copy code"><mp-icon name="copy"></mp-icon><span>Copy</span></button>`
// The page's policy allows no style attributes: a right-aligned or numeric column gets the class num.
const numeric = /^[-+$€£]?\s?[\d.,]+\s?%?$/

// Links: a URL with a scheme must have a safe one, as must a link the text
// spells out. The URL is percent-encoded as markdown-it does.
const safe = (url: string) => !/^[a-z][a-z\d+.-]*:/i.test(url) || /^(https?|ftp|mailto):/i.test(url)
const encode = (url: string) => url.replace(/%(?![\da-f]{2})|[^\w;/?:@&=+$,\-.!~*'()#%]/giu, encodeURIComponent)
const shown = (url: string) => { try { return decodeURI(url) } catch { return url } }
const a = (href: string, body: string, title = "") => `<a href="${esc(href)}"${title && ` title="${esc(title)}"`} target="_blank" rel="noopener noreferrer">${body}</a>`
const pseudo = String.raw`[^\s\p{P}\p{Cc}<>｜]`
const label = `${pseudo}+(?:-+${pseudo}+)*`
const host = String.raw`(?:\[[\da-f:.]+\]|${label}(?:\.${label})*)(?::\d{1,5})?`
const path = String.raw`(?:[/?#](?:[^\s\p{Cc}<>｜()[\]{}.,"'?!;«»“”‘’\-]|\([^\s()]*\)|\[[^\s\]]*\]|\{[^\s}]*\}|"[^\s"]+"|'[^\s']+'|'(?=${pseudo}|-)|\.{2,}[a-zA-Z0-9%/&]|\.(?![\s.]|$)|-+|,(?!\s|$)|;(?!\s|$)|!+(?![\s!]|$)|\?(?![\s?]|$))*)?`
const urlRe = new RegExp(`(?:https?|ftp)://${host}${path}`, "iuy")
const mailRe = new RegExp(String.raw`(^|[\s"(<>｜])((?:mailto:)?[\w.+*-]+@${label}(?:\.${label})+)(?![\w@-]|\.\w)`, "giu")
const autoRe = /^[a-z][a-z\d+.-]{1,31}:[^<>\x00-\x20]*$/i
const autoMail = /^[\w.!#$%&'*+/=?^`{|}~-]+@[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?(?:\.[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?)*$/i

// Entities: numeric ones and the few named ones people type.
const named: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " ", copy: "©", reg: "®", trade: "™", hellip: "…", mdash: "—", ndash: "–", larr: "←", rarr: "→", times: "×", deg: "°", middot: "·" }
const code = (c: number) => (c <= 8 || c === 11 || (c >= 14 && c <= 31) || (c >= 127 && c <= 159) || (c >= 0xd800 && c <= 0xdfff) || (c >= 0xfdd0 && c <= 0xfdef) || (c & 0xfffe) === 0xfffe || c > 0x10ffff ? "�" : String.fromCodePoint(c))
const entity = (e: string) => (e[0] === "#" ? code(e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10)) : named[e])
const entityRe = /&(#[xX][\da-fA-F]{1,6}|#\d{1,7}|[a-zA-Z][a-zA-Z\d]{1,31});/y
const unescape = (s: string) => s.replace(/\\([!-/:-@[-`{-~])|&(#[xX][\da-fA-F]{1,6}|#\d{1,7}|[a-zA-Z][a-zA-Z\d]{1,31});/g, (m, e, n) => e ?? entity(n) ?? m)

// ---- Inline: one paragraph, heading or cell.

// A delimiter is a run of * _ or ~ that may open or close emphasis. When it
// does, the closing tags it gets go on its left and the opening ones on its right.
type Delim = { c: string; n: number; len: number; open: boolean; close: boolean; l: string; r: string; prev?: Delim; next?: Delim }
type Node = string | Delim | { t: string }
type Ctx = { src: string; runs: Record<number, number>; end: number[]; nested: boolean[] }

const punct = (c: string) => /[\p{P}\p{S}]/u.test(c)
const white = (c: string) => /[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u202f\u205f\u3000]/.test(c)
const before = (s: string, i: number) => (i === 0 ? " " : (s.charCodeAt(i - 1) & 0xfc00) === 0xdc00 && i > 1 ? String.fromCodePoint(s.codePointAt(i - 2)!) : s[i - 1])

function context(src: string): Ctx {
  const runs: Record<number, number> = {}, end: number[] = [], nested: boolean[] = [], stack: number[] = []
  for (let i = 0; (i = src.indexOf("`", i)) >= 0; ) { const s = i; while (src[++i] === "`"); runs[i - s] = s }
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (c === "\\") i++
    else if (c === "`") i = span(src, runs, i, src.length)[0] - 1
    else if (c === "[") stack.push(i)
    else if (c === "]" && stack.length) {
      const o = stack.pop()!, up = stack.at(-1)
      end[o] = i
      if (up !== undefined && (nested[o] || (src[i + 1] === "(" && src[o - 1] !== "!"))) nested[up] = true
    }
  }
  return { src, runs, end, nested }
}

// span is where a code span that starts at i ends, and its code, if it closes.
function span(src: string, runs: Record<number, number>, i: number, max: number): [number, string?] {
  let e = i
  while (e < max && src[e] === "`") e++
  const n = e - i
  if ((runs[n] ?? -1) < e) return [e]
  for (let s = src.indexOf("`", e); s >= 0 && s < max; s = src.indexOf("`", s)) {
    const at = s
    while (src[s] === "`") s++
    if (s > max) break
    if (s - at === n) {
      const c = src.slice(e, at).replace(/\n/g, " ")
      return [s, c[0] === " " && c.at(-1) === " " && /[^ ]/.test(c) ? c.slice(1, -1) : c]
    }
  }
  return [e]
}

function inline(cx: Ctx, pos: number, max: number, inLink: boolean, depth: number): string {
  const { src } = cx, nodes: Node[] = []
  let pend = "", m: RegExpExecArray | null
  const push = (n: Node) => { if (pend) nodes.push({ t: pend }); pend = ""; nodes.push(n) }
  const text = /[^\n!#$%&*+\-:<=>@[\\\]^_`{}~]+/y
  while (pos < max) {
    const c = src[pos]
    text.lastIndex = pos
    if ((m = text.exec(src))) { pend += src.slice(pos, Math.min(max, pos + m[0].length)); pos = Math.min(max, text.lastIndex); continue }
    if (depth >= maxNesting) { pend += c; pos++; continue }
    if (c === ":" && !inLink && src.startsWith("//", pos + 1)) {
      const scheme = /[a-z\d+.-]*$/i.exec(pend.slice(-10))![0]
      urlRe.lastIndex = pos - scheme.length
      const u = /^(https?|ftp)$/i.test(scheme) && urlRe.exec(src)
      const url = u && u[0].replace(/\*+$/, "")
      if (url && url.length > scheme.length + 3 && safe(encode(url))) {
        pend = pend.slice(0, -scheme.length)
        push(a(encode(url), esc(shown(url))))
        pos += url.length - scheme.length
        continue
      }
    } else if (c === "\n") {
      pend = pend.replace(/ +$/, "")
      push("<br>\n")
      for (pos++; space(src[pos]) && pos < max; pos++);
      continue
    } else if (c === "\\" && pos + 1 < max) {
      const n = src[pos + 1]
      if (n === "\n") { push("<br>\n"); for (pos += 2; space(src[pos]) && pos < max; pos++); continue }
      if (/[!-/:-@[-`{-~]/.test(n)) { push(esc(n)); pos += 2; continue }
    } else if (c === "$" && src[pos + 1] !== "$") {
      const r = /\$(\S(?:[^$\n]*?[^\s\\])?)\$(?!\d)/y
      r.lastIndex = pos
      if ((m = r.exec(src))) { push(`<span class="tex">${esc(m[1])}</span>`); pos = r.lastIndex; continue }
    } else if (c === "`") {
      const [e, code] = span(src, cx.runs, pos, max)
      if (code !== undefined) push(`<code>${esc(code)}</code>`)
      else pend += src.slice(pos, e)
      pos = e
      continue
    } else if (c === "*" || c === "_" || c === "~") {
      let e = pos
      while (e < max && src[e] === c) e++
      if (c !== "~" || e - pos > 1) {
        const last = before(src, pos), next = e < max ? String.fromCodePoint(src.codePointAt(e)!) : " "
        const lp = punct(last), np = punct(next), lw = white(last), nw = white(next)
        const left = !nw && (!np || lw || lp), right = !lw && (!lp || nw || np)
        const split = c !== "_"
        push({ c, n: e - pos, len: c === "~" ? 0 : e - pos, open: left && (split || !right || lp), close: right && (split || !left || np), l: "", r: "" })
        pos = e
        continue
      }
    } else if (c === "[" || (c === "!" && src[pos + 1] === "[")) {
      const img = c === "!", open = pos + (img ? 1 : 0), end = cx.end[open] ?? -1
      if (end >= 0 && end < max && src[end + 1] === "(" && (img || !cx.nested[open])) {
        const l = dest(src, end + 2, max)
        if (l) {
          const body = inline(cx, open + 1, end, true, depth + 1)
          push(img ? `<img src="${esc(l.href)}" alt="${body.replace(/<[^>]*>/g, "")}"${l.title && ` title="${esc(l.title)}"`}>` : a(l.href, body, l.title))
          pos = l.pos
          continue
        }
      }
    } else if (c === "<") {
      const r = /<([^<>]*)>/y
      r.lastIndex = pos
      if ((m = r.exec(src)) && r.lastIndex <= max) {
        const url = autoRe.test(m[1]) ? encode(m[1]) : autoMail.test(m[1]) ? encode("mailto:" + m[1]) : ""
        if (url && safe(url)) { push(a(url, esc(shown(m[1])))); pos = r.lastIndex; continue }
      }
    } else if (c === "&") {
      entityRe.lastIndex = pos
      const d = (m = entityRe.exec(src)) && entity(m[1])
      if (d) { push(esc(d)); pos = entityRe.lastIndex; continue }
    }
    pend += c
    pos++
  }
  if (pend) nodes.push({ t: pend })
  emphasis(nodes.filter((n): n is Delim => typeof n === "object" && "c" in n))
  let out = "", run = ""
  const flush = () => { out += inLink ? esc(run) : linkMail(run); run = "" }
  for (const n of nodes) {
    if (typeof n === "string") { flush(); out += n }
    else if ("t" in n) run += n.t
    else { if (n.l) { flush(); out += n.l } run += n.c.repeat(n.n); if (n.r) { flush(); out += n.r } }
  }
  flush()
  return out
}

const linkMail = (s: string) => {
  let out = "", at = 0
  for (const m of s.matchAll(mailRe)) {
    const i = m.index + m[1].length, href = /^mailto:/i.test(m[2]) ? m[2] : "mailto:" + m[2]
    out += esc(s.slice(at, i)) + a(encode(href), esc(m[2]))
    at = i + m[2].length
  }
  return out + esc(s.slice(at))
}

// emphasis pairs the delimiters as CommonMark says: each closer takes the
// nearest opener of its kind, strong when both have two to give.
function emphasis(ds: Delim[]) {
  ds.forEach((d, i) => { d.prev = ds[i - 1]; d.next = ds[i + 1] })
  const drop = (d: Delim) => { if (d.prev) d.prev.next = d.next; if (d.next) d.next.prev = d.prev }
  const bottom: Record<string, Delim | undefined> = {}
  for (let closer = ds[0]; closer; ) {
    const key = closer.c + closer.open + (closer.len % 3)
    if (!closer.close || (closer.c === "~" && closer.n < 2)) { closer = closer.next!; continue }
    let o = closer.prev
    for (; o && o !== bottom[key]; o = o.prev) {
      if (o.c !== closer.c || !o.open || (o.c === "~" && o.n < 2)) continue
      if ((o.close || closer.open) && (o.len + closer.len) % 3 === 0 && (o.len % 3 !== 0 || closer.len % 3 !== 0)) continue
      break
    }
    if (o && o !== bottom[key]) {
      const k = closer.c === "~" || (o.n > 1 && closer.n > 1) ? 2 : 1, tag = closer.c === "~" ? "s" : k === 2 ? "strong" : "em"
      o.n -= k
      closer.n -= k
      o.r = `<${tag}>` + o.r
      closer.l += `</${tag}>`
      o.next = closer
      closer.prev = o
      if (!o.n) drop(o)
      if (!closer.n) { drop(closer); closer = closer.next! }
    } else {
      bottom[key] = closer.prev
      if (!closer.open) drop(closer)
      closer = closer.next!
    }
  }
}

// dest reads the (destination "title") of an inline link from pos, just past the "(".
function dest(src: string, pos: number, max: number): { href: string; title: string; pos: number } | undefined {
  const ws = () => { while (pos < max && (space(src[pos]) || src[pos] === "\n")) pos++ }
  ws()
  if (pos >= max) return
  let href = "", title = "", raw: string | undefined, start = pos
  if (src[pos] === "<") {
    for (let i = pos + 1; i < max && src[i] !== "\n" && src[i] !== "<"; i++) {
      if (src[i] === ">") { raw = src.slice(pos + 1, i); start = i + 1; break }
      if (src[i] === "\\") i++
    }
  } else {
    let i = pos, level = 0
    for (; i < max; i++) {
      const c = src.charCodeAt(i)
      if (c <= 32 || c === 127) break
      if (c === 92 && i + 1 < max && src[i + 1] !== " ") { i++; continue }
      if (c === 40 && ++level > 32) return
      if (c === 41 && level-- === 0) break
    }
    if (i > pos && level <= 0) { raw = src.slice(pos, i); start = i }
  }
  if (raw !== undefined) {
    const url = encode(unescape(raw))
    if (safe(url)) { href = url; pos = start }
  }
  const at = pos
  ws()
  const q = src[pos], close = q === "(" ? ")" : q
  if (pos < max && at !== pos && (q === '"' || q === "'" || q === "(")) {
    for (let i = pos + 1; i < max; i++) {
      if (src[i] === close) { title = unescape(src.slice(pos + 1, i)); pos = i + 1; ws(); break }
      if (q === "(" && src[i] === "(") break
      if (src[i] === "\\") i++
    }
  }
  if (pos >= max || src[pos] !== ")") return
  return { href, title, pos: pos + 1 }
}

// ---- Blocks: the line state machine of markdown-it. Each line has its
// start b, end e, offset t of its first non-space and indent s in columns.

type Out = string | { p: string; d: number; hide?: boolean }
type State = { src: string; b: number[]; e: number[]; t: number[]; s: number[]; ind: number; listInd: number; parent: string; line: number; tight: boolean; d: number; out: Out[]; task: string; noMath: string }
type Rule = (s: State, start: number, end: number, silent: boolean) => boolean

const first = (s: State, n: number) => s.b[n] + s.t[n]
const empty = (s: State, n: number) => first(s, n) >= s.e[n]
const getLine = (s: State, n: number) => s.src.slice(first(s, n), s.e[n])
const inl = (src: string) => inline(context(src), 0, src.length, false, 0)

function lines(s: State, begin: number, end: number, indent: number, keepLF: boolean) {
  let r = ""
  for (let n = begin; n < end; n++) {
    let col = 0, at = s.b[n]
    const last = n + 1 < end || keepLF ? s.e[n] + 1 : s.e[n]
    for (; at < last && col < indent; at++) {
      if (space(s.src[at])) col += s.src[at] === "\t" ? 4 - (col % 4) : 1
      else if (at - s.b[n] < s.t[n]) col++
      else break
    }
    r += " ".repeat(Math.max(0, col - indent)) + s.src.slice(at, last)
  }
  return r
}

function tokenize(s: State, line: number, end: number) {
  let gap = false
  while (line < end) {
    while (line < end && empty(s, line)) line++
    s.line = line
    if (line >= end || s.s[line] < s.ind) break
    if (s.d >= maxNesting) { s.line = end; break }
    rules.some(r => r(s, line, end, false))
    s.tight = !gap
    if (empty(s, s.line - 1)) gap = true
    line = s.line
    if (line < end && empty(s, line)) { gap = true; s.line = ++line }
  }
}

const cells = (l: string) => {
  const r = l.replace(/\\\|/g, "\0").split("|").map(c => c.replace(/\0/g, "|"))
  if (r[0] === "") r.shift()
  if (r.at(-1) === "") r.pop()
  return r
}

const table: Rule = (s, start, end, silent) => {
  const d = start + 1
  if (d + 1 > end || s.s[d] < s.ind || s.s[d] - s.ind >= 4 || s.s[start] - s.ind >= 4) return false
  const dl = getLine(s, d)
  if (!/^[|:-](?:[|:-]|[ \t])/.test(dl) || /^-[ \t]/.test(dl) || !/^[|:\- \t]*$/.test(dl)) return false
  const parts = dl.split("|"), aligns: boolean[] = []
  for (let i = 0; i < parts.length; i++) {
    const t = parts[i].trim()
    if (!t && (i === 0 || i === parts.length - 1)) continue
    if (!/^:?-+:?$/.test(t)) return false
    aligns.push(t.endsWith(":") && !t.startsWith(":"))
  }
  const head = getLine(s, start).trim()
  if (!head.includes("|")) return false
  const hc = cells(head)
  if (!hc.length || hc.length !== aligns.length) return false
  if (silent) return true
  const row = (cs: string[], tag: string) => "<tr>\n" + aligns.map((right, i) => { const c = (cs[i] ?? "").trim(); return `<${tag}${right || numeric.test(c) ? ' class="num"' : ""}>${inl(c)}</${tag}>\n` }).join("") + "</tr>\n"
  let html = `<table>\n<thead>\n${row(hc, "th")}</thead>\n`, body = "", fill = 0, n = start + 2
  const oldParent = s.parent
  s.parent = "table"
  for (; n < end; n++) {
    if (s.s[n] < s.ind || terms.blockquote.some(r => r(s, n, end, true))) break
    const l = getLine(s, n).trim()
    if (!l || s.s[n] - s.ind >= 4) break
    const cs = cells(l)
    if ((fill += aligns.length - cs.length) > maxAutofill) break
    body += row(cs, "td")
  }
  s.parent = oldParent
  s.out.push(html + (body && `<tbody>\n${body}</tbody>\n`) + "</table>\n")
  s.line = n
  return true
}

const codeBlock = (lang: string | undefined, body: string) => {
  const name = lang === undefined ? "" : langs[lang.toLowerCase()] ?? (lang ? lang[0].toUpperCase() + lang.slice(1) : "Code")
  return `<div class="code-block">${lang === undefined ? "" : `<div class="cb-head"><span>${esc(name)}</span>${copy}</div>`}<pre><code>${esc(body)}</code></pre></div>\n`
}

const indented: Rule = (s, start, end) => {
  if (s.s[start] - s.ind < 4) return false
  let n = start + 1, last = n
  for (; n < end; n++) {
    if (empty(s, n)) continue
    if (s.s[n] - s.ind < 4) break
    last = n + 1
  }
  s.line = last
  s.out.push(codeBlock(undefined, lines(s, start, last, 4 + s.ind, false) + "\n"))
  return true
}

const fence: Rule = (s, start, end, silent) => {
  const m = /^(`{3,}|~{3,})(.*)/.exec(getLine(s, start))
  if (s.s[start] - s.ind >= 4 || !m || (m[1][0] === "`" && m[2].includes("`"))) return false
  if (silent) return true
  let n = start, closed = false
  while (++n < end) {
    if (!empty(s, n) && s.s[n] < s.ind) break
    if (s.s[n] - s.ind >= 4) continue
    const c = /^(`+|~+)[ \t]*$/.exec(getLine(s, n))
    if (c && c[1][0] === m[1][0] && c[1].length >= m[1].length) { closed = true; break }
  }
  s.line = n + (closed ? 1 : 0)
  s.out.push(codeBlock(m[2].trim().split(/\s/)[0], lines(s, start + 1, n, s.s[start], true)))
  return true
}

// math is $$..$$ or \[..\] on lines of their own, or on one line.
const math: Rule = (s, start, end) => {
  const l = getLine(s, start), open = l.slice(0, 2), close = open === "$$" ? "$$" : "\\]"
  if ((open !== "$$" && open !== "\\[") || s.s[start] - s.ind >= 4) return false
  let n = start
  if (!l.slice(2).trimEnd().endsWith(close)) {
    if (s.noMath === `${close} ${end}`) return false // no line below closes it: seen from a line above
    while (++n < end && !getLine(s, n).trimEnd().endsWith(close));
    if (n >= end) { s.noMath = `${close} ${end}`; return false }
  }
  const body = [l.slice(2), ...Array.from({ length: n - start }, (_, i) => getLine(s, start + 1 + i))].join("\n").trimEnd().slice(0, -2)
  s.line = n + 1
  s.out.push(`<div class="math-block tex d">${esc(body)}</div>\n`)
  return true
}

const quote: Rule = (s, start, end, silent) => {
  if (s.s[start] - s.ind >= 4 || s.src[first(s, start)] !== ">") return false
  if (silent) return true
  const saved: number[][] = [], oldParent = s.parent, oldInd = s.ind
  const save = (n: number) => saved.push([s.b[n], s.t[n], s.s[n]])
  s.parent = "blockquote"
  let lastEmpty = false, n = start
  for (; n < end; n++) {
    let pos = first(s, n)
    const max = s.e[n]
    if (pos >= max) break
    if (s.src[pos++] === ">" && s.s[n] >= s.ind) {
      let initial = s.s[n] + 1
      if (space(s.src[pos])) { pos++; initial++ }
      let off = initial
      save(n)
      s.b[n] = pos
      for (; pos < max && space(s.src[pos]); pos++) off += s.src[pos] === "\t" ? 4 - (off % 4) : 1
      lastEmpty = pos >= max
      s.s[n] = off - initial
      s.t[n] = pos - s.b[n]
      continue
    }
    if (lastEmpty) break
    if (terms.blockquote.some(r => r(s, n, end, true))) {
      if (s.ind) { save(n); s.s[n] -= s.ind }
      break
    }
    save(n)
    s.s[n] = -1
  }
  s.ind = 0
  s.out.push("<blockquote>\n")
  s.d++
  tokenize(s, start, n)
  s.d--
  s.out.push("</blockquote>\n")
  s.parent = oldParent
  saved.forEach(([b, t, c], i) => { s.b[start + i] = b; s.t[start + i] = t; s.s[start + i] = c })
  s.ind = oldInd
  return true
}

const hr: Rule = (s, start, _end, silent) => {
  if (s.s[start] - s.ind >= 4 || !/^([*_-])[ \t]*(?:\1[ \t]*){2,}$/.test(getLine(s, start))) return false
  if (!silent) { s.line = start + 1; s.out.push("<hr>\n") }
  return true
}

// marker is where the content after a list marker at line n starts, or -1.
const marker = (s: State, n: number, ordered: boolean) => {
  const m = (ordered ? /^\d{1,9}[.)](?=[ \t]|$)/ : /^[*+-](?=[ \t]|$)/).exec(getLine(s, n))
  return m ? first(s, n) + m[0].length : -1
}

const list: Rule = (s, start, end, silent) => {
  let n = start, tight = true, after: number
  if (s.s[n] - s.ind >= 4 || (s.listInd >= 0 && s.s[n] - s.listInd >= 4 && s.s[n] < s.ind)) return false
  const para = silent && s.parent === "paragraph" && s.s[n] >= s.ind
  const ordered = (after = marker(s, n, true)) >= 0
  const num = ordered ? Number(s.src.slice(first(s, n), after - 1)) : 1
  if (ordered ? para && num !== 1 : (after = marker(s, n, false)) < 0) return false
  if (para && !/[^ \t]/.test(s.src.slice(after, s.e[n]))) return false
  if (silent) return true
  const mark = s.src[after - 1], tag = ordered ? "ol" : "ul", oldParent = s.parent
  s.out.push(num !== 1 ? `<ol start="${num}">\n` : `<${tag}>\n`)
  s.d++
  const from = s.out.length
  let prevEmptyEnd = false
  s.parent = "list"
  while (n < end) {
    let pos = after
    const max = s.e[n], initial = s.s[n] + after - first(s, n)
    let off = initial
    for (; pos < max && space(s.src[pos]); pos++) off += s.src[pos] === "\t" ? 4 - (off % 4) : 1
    let gap = pos >= max ? 1 : off - initial
    if (gap > 4) gap = 1
    const task = /^\[([ xX])\][ \t]+(?=\S)/.exec(s.src.slice(pos, max))
    s.out.push(task ? '<li class="task">' : "<li>")
    s.task = task ? task[1] : ""
    s.d++
    const oldTight = s.tight, oldT = s.t[n], oldS = s.s[n], oldListInd = s.listInd
    s.listInd = s.ind
    s.ind = initial + gap
    s.tight = true
    s.t[n] = pos - s.b[n]
    s.s[n] = off
    if (pos >= max && empty(s, n + 1)) s.line = Math.min(n + 2, end)
    else tokenize(s, n, end)
    s.task = ""
    if (!s.tight || prevEmptyEnd) tight = false
    prevEmptyEnd = s.line - n > 1 && empty(s, s.line - 1)
    s.ind = s.listInd
    s.listInd = oldListInd
    s.t[n] = oldT
    s.s[n] = oldS
    s.tight = oldTight
    s.d--
    s.out.push("</li>\n")
    n = s.line
    if (n >= end || s.s[n] < s.ind || s.s[n] - s.ind >= 4 || terms.list.some(r => r(s, n, end, true))) break
    if ((after = marker(s, n, ordered)) < 0 || s.src[after - 1] !== mark) break
  }
  s.d--
  s.out.push(`</${tag}>\n`)
  s.line = n
  s.parent = oldParent
  if (tight) for (let i = from; i < s.out.length; i++) { const o = s.out[i]; if (typeof o !== "string" && o.d === s.d + 2) o.hide = true }
  return true
}

const heading: Rule = (s, start, _end, silent) => {
  const m = /^(#{1,6})(?:[ \t]|$)/.exec(getLine(s, start))
  if (s.s[start] - s.ind >= 4 || !m) return false
  if (silent) return true
  let c = getLine(s, start).slice(m[1].length).replace(/[ \t]+$/, "")
  const t = c.replace(/#+$/, "")
  if (t.length < c.length && space(t.at(-1))) c = t
  s.line = start + 1
  s.out.push(`<h${m[1].length}>${inl(trim(c))}</h${m[1].length}>\n`)
  return true
}

// paragraph is a paragraph, or a setext heading when a line of = or - ends it.
const paragraph: Rule = (s, start, end) => {
  const oldParent = s.parent
  s.parent = "paragraph"
  let n = start + 1, level = 0
  for (; n < end && !empty(s, n); n++) {
    if (s.s[n] - s.ind > 3) continue
    if (s.s[n] >= s.ind) {
      const m = /^(?:(=+)|-+)[ \t]*$/.exec(getLine(s, n))
      if (m) { level = m[1] ? 1 : 2; break }
    }
    if (s.s[n] < 0) continue
    if (terms.paragraph.some(r => r(s, n, end, true))) break
  }
  s.parent = oldParent
  let c = trim(lines(s, start, n, s.ind, false)), box = ""
  // A list item that starts with [ ] or [x] is a task, and its first paragraph starts with the box.
  if (s.task && !level) { box = `<input type="checkbox" disabled${s.task === " " ? "" : " checked"}> `; c = c.replace(/^\[[ xX]\][ \t]+/, "") }
  s.task = ""
  const html = inl(c)
  if (level) s.out.push(`<h${level}>${html}</h${level}>\n`)
  else s.out.push({ p: box + html, d: s.d })
  s.line = n + (level ? 1 : 0)
  return true
}

const rules = [table, indented, fence, math, quote, hr, list, heading, paragraph]
const terms = { paragraph: [table, fence, math, quote, hr, list, heading], blockquote: [fence, math, quote, hr, list, heading], list: [fence, math, quote, hr] }

// markdown is text as HTML, and whether it has blocks: a heading, list,
// table, code block, quote, rule or display math.
export function markdown(text: string): { html: string; blocks: boolean } {
  const src = text.replace(/\r\n?/g, "\n").replace(/\0/g, "�")
  const s: State = { src, b: [], e: [], t: [], s: [], ind: 0, listInd: -1, parent: "root", line: 0, tight: false, d: 0, out: [], task: "", noMath: "" }
  const ls = src.split("\n")
  if (ls.at(-1) === "") ls.pop()
  let pos = 0
  for (const l of ls) {
    let t = 0, c = 0
    for (; space(l[t]); t++) c += l[t] === "\t" ? 4 - (c % 4) : 1
    s.b.push(pos); s.e.push(pos + l.length); s.t.push(t); s.s.push(c)
    pos += l.length + 1
  }
  s.b.push(src.length); s.e.push(src.length); s.t.push(0); s.s.push(0)
  tokenize(s, 0, ls.length)
  // A hidden paragraph's text is set off from a block after it, as markdown-it does.
  const html = s.out.map((o, i) => (typeof o === "string" ? o : !o.hide ? `<p>${o.p}</p>\n` : /^<(?![/]|div)/.test(s.out[i + 1] as string) ? o.p + "\n" : o.p)).join("")
  return { html, blocks: s.out.some(o => typeof o === "string") }
}
