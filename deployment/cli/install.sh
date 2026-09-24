#!/bin/sh
# Installs the withhuman CLI: downloads the binary for this machine from the
# release bucket, verifies its checksum, and puts it on the PATH.
#
#   curl -fsSL https://downloads.withhuman.ai/install.sh | sh
#
# Arguments are handed to the installed CLI, including the command:
#
#   curl -fsSL https://downloads.withhuman.ai/install.sh | sh -s -- onboard --url <origin> --code <code>
#   curl -fsSL https://downloads.withhuman.ai/install.sh | sh -s -- agent install --agent <slug> --url <origin>
#
# WITHHUMAN_CLI_BASE     mirror to download from (default https://downloads.withhuman.ai)
# WITHHUMAN_CLI_VERSION  release to install (default latest)
# WITHHUMAN_CLI_INSTALL_DIR  optional directory for the installed executable
set -eu

base="${WITHHUMAN_CLI_BASE:-https://downloads.withhuman.ai}"
version="${WITHHUMAN_CLI_VERSION:-latest}"

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported operating system: $(uname -s) (build from source with: go build ./cmd/withhuman-cli)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  die "curl or wget is required"
fi

if command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum "$1" | cut -d' ' -f1; }
else
  die "shasum or sha256sum is required to verify the download"
fi

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t withhuman)"
trap 'rm -rf "$tmp"' EXIT

file="withhuman-$os-$arch"
say "Downloading $base/cli/$version/$file"
fetch "$base/cli/$version/$file" "$tmp/withhuman"
fetch "$base/cli/$version/SHA256SUMS" "$tmp/SHA256SUMS"

expected="$(grep " $file\$" "$tmp/SHA256SUMS" | head -n1 | cut -d' ' -f1)"
[ -n "$expected" ] || die "SHA256SUMS has no entry for $file"
actual="$(digest "$tmp/withhuman")"
[ "$expected" = "$actual" ] || die "checksum mismatch for $file (expected $expected, got $actual)"
chmod 0755 "$tmp/withhuman"

# The provider's single binary is also called withhuman. It takes
# --data-dir; the CLI does not. Never replace one with the other.
existing="$(command -v withhuman 2>/dev/null || true)"
if [ -n "$existing" ] && "$existing" --help 2>&1 | grep -q -- '--data-dir'; then
  die "$existing is the withHuman server binary, not the CLI. Remove it from the PATH or set a different install location before installing the CLI."
fi

install_to() {
  dir="$1"
  if [ -w "$dir" ]; then
    mv "$tmp/withhuman" "$dir/withhuman"
  else
    say "Installing to $dir needs sudo."
    sudo mv "$tmp/withhuman" "$dir/withhuman"
  fi
  say "Installed $dir/withhuman"
}

if [ -n "${WITHHUMAN_CLI_INSTALL_DIR:-}" ]; then
  target="$WITHHUMAN_CLI_INSTALL_DIR"
  mkdir -p "$target"
  install_to "$target"
elif [ -d /usr/local/bin ] && { [ -w /usr/local/bin ] || command -v sudo >/dev/null 2>&1; }; then
  target=/usr/local/bin
  install_to "$target"
else
  target="$HOME/.local/bin"
  mkdir -p "$target"
  install_to "$target"
  case ":$PATH:" in
    *":$target:"*) ;;
    *) say "Add $target to your PATH, for example: export PATH=\"$target:\$PATH\"" ;;
  esac
fi

"$target/withhuman" version

if [ "$#" -gt 0 ]; then
  # A piped script consumes stdin. Reopen the controlling terminal for
  # runtime selection when available; otherwise the CLI reports whether
  # this command needs interactive input. Probe in a subshell because a
  # failed exec redirection exits some POSIX shells even inside an if.
  if (exec 3</dev/tty) 2>/dev/null; then
    exec 0</dev/tty
  fi
  rm -rf "$tmp"
  trap - EXIT
  exec "$target/withhuman" "$@"
fi
