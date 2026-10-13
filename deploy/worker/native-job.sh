#!/bin/bash
# Trusted local profile wrapper. Install outside the application checkout.
# All arguments/env below are selected by the Mac's local configuration.
set -euo pipefail
: "${GANCLAW_JOB_DIR:?}"
output="$GANCLAW_JOB_DIR/output"
work="$GANCLAW_JOB_DIR/work"
mkdir -p "$output" "$work"
case "${1:-}" in
  ios-build)
    : "${XCODE_SCHEME:?}"
    /usr/bin/xcodebuild build -scheme "$XCODE_SCHEME" -configuration Debug \
      -sdk iphonesimulator -destination 'generic/platform=iOS Simulator' \
      -derivedDataPath "$work/DerivedData" CODE_SIGNING_ALLOWED=NO
    ditto "$work/DerivedData/Build/Products/Debug-iphonesimulator" "$output/products"
    ;;
  ios-test)
    : "${XCODE_SCHEME:?}" "${IOS_SIMULATOR_UDID:?}"
    # This simulator is reserved exclusively for the worker.
    /usr/bin/xcrun simctl boot "$IOS_SIMULATOR_UDID"
    /usr/bin/xcrun simctl bootstatus "$IOS_SIMULATOR_UDID" -b
    /usr/bin/xcodebuild test -scheme "$XCODE_SCHEME" -configuration Debug \
      -destination "platform=iOS Simulator,id=$IOS_SIMULATOR_UDID" \
      -parallel-testing-enabled NO -maximum-concurrent-test-simulator-destinations 1 \
      -derivedDataPath "$work/DerivedData" -resultBundlePath "$output/tests.xcresult" \
      'SWIFT_ACTIVE_COMPILATION_CONDITIONS=$(inherited) UI_TESTING' CODE_SIGNING_ALLOWED=NO
    # UI test attachments in xcresult are the authoritative screenshots.
    /usr/bin/xcrun simctl io "$IOS_SIMULATOR_UDID" screenshot "$output/final-screen.png"
    ;;
  android-build)
    ./gradlew --no-daemon :app:clean :app:assembleDebug
    cp -R app/build/outputs/apk "$output/apk"
    ;;
  android-test)
    : "${ANDROID_HOME:?}" "${ANDROID_AVD:?}"
    # Reserve port 5580 for this worker; do not reuse a person's running emulator.
    if "$ANDROID_HOME/platform-tools/adb" -s emulator-5580 get-state >/dev/null 2>&1; then
      echo 'Reserved emulator port already occupied; refusing device reuse' >&2
      exit 1
    fi
    "$ANDROID_HOME/emulator/emulator" -avd "$ANDROID_AVD" -port 5580 \
      -no-snapshot -wipe-data -no-boot-anim >"$output/emulator.log" 2>&1 &
    emulator_pid=$!
    trap 'kill "$emulator_pid" 2>/dev/null || true; wait "$emulator_pid" 2>/dev/null || true' EXIT
    export ANDROID_SERIAL=emulator-5580
    "$ANDROID_HOME/platform-tools/adb" -s "$ANDROID_SERIAL" wait-for-device
    until [ "$("$ANDROID_HOME/platform-tools/adb" -s "$ANDROID_SERIAL" shell getprop sys.boot_completed | tr -d '\r')" = 1 ]; do
      sleep 2
    done
    # Copy evidence even when an assertion makes Gradle fail.
    set +e
    ./gradlew --no-daemon :app:clean :app:connectedDebugAndroidTest
    result=$?
    set -e
    for path in app/build/outputs/androidTest-results app/build/reports/androidTests; do
      if [ -d "$path" ]; then cp -R "$path" "$output/"; fi
    done
    "$ANDROID_HOME/platform-tools/adb" -s "$ANDROID_SERIAL" exec-out screencap -p >"$output/final-screen.png" || true
    # Test-side screenshots belong in the app's external files directory.
    if [ -n "${ANDROID_APP_ID:-}" ]; then
      "$ANDROID_HOME/platform-tools/adb" -s "$ANDROID_SERIAL" pull \
        "/sdcard/Android/data/$ANDROID_APP_ID/files/screenshots" "$output/screenshots" || true
    fi
    exit "$result"
    ;;
  *) echo 'Unknown local profile' >&2; exit 2 ;;
esac
