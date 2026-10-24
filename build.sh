#!/usr/bin/env bash
# Cross-compile frp-panel for Linux. Output: dist/frp-panel-linux-{amd64,arm64,armv7} + sha256sums.txt
#   PANEL_REPO=you/frp-panel VERSION=0.1.0 ./build.sh
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.1.0)}"
VERSION="${VERSION#v}"
PANEL_REPO="${PANEL_REPO:-wangmancang520/frp-panel}"
LDFLAGS="-s -w -X main.version=${VERSION} -X frp-panel/internal/api.DefaultInstallScriptURL=https://raw.githubusercontent.com/${PANEL_REPO}/main/install.sh"

rm -rf dist && mkdir -p dist
build() { # GOARCH name [GOARM]
  echo "building $2"
  CGO_ENABLED=0 GOOS=linux GOARCH="$1" GOARM="${3:-}" go build -trimpath -ldflags "$LDFLAGS" -o "dist/frp-panel-linux-$2" .
}
build amd64 amd64
build arm64 arm64
build arm armv7 7
(cd dist && sha256sum frp-panel-linux-* > sha256sums.txt 2>/dev/null || shasum -a 256 frp-panel-linux-* > sha256sums.txt)
cp install.sh dist/
ls -lh dist
