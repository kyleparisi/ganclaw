// gencap: writes what the OS requires for each Capability
// (Ganclaw/Platform/Capability.swift) into the app's Info.plist and the
// Mac entitlements file. Built and run before every compile (project.yml);
// also used by CI.
//
//   gencap <ios-dir>                 write Info.plist + entitlements
//   gencap <ios-dir> --check         exit 1 if either is out of date
//   gencap --entitlements            print the Mac entitlements, one per line
//
// Compiled together with Capability.swift, so the manifest is the only
// place a capability is declared.
import Foundation

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(Data("gencap: \(msg)\n".utf8))
    exit(1)
}

let args = Array(CommandLine.arguments.dropFirst())
if args == ["--entitlements"] {
    Capability.macEntitlements.forEach { print($0) }
    exit(0)
}
guard let dir = args.first else { fail("usage: gencap <ios-dir> [--check] | --entitlements") }
let check = args.dropFirst().contains("--check")
let infoURL = URL(fileURLWithPath: dir).appendingPathComponent("Ganclaw/Info.plist")
let entURL = URL(fileURLWithPath: dir).appendingPathComponent("Ganclaw/Generated/Ganclaw-mac.entitlements")

func plistData(_ obj: Any) -> Data {
    do {
        return try PropertyListSerialization.data(fromPropertyList: obj, format: .xml, options: 0)
    } catch {
        fail("encoding plist: \(error)")
    }
}

// Info.plist: every capability's usage string, and no usage string without
// a capability (so a stale one can't hide a missing declaration).
guard let infoData = try? Data(contentsOf: infoURL),
      var info = try? PropertyListSerialization.propertyList(from: infoData, format: nil) as? [String: Any]
else { fail("can't read \(infoURL.path)") }
for key in info.keys where key.hasPrefix("NS") && key.hasSuffix("UsageDescription") {
    info[key] = nil
}
for c in Capability.allCases where !c.usageKey.isEmpty {
    info[c.usageKey] = c.usageDescription
}
let newInfo = plistData(info)

// Mac entitlements: exactly the manifest's.
var ent: [String: Any] = [:]
for e in Capability.macEntitlements { ent[e] = true }
let newEnt = plistData(ent)
let iosEntURL = URL(fileURLWithPath: dir).appendingPathComponent("Ganclaw/Generated/Ganclaw-ios.entitlements")
let newIOSEnt = plistData(Capability.iosEntitlements)

/// Compares parsed contents, so formatting differences between tools
/// (Xcode, Foundation on Linux) don't count as drift.
func same(_ url: URL, _ data: Data) -> Bool {
    guard let old = try? Data(contentsOf: url),
          let a = try? PropertyListSerialization.propertyList(from: old, format: nil) as? NSDictionary,
          let b = try? PropertyListSerialization.propertyList(from: data, format: nil) as? NSDictionary
    else { return false }
    return a.isEqual(b)
}

var stale: [String] = []
for (url, data) in [(infoURL, newInfo), (entURL, newEnt), (iosEntURL, newIOSEnt)] {
    if same(url, data) { continue }
    if check {
        stale.append(url.lastPathComponent)
    } else {
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        do { try data.write(to: url, options: .atomic) } catch { fail("writing \(url.path): \(error)") }
        print("gencap: wrote \(url.lastPathComponent)")
    }
}
if !stale.isEmpty {
    fail("\(stale.joined(separator: ", ")) out of date with Capability.swift; run apps/ios/tools/gencap/run.sh")
}
