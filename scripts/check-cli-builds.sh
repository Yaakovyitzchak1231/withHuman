#!/bin/sh
set -eu
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT HUP INT TERM
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  os=${target%/*}
  arch=${target#*/}
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$out/withhuman-$os-$arch" ./cmd/withhuman-cli
done
