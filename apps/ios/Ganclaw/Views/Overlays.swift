import SwiftUI

struct ToastView: View {
    var text: String
    var body: some View {
        Text(text)
            .font(.system(size: 14, weight: .semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 16)
            .frame(height: 40)
            .background(Theme.ink, in: Capsule())
            .shadow(color: .black.opacity(0.2), radius: 12, y: 6)
            .control("toast")
            .accessibilityAddTraits(.updatesFrequently)
    }
}

struct BannerView: View {
    var text: String
    var dismiss: () -> Void
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Theme.amberText)
            Text(text).font(.system(size: 14)).foregroundStyle(Theme.ink).frame(maxWidth: .infinity, alignment: .leading)
            Button(action: dismiss) { Image(systemName: "xmark").font(.system(size: 13, weight: .semibold)) }
                .foregroundStyle(Theme.text2)
                .frame(width: 32, height: 32)
                .accessibilityLabel("Dismiss")
        }
        .padding(.leading, 14).padding(.trailing, 6).padding(.vertical, 6)
        .background(Theme.amberChip, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .padding(.horizontal, 16)
        .accessibilityElement(children: .contain)
        .control("banner")
    }
}
