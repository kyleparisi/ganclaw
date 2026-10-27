import Foundation

/// Answers "may this running binary use capability X?" from what it was
/// actually built and signed with, not from what the source says.
struct CapabilityCheck: Sendable {
    /// Looks up an Info.plist key.
    var infoValue: @Sendable (String) -> Any?
    /// Looks up one of the process's own entitlements; nil on platforms
    /// that don't enforce them (iOS).
    var entitlement: (@Sendable (String) -> Bool)?

    /// The first missing declaration for `c` or anything it requires, or
    /// nil when it's safe to use. What `c` builds on is checked first: with
    /// nothing declared, "no microphone" is the cause, not "no speech
    /// recognition".
    func missing(_ c: Capability) -> PlatformError? {
        for need in c.requires + [c] {
            let usage = infoValue(need.usageKey) as? String ?? ""
            if !need.usageKey.isEmpty && usage.isEmpty {
                return .undeclared(need, missing: "\(need.usageKey) in Info.plist")
            }
            if let ent = need.macEntitlement, let has = entitlement, !has(ent) {
                return .undeclared(need, missing: "the \(ent) entitlement")
            }
        }
        return nil
    }

    func require(_ c: Capability) throws {
        if let e = missing(c) { throw e }
    }

    static let live = CapabilityCheck(
        infoValue: { Bundle.main.object(forInfoDictionaryKey: $0) },
        entitlement: MacEntitlements.lookup
    )
}

/// Reads the process's own entitlements on the Mac (Catalyst). The SecTask
/// functions aren't in the Catalyst SDK headers, so they're found at run
/// time; on iOS there is nothing to enforce and this is nil.
enum MacEntitlements {
    static let lookup: (@Sendable (String) -> Bool)? = {
        #if targetEnvironment(macCatalyst)
        typealias Create = @convention(c) (CFAllocator?) -> Unmanaged<AnyObject>?
        typealias Copy = @convention(c) (AnyObject, CFString, UnsafeMutablePointer<Unmanaged<CFError>?>?) -> Unmanaged<AnyObject>?
        guard let lib = dlopen("/System/Library/Frameworks/Security.framework/Security", RTLD_NOW),
              let c = dlsym(lib, "SecTaskCreateFromSelf"),
              let v = dlsym(lib, "SecTaskCopyValueForEntitlement")
        else { return nil }
        let create = unsafeBitCast(c, to: Create.self)
        let copy = unsafeBitCast(v, to: Copy.self)
        guard let task = create(nil)?.takeRetainedValue() else { return nil }
        nonisolated(unsafe) let t = task
        return { name in
            guard let value = copy(t, name as CFString, nil)?.takeRetainedValue() else { return false }
            return (value as? Bool) ?? (value as? NSNumber)?.boolValue ?? false
        }
        #else
        return nil
        #endif
    }()
}
