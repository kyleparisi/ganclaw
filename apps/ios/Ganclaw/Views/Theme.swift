import SwiftUI

// Colors and type from the Relay design canvas.
extension Color {
    init(hex: UInt32) {
        self.init(.sRGB, red: Double((hex >> 16) & 0xFF) / 255, green: Double((hex >> 8) & 0xFF) / 255, blue: Double(hex & 0xFF) / 255)
    }
}

enum Theme {
    static let ground = Color(hex: 0xF6F5F2)
    static let card = Color.white
    static let ink = Color(hex: 0x17181A)
    static let text2 = Color(hex: 0x45464A)
    static let muted = Color(hex: 0x6B6C70)
    static let line = Color(hex: 0xE2E0DA)
    static let lineSoft = Color(hex: 0xEFEDE8)
    static let segment = Color(hex: 0xECEAE4)
    static let chip = Color(hex: 0xF1F0EC)
    static let blue = Color(hex: 0x2F5BEA)
    static let blueSoft = Color(hex: 0xDCE4FC)
    static let amber = Color(hex: 0xF2A93B)
    static let amberChip = Color(hex: 0xFDF0DC)
    static let amberText = Color(hex: 0x8A4B00)
    static let teal = Color(hex: 0x1C6E64)
    static let red = Color(hex: 0xA3401C)
    static let redSoft = Color(hex: 0xFBEBE4)
    static let codeBg = Color(hex: 0x1D1F24)
    static let codeFg = Color(hex: 0xD1D5DB)

    // Drive mode is always dark.
    enum Drive {
        static let ground = Color(hex: 0x0B0C0E)
        static let surface = Color(hex: 0x16171A)
        static let ring1 = Color(hex: 0x121316)
        static let ring2 = Color(hex: 0x0F1012)
        static let line = Color(hex: 0x2A2C31)
        static let fg = Color(hex: 0xF4F4F5)
        static let muted = Color(hex: 0xA1A3A8)
        static let soft = Color(hex: 0xC9CACE)
        static let chip = Color(hex: 0x2B2111)
        static let avatarBg = Color(hex: 0x1F2A4D)
        static let avatarFg = Color(hex: 0x9DB4FF)
    }

    /// Avatar colors, picked per agent by name so they stay stable.
    static let avatars: [(bg: Color, fg: Color)] = [
        (Color(hex: 0xE8EDFD), Color(hex: 0x2F5BEA)),
        (Color(hex: 0xEFEAF9), Color(hex: 0x6342B8)),
        (Color(hex: 0xE5F3F1), Color(hex: 0x1C6E64)),
        (Color(hex: 0xFBEBE4), Color(hex: 0xA3401C)),
        (Color(hex: 0xFDF0DC), Color(hex: 0x8A4B00)),
    ]

    static func avatar(for name: String) -> (bg: Color, fg: Color) {
        let sum = name.unicodeScalars.reduce(0) { ($0 &* 31 &+ Int($1.value)) & 0x7FFF_FFFF }
        return avatars[sum % avatars.count]
    }

    static func initials(_ name: String) -> String { AgentNames.initials(name) }
    static func displayName(_ name: String) -> String { AgentNames.displayName(name) }
}

struct AgentAvatar: View {
    var name: String
    var size: CGFloat = 44
    var dim = false

    var body: some View {
        let c = Theme.avatar(for: name)
        Text(Theme.initials(name))
            .font(.system(size: size * 0.36, weight: .bold))
            .foregroundStyle(dim ? Color(hex: 0x55565B) : c.fg)
            .frame(width: size, height: size)
            .background(dim ? Theme.segment : c.bg, in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
            .accessibilityHidden(true)
    }
}

extension Date {
    /// "2m", "1h", "Yesterday", "Oct 22".
    func shortAgo(now: Date = Date()) -> String {
        let s = now.timeIntervalSince(self)
        if s < 60 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m" }
        if s < 86400 { return "\(Int(s / 3600))h" }
        if Calendar.current.isDateInYesterday(self) { return "Yesterday" }
        return formatted(.dateTime.month(.abbreviated).day())
    }
}

extension Int64 {
    var fileSize: String { ByteCountFormatter.string(fromByteCount: self, countStyle: .file) }
}
