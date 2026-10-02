// The js interpreter. The worker writes a snippet on stdin, then a line of
// NUL and the run's mark. The snippet runs as the body of an async function;
// its console output and returned value go to stdout, then "<mark> <exit>".
// Every console method writes to stdout, so all output keeps its order and
// the mark comes last.
import { format } from "node:util"

const out = (s) => process.stdout.write(s + "\n")
for (const k of ["log", "info", "warn", "error", "debug"]) console[k] = (...a) => out(format(...a))
process.on("uncaughtException", (e) => out(`uncaught exception: ${e?.stack ?? e}`))
process.on("unhandledRejection", (e) => out(`unhandled rejection: ${e?.stack ?? e}`))

// The snippet starts on the wrapper's first line, so a stack's snippet:N is
// the snippet's line N.
const compile = (src) => (0, eval)(`(async () => {${src}\n})\n//# sourceURL=snippet`)
const json = (v) => JSON.stringify(v, (_, x) => (typeof x === "bigint" ? x.toString() : x), 2) ?? String(v)

async function run(src, mark) {
  let exit = 0
  try {
    const v = await compile(src)()
    if (v !== undefined) out(typeof v === "string" ? v : json(v))
  } catch (e) {
    out(e?.stack ?? String(e))
    exit = 1
  }
  out(`${mark} ${exit}`)
}

let buf = ""
let lines = []
let queue = Promise.resolve()
process.stdin.setEncoding("utf8")
process.stdin.on("data", (chunk) => {
  buf += chunk
  for (let i; (i = buf.indexOf("\n")) >= 0; ) {
    const line = buf.slice(0, i)
    buf = buf.slice(i + 1)
    if (!line.startsWith("\0")) {
      lines.push(line)
      continue
    }
    const src = lines.join("\n")
    lines = []
    queue = queue.then(() => run(src, line.slice(1)))
  }
})
process.stdin.on("end", () => queue.then(() => process.exit(0)))
