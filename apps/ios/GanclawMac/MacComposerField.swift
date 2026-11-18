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
    var onComplete: () -> Bool
    var onImage: (PastedImage) -> Void
    var onFiles: ([URL]) -> Void
    var onPasteError: () -> Void

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
        view.textContainer?.containerSize = NSSize(width: 0, height: CGFloat.greatestFiniteMagnitude)
        view.isVerticallyResizable = true
        view.isHorizontallyResizable = false
        view.minSize = .zero
        view.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        view.autoresizingMask = [.width]
        view.isAutomaticQuoteSubstitutionEnabled = false
        view.isAutomaticDashSubstitutionEnabled = false
        view.setAccessibilityLabel("Message")

        let scroll = ComposerScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = false
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
        view.onComplete = onComplete
        view.onImage = onImage
        view.onFiles = onFiles
        view.onPasteError = onPasteError
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
        guard let width = proposal.width, let view = nsView.documentView as? NSTextView else { return nil }
        return CGSize(width: width, height: Self.measuredHeight(for: view.string, width: width))
    }

    static func measuredHeight(for text: String, width: CGFloat) -> CGFloat {
        // Measure with a separate layout stack. SwiftUI may ask for several
        // proposed widths before choosing one; changing the live document
        // frame here disrupts its scrolling and caret position.
        let storage = NSTextStorage(string: text, attributes: [.font: Self.font])
        let measuringLayout = NSLayoutManager()
        let measuringContainer = NSTextContainer(containerSize: NSSize(width: max(0, width), height: .greatestFiniteMagnitude))
        measuringContainer.lineFragmentPadding = 0
        storage.addLayoutManager(measuringLayout)
        measuringLayout.addTextContainer(measuringContainer)
        measuringLayout.ensureLayout(for: measuringContainer)
        let line = measuringLayout.defaultLineHeight(for: Self.font)
        var used = measuringLayout.usedRect(for: measuringContainer).height
        if measuringLayout.extraLineFragmentTextContainer != nil {
            used = max(used, measuringLayout.extraLineFragmentRect.maxY)
        }
        return min(max(line, used), line * Self.maxLines).rounded(.up)
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

// Keep the document width tied to the actual viewport, not a proposed
// SwiftUI size. The text view can still grow vertically and scroll.
final class ComposerScrollView: NSScrollView {
    override func layout() {
        super.layout()
        guard let view = documentView else { return }
        let width = contentSize.width
        if view.frame.width != width {
            view.setFrameSize(NSSize(width: width, height: view.frame.height))
        }
    }
}

final class ComposerNSTextView: NSTextView {
    var pasteboard: PasteboardClient = .inert
    var onSubmit: () -> Void = {}
    var onComplete: () -> Bool = { false }
    var onImage: (PastedImage) -> Void = { _ in }
    var onFiles: ([URL]) -> Void = { _ in }
    var onPasteError: () -> Void = {}

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { (delegate as? MacComposerField.Coordinator)?.textDidBeginEditing(Notification(name: NSText.didBeginEditingNotification, object: self)) }
        return ok
    }

    override func keyDown(with event: NSEvent) {
        if event.keyCode == 48, event.modifierFlags.intersection([.command, .control, .option, .shift]).isEmpty,
           !hasMarkedText(), onComplete() { return }
        let isReturn = event.keyCode == 36 || event.keyCode == 76 // Return, keypad Enter
        let newline = event.modifierFlags.contains(.shift) || event.modifierFlags.contains(.option)
        if isReturn, !newline, !hasMarkedText() {
            onSubmit()
            return
        }
        super.keyDown(with: event)
    }

    override func validateUserInterfaceItem(_ item: NSValidatedUserInterfaceItem) -> Bool {
        if item.action == #selector(paste(_:)), pasteboard.hasImages() || pasteboard.hasFiles() { return true }
        return super.validateUserInterfaceItem(item)
    }

    override func paste(_ sender: Any?) {
        if pasteboard.hasFiles() {
            let files = pasteboard.fileURLs()
            if files.isEmpty { onPasteError() } else { onFiles(files) }
        } else if pasteboard.hasImages() {
            if let image = pasteboard.pastedImage() { onImage(image) } else { onPasteError() }
        } else {
            pasteAsPlainText(sender)
        }
    }
}
