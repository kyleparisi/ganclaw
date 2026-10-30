import SwiftUI
import UIKit

/// A plain-text editor that routes pasted images into the attachment flow.
///
/// Focus is a plain `Bool` binding, not `FocusState`: no SwiftUI focusable
/// view owns it, so the text view is the single source of first-responder
/// changes and the binding only ever reports what UIKit did.
struct ComposerTextView: UIViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool
    var placeholder: String
    var pasteboard: PasteboardClient
    var onImage: (PastedImage) -> Void

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
        let coordinator = context.coordinator
        coordinator.parent = self
        view.pasteboard = pasteboard
        view.onImage = onImage
        view.accessibilityHint = placeholder
        // Don't replace text mid-composition (IME, dictation): it would
        // commit or drop the marked text and move the caret.
        if view.markedTextRange == nil, view.text != text { view.text = text }
        coordinator.syncing = true
        view.setWantsFocus(focused)
        coordinator.syncing = false
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
        /// True while updateUIView applies the binding to the view, so the
        /// delegate callbacks that causes don't write back mid-update.
        var syncing = false

        init(_ parent: ComposerTextView) { self.parent = parent }

        func textViewDidChange(_ textView: UITextView) {
            if parent.text != textView.text { parent.text = textView.text }
        }
        func textViewDidBeginEditing(_ textView: UITextView) { report(true) }
        func textViewDidEndEditing(_ textView: UITextView) { report(false) }

        func report(_ focused: Bool) {
            guard !syncing, parent.focused != focused else { return }
            parent.focused = focused
        }
    }
}

final class ImagePasteTextView: UITextView {
    var pasteboard: PasteboardClient = .inert
    var onImage: (PastedImage) -> Void = { _ in }
    private(set) var wantsFocus = false

    /// Becomes or resigns first responder to match `focused`. A request made
    /// before the view is in a window is applied when it gets one.
    func setWantsFocus(_ focused: Bool) {
        wantsFocus = focused
        applyFocus()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        applyFocus()
    }

    private func applyFocus() {
        if wantsFocus, !isFirstResponder, window != nil { becomeFirstResponder() }
        if !wantsFocus, isFirstResponder { resignFirstResponder() }
    }

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
        guard let image = pasteboard.pastedImage() else { return }
        onImage(image)
    }
}
