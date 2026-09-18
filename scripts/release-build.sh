#!/bin/sh
# Build the release assets of Rowset Studio for every supported platform.
# Usage: scripts/release-build.sh VERSION OUTPUT_DIR
#
# Asset names carry no version, so
# https://github.com/rowsetdev/rowset-studio/releases/latest/download/<asset>
# always resolves to the newest release (install.sh and install.ps1 use it).
# The macOS app needs a macOS host (swiftc, lipo, codesign); elsewhere it is
# skipped. ROWSET_SIGN_IDENTITY signs the app with a Developer ID.
set -eu

[ "$#" -eq 2 ] || { printf '%s\n' 'Usage: release-build.sh VERSION OUTPUT_DIR' >&2; exit 2; }
VERSION=$1
case "$VERSION" in ''|*[!0-9A-Za-z._-]*) printf '%s\n' 'Invalid version.' >&2; exit 2 ;; esac
ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
mkdir -p "$2"
OUT=$(CDPATH= cd -- "$2" && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/rowset-release.XXXXXX")
CHILD_PIDS=""
cleanup() {
  if [ -n "$CHILD_PIDS" ]; then
    kill $CHILD_PIDS 2>/dev/null || true
    wait $CHILD_PIDS 2>/dev/null || true
  fi
  rm -rf -- "$WORK"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

# Studio is built once and embedded into a private copy of the Go module, so
# the source tree's embedded placeholder stays untouched.
(
  cd "$ROOT_DIR/rowset-studio"
  VITE_ROWSET_VERSION="$VERSION" npm run build
)
mkdir -p "$WORK/src/rowset-core"
cp "$ROOT_DIR/rowset-core/go.mod" "$ROOT_DIR/rowset-core/go.sum" "$WORK/src/rowset-core/"
cp -R "$ROOT_DIR/rowset-core/cmd" "$ROOT_DIR/rowset-core/internal" "$WORK/src/rowset-core/"
cp -R "$ROOT_DIR/rowset-parser" "$WORK/src/rowset-parser"
rm -rf "$WORK/src/rowset-core/internal/web/dist"
cp -R "$ROOT_DIR/rowset-studio/dist" "$WORK/src/rowset-core/internal/web/dist"

build_target() {
  target=$1
  os=${target%/*}
  arch=${target#*/}
  name="rowset-studio-$os-$arch"
  dir="$WORK/pkg/$name"
  exe=rowset
  [ "$os" = windows ] && exe=rowset.exe
  mkdir -p "$dir"
  (
    cd "$WORK/src/rowset-core"
    # A macOS host builds both macOS targets with CGO, which includes DuckDB;
    # the other targets stay portable non-CGO builds without it.
    cgo=0
    if [ "$os" = darwin ] && [ "$(uname -s)" = Darwin ]; then
      cgo=1
      clang_arch=$arch
      [ "$arch" = amd64 ] && clang_arch=x86_64
      export CC="clang -arch $clang_arch" CXX="clang++ -arch $clang_arch"
      export CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_CXXFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0"
    fi
    CGO_ENABLED=$cgo GOOS=$os GOARCH=$arch go build -p "$PACKAGE_JOBS" -trimpath -ldflags="-s -w -X main.version=$VERSION" -o "$dir/$exe" ./cmd/rowset
  )
  cp "$ROOT_DIR/README.md" "$ROOT_DIR/CHANGELOG.md" "$dir/"
  [ -f "$ROOT_DIR/LICENSE" ] && cp "$ROOT_DIR/LICENSE" "$dir/"
  [ "$os" = linux ] && cp "$ROOT_DIR/rowset-studio/public/favicon.svg" "$dir/rowset-studio.svg"
  if [ "$os" = windows ]; then
    node "$ROOT_DIR/scripts/generate-windows-icon.mjs" "$dir/rowset-studio.ico"
    (cd "$WORK/pkg" && zip -qr "$OUT/$name.zip" "$name")
  else
    tar -C "$WORK/pkg" -czf "$OUT/$name.tar.gz" "$name"
  fi
}

# Cross-target compilation is the slowest part of a release. Build two targets
# at a time by default: enough to use the hosted runner without six large Go
# compiler processes competing for memory. Override for a larger build host.
JOBS=${ROWSET_RELEASE_JOBS:-2}
case "$JOBS" in ''|*[!0-9]*|0) printf '%s\n' 'ROWSET_RELEASE_JOBS must be a positive integer.' >&2; exit 2 ;; esac
# A target has a large driver graph. Capping its internal package builds keeps
# two targets from expanding into dozens of memory-heavy compiler processes.
PACKAGE_JOBS=${ROWSET_GO_PACKAGE_JOBS:-1}
case "$PACKAGE_JOBS" in ''|*[!0-9]*|0) printf '%s\n' 'ROWSET_GO_PACKAGE_JOBS must be a positive integer.' >&2; exit 2 ;; esac
active=0
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  build_target "$target" &
  CHILD_PIDS="$CHILD_PIDS $!"
  active=$((active + 1))
  if [ "$active" -ge "$JOBS" ]; then
    failed=0
    for pid in $CHILD_PIDS; do if ! wait "$pid"; then failed=1; fi; done
    [ "$failed" -eq 0 ] || exit 1
    CHILD_PIDS=""
    active=0
  fi
done
failed=0
for pid in $CHILD_PIDS; do if ! wait "$pid"; then failed=1; fi; done
[ "$failed" -eq 0 ] || exit 1
CHILD_PIDS=""

# One universal macOS app for Apple silicon and Intel.
if [ "$(uname -s)" = Darwin ]; then
  APP="$WORK/app/Rowset Studio.app"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  lipo -create "$WORK/pkg/rowset-studio-darwin-amd64/rowset" "$WORK/pkg/rowset-studio-darwin-arm64/rowset" -output "$APP/Contents/Resources/rowset"
  cp "$ROOT_DIR/deploy/desktop/Info.plist" "$APP/Contents/Info.plist"
  plutil -replace CFBundleShortVersionString -string "$VERSION" "$APP/Contents/Info.plist"
  for arch in arm64 x86_64; do
    swiftc -target "$arch-apple-macos12" -module-cache-path "$WORK/swift-cache" "$ROOT_DIR/deploy/desktop/macos.swift" -o "$WORK/Rowset-$arch" -framework Cocoa
  done
  lipo -create "$WORK/Rowset-arm64" "$WORK/Rowset-x86_64" -output "$APP/Contents/MacOS/Rowset"
  if [ -n "${ROWSET_SIGN_IDENTITY:-}" ]; then
    codesign --force --options runtime --timestamp --sign "$ROWSET_SIGN_IDENTITY" "$APP/Contents/Resources/rowset"
    codesign --force --options runtime --timestamp --sign "$ROWSET_SIGN_IDENTITY" "$APP"
  else
    codesign --force --deep --sign - "$APP"
  fi
  (cd "$WORK/app" && ditto -c -k --keepParent "Rowset Studio.app" "$OUT/rowset-studio-macos.zip")
else
  printf '%s\n' 'Skipping the macOS app: it needs a macOS host.'
fi

cp "$ROOT_DIR/install.sh" "$ROOT_DIR/install.ps1" "$OUT/"
(
  cd "$OUT"
  rm -f SHA256SUMS
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum rowset-studio-* install.sh install.ps1 > SHA256SUMS
  else
    shasum -a 256 rowset-studio-* install.sh install.ps1 > SHA256SUMS
  fi
)
printf 'Release assets for %s in %s\n' "$VERSION" "$OUT"
