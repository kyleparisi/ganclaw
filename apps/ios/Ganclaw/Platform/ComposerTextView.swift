import SwiftUI
import UIKit

/// A plain-text editor that routes pasted images into the attachment flow.
struct ComposerTextView: UIViewRepresentable {
    @Binding var text: String
    var focused: FocusState<Bool>.Binding
    var pasteboard: PasteboardClient
    var onImage: (Data) -> Void

    func makeUIView(context: Context) -> ImagePasteTextView {
        let view = ImagePasteTextView()
        view.delegate = context.coordinator
        view.backgroundColor = .clear
        view.font = .systemFont(ofSize: 15)
        view.textContainerInset = .zero
        view.textContainer.lineFragmentPadding = 0
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        view.accessibilityLabel = "Message"
        return view
    }

    func updateUIView(_ view: ImagePasteTextView, context: Context) {
        context.coordinator.parent = self
        view.pasteboard = pasteboard
        view.onImage = onImage
        if view.text != text { view.text = text }
        if focused.wrappedValue && !view.isFirstResponder { view.becomeFirstResponder() }
        if !focused.wrappedValue && view.isFirstResponder { view.resignFirstResponder() }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: ImagePasteTextView, context: Context) -> CGSize? {
        guard let width = proposal.width else { return nil }
        let line = uiView.font?.lineHeight ?? 18
        let height = uiView.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude)).height
        return CGSize(width: width, height: min(max(line, height), line * 6))
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: ComposerTextView
        init(_ parent: ComposerTextView) { self.parent = parent }
        func textViewDidChange(_ textView: UITextView) { parent.text = textView.text }
        func textViewDidBeginEditing(_ textView: UITextView) { parent.focused.wrappedValue = true }
        func textViewDidEndEditing(_ textView: UITextView) { parent.focused.wrappedValue = false }
    }
}

final class ImagePasteTextView: UITextView {
    var pasteboard: PasteboardClient = .inert
    var onImage: (Data) -> Void = { _ in }

    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        // Checking types does not read clipboard contents or prompt for access.
        if action == #selector(paste(_:)), pasteboard.hasImages() { return true }
        return super.canPerformAction(action, withSender: sender)
    }

    override func paste(_ sender: Any?) {
        guard pasteboard.hasImages() else {
            super.paste(sender)
            return
        }
        guard let image = pasteboard.image(), let data = image.pngData() else { return }
        onImage(data)
    }
}
