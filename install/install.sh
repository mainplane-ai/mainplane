#!/bin/sh
# Installs the mainplane CLI, unless this machine has one; with an api key it logs this user's CLI in.
# With a join token it makes this Linux or macOS machine a worker. With server it makes this machine
# the harness instead, from the provider keys set in this shell, and logs the CLI in to it. Run it
# without sudo, so the login is yours and those keys reach it; it asks for sudo itself. In a root shell
# with no sudo, all run as root, and code runs as root. The release stamps its version.
#   curl -fsSL https://dl.mainplane.ai/@VERSION@/install.sh | sh -s -- [api key | join token | server]
set -eu
v=@VERSION@
dl=https://dl.mainplane.ai/$v
case $#:${1:-} in
0: | 1:mp_key_* | 1:mp_join_* | 1:server) ;;
*)
  echo "usage: curl -fsSL $dl/install.sh | sh -s -- [api key | join token | server]" >&2
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
case ${1:-} in
server)
  get mainplane
  get mainplane-server
  # install places mainplane-server, runs it as a service, makes this machine the worker admin with the
  # mainplane beside it, which places that in /usr/local/bin, and logs it in
  ./mainplane-server install
  ;;
mp_join_*)
  get mainplane
  ./mainplane install "$1"
  ;;
*)
  # A CLI this machine has is used: a worker's or harness's is root's to replace.
  b=/usr/local/bin/mainplane
  if [ ! -e $b ]; then
    get mainplane
    # root, as on a fresh VPS, may have no sudo
    sudo=sudo
    if [ "$(id -u)" -eq 0 ]; then sudo=; fi
    $sudo mkdir -p -m 755 /usr/local/bin
    $sudo install -m 755 mainplane $b
    echo "mainplane $v installed"
  elif [ $# -eq 0 ]; then
    echo "mainplane is installed: $b"
  fi
  if [ $# -eq 1 ]; then $b login "$1"; fi
  ;;
esac
