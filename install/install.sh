#!/bin/sh
# Installs the mainplane CLI; with a join token it also makes this Linux or macOS machine a worker.
# The release stamps its version.
#   curl -fsSL https://dl.mainplane.ai/@VERSION@/install.sh | sudo sh -s -- [join token]
set -eu
v=@VERSION@
dl=https://dl.mainplane.ai/$v
[ $# -le 1 ] || { echo "usage: curl -fsSL $dl/install.sh | sudo sh -s -- [join token]" >&2; exit 2; }
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "no mainplane build for $(uname -m)" >&2; exit 1 ;;
esac
f=mainplane-$(uname -s | tr '[:upper:]' '[:lower:]')-$arch
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
cd "$t"
curl -fsSLO "$dl/$f"
curl -fsSLO "$dl/SHA256SUMS"
grep " $f\$" SHA256SUMS > sum
if command -v sha256sum > /dev/null; then sha256sum -c sum; else shasum -a 256 -c sum; fi
chmod +x "$f"
if [ $# -eq 1 ]; then
  "./$f" install "$1"
else
  mkdir -p /usr/local/bin
  mv "$f" /usr/local/bin/mainplane
fi
echo "mainplane $v installed: $(/usr/local/bin/mainplane version)"
