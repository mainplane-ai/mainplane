"""Runs the mainplane CLI of this package's version. The first run downloads this machine's
release binary, checks it against the SHA256SUMS the release packed in here, and caches it."""

import hashlib
import os
import platform
import subprocess
import sys
import urllib.request
from importlib.metadata import version
from pathlib import Path


def main():
    v = "v" + version("mainplane")
    goos = {"linux": "linux", "darwin": "darwin", "win32": "windows"}.get(sys.platform)
    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine().lower())
    if not goos or not arch:
        sys.exit(f"mainplane: no mainplane build for {sys.platform} {platform.machine()}")
    name = f"mainplane-{goos}-{arch}" + (".exe" if goos == "windows" else "")
    sums = (Path(__file__).parent / "SHA256SUMS").read_text().splitlines()
    line = next((l for l in sums if l.endswith("  " + name)), None)
    if not line:
        sys.exit(f"mainplane: release {v} has no {name}")
    if goos == "windows":
        cache = Path(os.environ["LOCALAPPDATA"])
    elif goos == "darwin":
        cache = Path.home() / "Library" / "Caches"
    else:
        cache = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache")
    bin = cache / "mainplane" / v / f"{goos}-{arch}" / ("mainplane.exe" if goos == "windows" else "mainplane")
    sum = line.split(" ")[0]
    if not bin.exists() or hashlib.sha256(bin.read_bytes()).hexdigest() != sum:
        url = f"https://dl.mainplane.ai/{v}/{name}"
        # Cloudflare refuses the default Python-urllib user agent
        b = urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "mainplane-pypi/" + v}), timeout=300).read()
        if hashlib.sha256(b).hexdigest() != sum:
            sys.exit(f"mainplane: {url} does not match SHA256SUMS")
        bin.parent.mkdir(parents=True, exist_ok=True)
        # A temporary file per process, since first runs may race; the loser keeps the winner's identical file.
        tmp = bin.with_name(f"{bin.name}.{os.getpid()}")
        tmp.write_bytes(b)
        tmp.chmod(0o755)
        try:
            os.replace(tmp, bin)
        except OSError:
            tmp.unlink()
            if not bin.exists():
                raise
    if goos == "windows":
        sys.exit(subprocess.call([str(bin), *sys.argv[1:]]))
    os.execv(bin, [str(bin), *sys.argv[1:]])
