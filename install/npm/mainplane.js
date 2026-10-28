#!/usr/bin/env node
// Runs the mainplane CLI of this package's version. The first run downloads this machine's
// release binary, checks it against the SHA256SUMS the release packed in here, and caches it.
const { createHash } = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

async function main() {
  const v = "v" + require("./package.json").version;
  const goos = { linux: "linux", darwin: "darwin", win32: "windows" }[process.platform];
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!goos || !arch) throw new Error(`no mainplane build for ${process.platform} ${process.arch}`);
  const name = `mainplane-${goos}-${arch}${goos === "windows" ? ".exe" : ""}`;
  const line = fs.readFileSync(path.join(__dirname, "SHA256SUMS"), "utf8").split("\n").find((l) => l.endsWith("  " + name));
  if (!line) throw new Error(`release ${v} has no ${name}`);
  const cache =
    goos === "windows" ? process.env.LOCALAPPDATA
    : goos === "darwin" ? path.join(os.homedir(), "Library", "Caches")
    : process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  const bin = path.join(cache, "mainplane", v, goos === "windows" ? "mainplane.exe" : "mainplane");
  if (!fs.existsSync(bin)) {
    const url = `https://dl.mainplane.ai/${v}/${name}`;
    const res = await fetch(url);
    if (!res.ok) throw new Error(`${url}: ${res.status}`);
    const b = Buffer.from(await res.arrayBuffer());
    if (createHash("sha256").update(b).digest("hex") !== line.split(" ")[0]) throw new Error(`${url} does not match SHA256SUMS`);
    fs.mkdirSync(path.dirname(bin), { recursive: true });
    fs.writeFileSync(bin + ".tmp", b, { mode: 0o755 });
    fs.renameSync(bin + ".tmp", bin);
  }
  const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
  if (r.error) throw r.error;
  if (r.signal) process.kill(process.pid, r.signal);
  process.exit(r.status);
}

main().catch((e) => {
  console.error("mainplane: " + e.message);
  process.exit(1);
});
