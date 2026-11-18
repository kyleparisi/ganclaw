#!/bin/bash
# Archives a Release build signed for App Store Connect and exports it, or
# uploads it to TestFlight with --upload.
#
#   apps/ios/tools/testflight.sh                  # archive + export .ipa to build/export
#   apps/ios/tools/testflight.sh --upload         # archive + upload to App Store Connect
#   apps/ios/tools/testflight.sh --mac [--upload] # the same for the native Mac app (.pkg)
#
# The Mac app (GanclawMac target) shares the iOS app's bundle ID, so both
# land in the same App Store Connect record (universal purchase).
#
# Needs a paid Apple Developer team, signed in under Xcode → Settings →
# Accounts. The team comes from $DEVELOPMENT_TEAM, else from
# DEVELOPMENT_TEAM in apps/ios/Local.xcconfig (git-ignored).
#
# Without an Xcode account (CI, .github/workflows/testflight.yml), sign in
# with an App Store Connect API key instead. The keychain must hold the
# Apple Development (archive), Apple Distribution (export) and, for --mac,
# 3rd Party Mac Developer Installer (.pkg) identities:
#
#   ASC_KEY_PATH=AuthKey_XXXX.p8 ASC_KEY_ID=XXXX ASC_ISSUER_ID=<uuid>
#   BUILD_NUMBER=<n>    # optional; defaults to the timestamp below
#
# GANCLAW_SIGN_IDENTITY overrides the archive identity. Don't set it to
# "Apple Distribution": with automatic signing Xcode rejects that.
#
# The build number is a UTC timestamp, so every upload is unique. Push is
# still opt-in through Local.xcconfig; when it is on, the archive uses the
# production APNs environment, which TestFlight needs.
set -euo pipefail

IOS="$(cd "$(dirname "$0")/.." && pwd)"
BUILD="$IOS/build"
DESTINATION=export
PLATFORM=ios
for arg in "$@"; do
  case "$arg" in
    --upload) DESTINATION=upload ;;
    --mac) PLATFORM=mac ;;
    *) echo "usage: $0 [--mac] [--upload]" >&2; exit 2 ;;
  esac
done
if [ "$PLATFORM" = mac ]; then
  SCHEME=GanclawMac
  ARCHIVE_DEST='generic/platform=macOS'
  PRODUCT=Ganclaw.pkg
else
  SCHEME=Ganclaw
  ARCHIVE_DEST='generic/platform=iOS'
  PRODUCT=Ganclaw.ipa
fi

TEAM="${DEVELOPMENT_TEAM:-}"
if [ -z "$TEAM" ] && [ -f "$IOS/Local.xcconfig" ]; then
  TEAM="$(sed -n 's/^[[:space:]]*DEVELOPMENT_TEAM[[:space:]]*=[[:space:]]*\([A-Z0-9]*\).*/\1/p' "$IOS/Local.xcconfig" | tail -1)"
fi
if [ -z "$TEAM" ]; then
  echo "No team: set DEVELOPMENT_TEAM or add 'DEVELOPMENT_TEAM = <team id>' to $IOS/Local.xcconfig" >&2
  exit 1
fi

AUTH=()
if [ -n "${ASC_KEY_PATH:-}" ]; then
  : "${ASC_KEY_ID:?ASC_KEY_ID must be set with ASC_KEY_PATH}"
  : "${ASC_ISSUER_ID:?ASC_ISSUER_ID must be set with ASC_KEY_PATH}"
  AUTH=(-authenticationKeyPath "$ASC_KEY_PATH"
        -authenticationKeyID "$ASC_KEY_ID"
        -authenticationKeyIssuerID "$ASC_ISSUER_ID")
fi
SIGN=()
if [ -n "${GANCLAW_SIGN_IDENTITY:-}" ]; then
  SIGN=(CODE_SIGN_IDENTITY="$GANCLAW_SIGN_IDENTITY")
elif [ "$PLATFORM" = mac ]; then
  # GanclawMac is ad-hoc signed ("-") for everyday builds; an App Store
  # archive needs a real identity (export re-signs it for distribution).
  SIGN=(CODE_SIGN_IDENTITY="Apple Development")
fi

BUILD_NUMBER="${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M)}"
ARCHIVE="$BUILD/$SCHEME.xcarchive"
EXPORT="$BUILD/export-$PLATFORM"
mkdir -p "$BUILD"
rm -rf "$ARCHIVE" "$EXPORT"

echo "==> project"
(cd "$IOS" && xcodegen generate --quiet)

echo "==> archive $SCHEME (team $TEAM, build $BUILD_NUMBER)"
xcodebuild -project "$IOS/Ganclaw.xcodeproj" -scheme "$SCHEME" \
  -configuration Release -destination "$ARCHIVE_DEST" \
  -archivePath "$ARCHIVE" -allowProvisioningUpdates ${AUTH[@]+"${AUTH[@]}"} \
  DEVELOPMENT_TEAM="$TEAM" CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
  ${SIGN[@]+"${SIGN[@]}"} \
  APNS_ENVIRONMENT=production \
  archive

cat > "$BUILD/ExportOptions-$PLATFORM.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>method</key><string>app-store-connect</string>
  <key>destination</key><string>$DESTINATION</string>
  <key>teamID</key><string>$TEAM</string>
  <key>signingStyle</key><string>automatic</string>
  <key>manageAppVersionAndBuildNumber</key><false/>
</dict>
</plist>
EOF

echo "==> $DESTINATION"
xcodebuild -exportArchive -archivePath "$ARCHIVE" \
  -exportOptionsPlist "$BUILD/ExportOptions-$PLATFORM.plist" \
  -exportPath "$EXPORT" -allowProvisioningUpdates ${AUTH[@]+"${AUTH[@]}"}

if [ "$DESTINATION" = upload ]; then
  echo "Uploaded $PLATFORM build $BUILD_NUMBER. It shows up in App Store Connect → TestFlight once processing finishes."
else
  echo "Exported $EXPORT/$PRODUCT (build $BUILD_NUMBER). Rerun with --upload to send it to TestFlight."
fi
