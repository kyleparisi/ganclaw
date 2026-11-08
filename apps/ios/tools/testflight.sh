#!/bin/bash
# Archives a Release build signed for App Store Connect and exports it, or
# uploads it to TestFlight with --upload.
#
#   apps/ios/tools/testflight.sh            # archive + export .ipa to build/export
#   apps/ios/tools/testflight.sh --upload   # archive + upload to App Store Connect
#
# Needs a paid Apple Developer team, signed in under Xcode → Settings →
# Accounts. The team comes from $DEVELOPMENT_TEAM, else from
# DEVELOPMENT_TEAM in apps/ios/Local.xcconfig (git-ignored).
#
# The build number is a UTC timestamp, so every upload is unique. Push is
# still opt-in through Local.xcconfig; when it is on, the archive uses the
# production APNs environment, which TestFlight needs.
set -euo pipefail

IOS="$(cd "$(dirname "$0")/.." && pwd)"
BUILD="$IOS/build"
DESTINATION=export
case "${1:-}" in
  --upload) DESTINATION=upload ;;
  "") ;;
  *) echo "usage: $0 [--upload]" >&2; exit 2 ;;
esac

TEAM="${DEVELOPMENT_TEAM:-}"
if [ -z "$TEAM" ] && [ -f "$IOS/Local.xcconfig" ]; then
  TEAM="$(sed -n 's/^[[:space:]]*DEVELOPMENT_TEAM[[:space:]]*=[[:space:]]*\([A-Z0-9]*\).*/\1/p' "$IOS/Local.xcconfig" | tail -1)"
fi
if [ -z "$TEAM" ]; then
  echo "No team: set DEVELOPMENT_TEAM or add 'DEVELOPMENT_TEAM = <team id>' to $IOS/Local.xcconfig" >&2
  exit 1
fi

BUILD_NUMBER="$(date -u +%Y%m%d%H%M)"
ARCHIVE="$BUILD/Ganclaw.xcarchive"
EXPORT="$BUILD/export"
mkdir -p "$BUILD"
rm -rf "$ARCHIVE" "$EXPORT"

echo "==> project"
(cd "$IOS" && xcodegen generate --quiet)

echo "==> archive (team $TEAM, build $BUILD_NUMBER)"
xcodebuild -project "$IOS/Ganclaw.xcodeproj" -scheme Ganclaw \
  -configuration Release -destination 'generic/platform=iOS' \
  -archivePath "$ARCHIVE" -allowProvisioningUpdates \
  DEVELOPMENT_TEAM="$TEAM" CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
  APNS_ENVIRONMENT=production \
  archive

cat > "$BUILD/ExportOptions.plist" <<EOF
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
  -exportOptionsPlist "$BUILD/ExportOptions.plist" \
  -exportPath "$EXPORT" -allowProvisioningUpdates

if [ "$DESTINATION" = upload ]; then
  echo "Uploaded build $BUILD_NUMBER. It shows up in App Store Connect → TestFlight once processing finishes."
else
  echo "Exported $EXPORT/Ganclaw.ipa (build $BUILD_NUMBER). Rerun with --upload to send it to TestFlight."
fi
