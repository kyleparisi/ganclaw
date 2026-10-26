#!/bin/bash
# Builds the iOS app, boots a simulator and runs the control-server tests
# against the fake ganclaw server. Same script locally and in CI.
#
#   apps/ios/test/run.sh            # build, unit tests, control-server tests
#   SKIP_UNIT=1 apps/ios/test/run.sh
#   PYTEST_ARGS="-k drive" apps/ios/test/run.sh
#
# Needs Xcode, xcodegen (brew install xcodegen), Go and Python 3.
set -euo pipefail

IOS="$(cd "$(dirname "$0")/.." && pwd)"
REPO="$(cd "$IOS/../.." && pwd)"
BUILD="$IOS/build"
export ARTIFACTS="${ARTIFACTS:-$IOS/test/artifacts}"
mkdir -p "$BUILD" "$ARTIFACTS"

echo "==> fake server"
(cd "$REPO" && go build -o "$BUILD/fakeserver" ./apps/ios/tools/fakeserver)
export FAKESERVER_BIN="$BUILD/fakeserver"

echo "==> capability declarations"
# Committed Info.plist and entitlements must match Platform/Capability.swift.
"$IOS/tools/gencap/run.sh" "$IOS" --check

echo "==> project"
(cd "$IOS" && xcodegen generate --quiet)

echo "==> simulator"
if [ -z "${GANCLAW_SIM:-}" ]; then
  # The newest plain "iPhone <number>" (390x844-class, like the design).
  GANCLAW_SIM="$(xcrun simctl list devices available -j | python3 -c '
import json, re, sys
devs = json.load(sys.stdin)["devices"]
best = None
for runtime, items in devs.items():
    if "iOS" not in runtime:
        continue
    ver = tuple(int(x) for x in runtime.rsplit("iOS-", 1)[-1].split("-") if x.isdigit())
    for d in items:
        m = re.fullmatch(r"iPhone (\d+)", d["name"])
        if m:
            key = (ver, int(m.group(1)))
            if best is None or key > best[0]:
                best = (key, d["udid"], d["name"])
if best is None:
    sys.exit("no available iPhone simulator")
print(best[1])
')"
fi
export GANCLAW_SIM
echo "    using $GANCLAW_SIM"
xcrun simctl boot "$GANCLAW_SIM" 2>/dev/null || true
xcrun simctl bootstatus "$GANCLAW_SIM" -b >/dev/null

DEST="platform=iOS Simulator,id=$GANCLAW_SIM"
XCB=(xcodebuild -project "$IOS/Ganclaw.xcodeproj" -scheme Ganclaw -configuration Debug -destination "$DEST" -derivedDataPath "$BUILD/dd" CODE_SIGNING_ALLOWED=NO)

if [ -z "${SKIP_UNIT:-}" ]; then
  echo "==> build + unit tests"
  "${XCB[@]}" test -resultBundlePath "$ARTIFACTS/unit.xcresult" 2>&1 | tee "$ARTIFACTS/xcodebuild-test.log" | grep -E "error:|warning: .*Ganclaw/|Test Case .* (passed|failed)|\*\* (TEST|BUILD) " || true
  [ "${PIPESTATUS[0]}" -eq 0 ] || { echo "unit tests failed; see $ARTIFACTS/xcodebuild-test.log"; exit 1; }
else
  echo "==> build"
  "${XCB[@]}" build 2>&1 | tee "$ARTIFACTS/xcodebuild-build.log" | grep -E "error:|\*\* BUILD " || true
  [ "${PIPESTATUS[0]}" -eq 0 ] || { echo "build failed; see $ARTIFACTS/xcodebuild-build.log"; exit 1; }
fi

APP="$BUILD/dd/Build/Products/Debug-iphonesimulator/Ganclaw.app"
echo "==> install"
xcrun simctl install "$GANCLAW_SIM" "$APP"
# The fake server runs on plain http://127.0.0.1, allowed by
# NSAllowsLocalNetworking. Grant permissions so no prompt covers the UI.
xcrun simctl privacy "$GANCLAW_SIM" grant microphone com.kyleparisi.ganclaw 2>/dev/null || true
xcrun simctl privacy "$GANCLAW_SIM" grant photos com.kyleparisi.ganclaw 2>/dev/null || true

echo "==> control-server tests"
python3 -m venv "$BUILD/venv" >/dev/null 2>&1 || true
"$BUILD/venv/bin/pip" install --quiet pytest requests
cd "$IOS/test"
"$BUILD/venv/bin/python" -m pytest -v ${PYTEST_ARGS:-} --junitxml="$ARTIFACTS/control-tests.xml"
