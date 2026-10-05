#!/bin/sh
# Installs the mainplane CLI; with a join token it also makes this Linux or macOS machine a worker.
# With server it makes this machine the harness instead, from the provider keys set in this shell, and
# logs the CLI in to it. Run it without sudo so those keys reach it; it asks for sudo itself. In a root
# shell with no sudo, both run as root, and code runs as root. The release stamps its version.
#   curl -fsSL https://dl.mainplane.ai/@VERSION@/install.sh | sh -s -- [join token]
#   curl -fsSL https://dl.mainplane.ai/@VERSION@/install.sh | sh -s -- server
set -eu
v=@VERSION@
dl=https://dl.mainplane.ai/$v
case $# in
0 | 1) ;;
*)
  echo "usage: curl -fsSL $dl/install.sh | sh -s -- [join token]" >&2
  echo "       curl -fsSL $dl/install.sh | sh -s -- server" >&2
  exit 2
  ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "no mainplane build for $(uname -m)" >&2; exit 1 ;;
esac
os=$(uname -s | tr '[:upper:]' '[:lower:]')
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
cd "$t"
curl -fsSLO "$dl/SHA256SUMS"
# get fetches this machine's build of release binary $1 as ./$1, checked against SHA256SUMS. Modes are
# set here, since root's umask may be narrower than every user running the CLI.
get() {
  curl -fsSLO "$dl/$1-$os-$arch"
  grep " $1-$os-$arch\$" SHA256SUMS > sum || { echo "release $v has no $1-$os-$arch" >&2; exit 1; }
  if command -v sha256sum > /dev/null; then sha256sum --quiet -c sum; else shasum -a 256 --quiet -c sum; fi
  chmod 755 "$1-$os-$arch"
  mv "$1-$os-$arch" "$1"
}
get mainplane
if [ "${1:-}" = server ]; then
  get mainplane-server
  # install places mainplane-server, runs it as a service, makes this machine the worker admin with the
  # mainplane beside it, which places that in /usr/local/bin, and logs it in
  ./mainplane-server install
elif [ $# -eq 1 ]; then
  ./mainplane install "$1"
else
  mkdir -p -m 755 /usr/local/bin
  mv mainplane /usr/local/bin/mainplane
  echo "mainplane $v installed"
fi
