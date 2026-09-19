#!/bin/sh
# Build the rowset executable with the current Studio embedded. GOOS/GOARCH
# select another target, e.g. GOOS=windows GOARCH=amd64 for Windows.
# The version comes from rowset-studio/package.json (override: ROWSET_VERSION).
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
VERSION=${ROWSET_VERSION:-$(node -p "require('$ROOT_DIR/rowset-studio/package.json').version")}
BUILD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/rowset-build.XXXXXX")
trap 'rm -rf -- "$BUILD_DIR"' EXIT HUP INT TERM
mkdir -p "$BUILD_DIR/rowset-core" "$ROOT_DIR/dists/local"
EXT=""
[ "$(go env GOOS)" = windows ] && EXT=.exe
(
  cd "$ROOT_DIR/rowset-studio"
  VITE_ROWSET_VERSION="$VERSION" npm run build
)
cp "$ROOT_DIR/rowset-core/go.mod" "$ROOT_DIR/rowset-core/go.sum" "$BUILD_DIR/rowset-core/"
cp -R "$ROOT_DIR/rowset-core/cmd" "$ROOT_DIR/rowset-core/internal" "$ROOT_DIR/rowset-core/rowset" "$ROOT_DIR/rowset-core/sqlguard" "$BUILD_DIR/rowset-core/"
cp -R "$ROOT_DIR/rowset-studio/dist/." "$BUILD_DIR/rowset-core/internal/web/dist/"
(
  cd "$BUILD_DIR/rowset-core"
  # Native builds include the DuckDB driver. Cross builds remain portable;
  # set CGO_ENABLED=1 with a target C/C++ toolchain to include DuckDB there.
  ROWSET_CGO=${CGO_ENABLED:-1}
  if [ "$(go env GOOS)/$(go env GOARCH)" != "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" ]; then ROWSET_CGO=${CGO_ENABLED:-0}; fi
  CGO_ENABLED=$ROWSET_CGO go build -trimpath -ldflags="-X main.version=$VERSION" -o "$ROOT_DIR/dists/local/rowset$EXT" ./cmd/rowset
)
printf 'Built %s (version %s)\n' "$ROOT_DIR/dists/local/rowset$EXT" "$VERSION"
