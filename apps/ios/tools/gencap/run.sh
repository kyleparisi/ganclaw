#!/bin/sh
# Builds gencap (with the app's Capability.swift) and runs it.
#   run.sh [ios-dir] [--check]   write (or check) Info.plist + entitlements
#   run.sh --entitlements        print the Mac entitlements
set -e
IOS="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${GENCAP_BIN:-${TMPDIR:-/tmp}/ganclaw-gencap}"
mkdir -p "$(dirname "$OUT")"
SWIFTC="swiftc"
command -v xcrun >/dev/null 2>&1 && SWIFTC="xcrun --sdk macosx swiftc"
# Rebuild only when a source changed.
if [ ! -x "$OUT" ] || [ "$IOS/Ganclaw/Platform/Capability.swift" -nt "$OUT" ] || [ "$IOS/tools/gencap/main.swift" -nt "$OUT" ]; then
  $SWIFTC -O -o "$OUT" "$IOS/Ganclaw/Platform/Capability.swift" "$IOS/tools/gencap/main.swift"
fi
if [ "$1" = "--entitlements" ]; then
  exec "$OUT" --entitlements
fi
DIR="${1:-$IOS}"
[ $# -gt 0 ] && shift
exec "$OUT" "$DIR" "$@"
