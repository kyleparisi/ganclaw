import AppKit
import SwiftUI

/// The message box: an NSTextView that grows to six lines, sends on
/// Return (⇧Return or ⌥Return for a new line), and routes pasted images
/// into the attachment flow.
///
/// `focusRequest` focuses it whenever the value changes (on appear, after
/// choosing Reply); `focused` reports what AppKit did.
struct MacComposerField: NSViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool
    var focusRequest: Int
    var placeholder: String
    var pasteboard: PasteboardClient
    var onSubmit: () -> Void
    var onImage: (PastedImage) -> Void

    static let font = NSFont.systemFont(ofSize: 14)
    static let maxLines: CGFloat = 6

    func makeNSView(context: Context) -> NSScrollView {
        let view = ComposerNSTextView()
        view.delegate = context.coordinator
        view.isRichText = false
        view.importsGraphics = false
        view.allowsUndo = true
        view.drawsBackground = false
        view.font = Self.font
        view.textContainerInset = .zero
        view.textContainer?.lineFragmentPadding = 0
        view.textContainer?.widthTracksTextView = true
        view.isVerticallyResizable = true
        view.isHorizontallyResizable = false
        view.autoresizingMask = [.width]
        view.isAutomaticQuoteSubstitutionEnabled = false
        view.isAutomaticDashSubstitutionEnabled = false
        view.setAccessibilityLabel("Message")

        let scroll = NSScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder
        scroll.documentView = view
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let view = scroll.documentView as? ComposerNSTextView else { return }
        context.coordinator.parent = self
        view.pasteboard = pasteboard
        view.onSubmit = onSubmit
        view.onImage = onImage
        view.setAccessibilityHelp(placeholder)
        // Don't replace text mid-composition (input methods): it would
        // commit or drop the marked text and move the caret.
        if !view.hasMarkedText(), view.string != text {
            view.string = text
            view.invalidateIntrinsicContentSize()
        }
        if context.coordinator.lastFocusRequest != focusRequest {
            context.coordinator.lastFocusRequest = focusRequest
            DispatchQueue.main.async { view.window?.makeFirstResponder(view) }
        }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView: NSScrollView, context: Context) -> CGSize? {
        guard let width = proposal.width, let view = nsView.documentView as? NSTextView,
              let layout = view.layoutManager, let container = view.textContainer else { return nil }
        container.containerSize = NSSize(width: width, height: .greatestFiniteMagnitude)
        layout.ensureLayout(for: container)
        let line = layout.defaultLineHeight(for: Self.font)
        let used = layout.usedRect(for: container).height
        return CGSize(width: width, height: min(max(line, used), line * Self.maxLines).rounded(.up))
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: MacComposerField
        var lastFocusRequest: Int?

        init(_ parent: MacComposerField) { self.parent = parent }

        func textDidChange(_ notification: Notification) {
            guard let view = notification.object as? NSTextView else { return }
            if parent.text != view.string { parent.text = view.string }
        }

        func textDidBeginEditing(_ notification: Notification) { report(true) }
        func textDidEndEditing(_ notification: Notification) { report(false) }

        private func report(_ focused: Bool) {
            if parent.focused != focused { parent.focused = focused }
        }
    }
}

final class ComposerNSTextView: NSTextView {
    var pasteboard: PasteboardClient = .inert
    var onSubmit: () -> Void = {}
    var onImage: (PastedImage) -> Void = { _ in }

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { (delegate as? MacComposerField.Coordinator)?.textDidBeginEditing(Notification(name: NSText.didBeginEditingNotification, object: self)) }
        return ok
    }

    override func keyDown(with event: NSEvent) {
        let isReturn = event.keyCode == 36 || event.keyCode == 76 // Return, keypad Enter
        let newline = event.modifierFlags.contains(.shift) || event.modifierFlags.contains(.option)
        if isReturn, !newline, !hasMarkedText() {
            onSubmit()
            return
        }
        super.keyDown(with: event)
    }

    override func paste(_ sender: Any?) {
        guard pasteboard.hasImages(), let image = pasteboard.pastedImage() else {
            pasteAsPlainText(sender)
            return
        }
        onImage(image)
    }
}
