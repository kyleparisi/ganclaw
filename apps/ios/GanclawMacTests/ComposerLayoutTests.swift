import AppKit
import SwiftUI
import XCTest

@MainActor
final class ComposerLayoutTests: XCTestCase {
    func testTypingWrapsWithinViewportAndKeepsCaretVisible() throws {
        let scroll = MacComposerField.makeScrollView()
        scroll.frame = NSRect(x: 0, y: 0, width: 150, height: 80)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 150, height: 80),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = scroll
        window.makeKeyAndOrderFront(nil)
        defer { window.orderOut(nil) }
        scroll.layoutSubtreeIfNeeded()
        let view = try XCTUnwrap(scroll.documentView as? ComposerNSTextView)
        window.makeFirstResponder(view)

        let typed = String(repeating: "words to wrap ", count: 30)
        for character in typed {
            view.insertText(String(character), replacementRange: view.selectedRange())
        }
        scroll.layoutSubtreeIfNeeded()
        let layout = try XCTUnwrap(view.layoutManager)
        let container = try XCTUnwrap(view.textContainer)
        layout.ensureLayout(for: container)

        XCTAssertEqual(view.string, typed)
        XCTAssertEqual(view.frame.width, scroll.contentSize.width, accuracy: 0.5)
        XCTAssertLessThanOrEqual(layout.usedRect(for: container).width, scroll.contentSize.width + 0.5)
        XCTAssertGreaterThan(view.frame.height, scroll.contentSize.height)
        let caret = view.firstRect(forCharacterRange: view.selectedRange(), actualRange: nil)
        let viewport = window.convertToScreen(scroll.convert(scroll.bounds, to: nil))
        XCTAssertTrue(viewport.intersects(caret), "Caret should remain visible after typing")
    }

    func testPastingMultilineTextReplacesSelectionAndWraps() throws {
        let scroll = MacComposerField.makeScrollView()
        scroll.frame = NSRect(x: 0, y: 0, width: 120, height: 80)
        scroll.layoutSubtreeIfNeeded()
        let view = try XCTUnwrap(scroll.documentView as? ComposerNSTextView)
        view.string = "Before selected after"
        view.setSelectedRange(NSRange(location: 7, length: 8))
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let pasted = String(repeating: "pasted words ", count: 8) + "\nnext line"
        board.clearContents()
        XCTAssertTrue(board.setString(pasted, forType: .string))

        XCTAssertTrue(view.readSelection(from: board, type: .string))
        scroll.layoutSubtreeIfNeeded()
        let layout = try XCTUnwrap(view.layoutManager)
        let container = try XCTUnwrap(view.textContainer)
        layout.ensureLayout(for: container)
        XCTAssertEqual(view.string, "Before " + pasted + " after")
        XCTAssertEqual(view.selectedRange(), NSRange(location: ("Before " + pasted).utf16.count, length: 0))
        XCTAssertGreaterThan(layout.usedRect(for: container).height, MacComposerField.font.pointSize * 2)
        XCTAssertLessThanOrEqual(layout.usedRect(for: container).width, scroll.contentSize.width + 0.5)
    }

    func testTypingUpdatesBoundDraft() throws {
        var draft = ""
        let field = MacComposerField(
            text: Binding(get: { draft }, set: { draft = $0 }),
            focused: .constant(false), focusRequest: 0, placeholder: "Message",
            pasteboard: .inert, onSubmit: {}, onComplete: { false },
            onImage: { _ in }, onFiles: { _ in }, onPasteError: {}
        )
        let coordinator = field.makeCoordinator()
        let scroll = MacComposerField.makeScrollView()
        let view = try XCTUnwrap(scroll.documentView as? ComposerNSTextView)
        view.delegate = coordinator
        view.insertText("Hello world", replacementRange: view.selectedRange())
        XCTAssertEqual(draft, "Hello world")
    }

    func testResizingViewportReflowsText() throws {
        let scroll = MacComposerField.makeScrollView()
        scroll.frame = NSRect(x: 0, y: 0, width: 240, height: 80)
        let view = try XCTUnwrap(scroll.documentView as? ComposerNSTextView)
        view.string = String(repeating: "words to wrap ", count: 12)
        scroll.layoutSubtreeIfNeeded()
        let layout = try XCTUnwrap(view.layoutManager)
        let container = try XCTUnwrap(view.textContainer)
        layout.ensureLayout(for: container)
        let wideHeight = layout.usedRect(for: container).height

        scroll.setFrameSize(NSSize(width: 100, height: 80))
        scroll.layoutSubtreeIfNeeded()
        layout.ensureLayout(for: container)
        XCTAssertEqual(view.frame.width, scroll.contentSize.width, accuracy: 0.5)
        XCTAssertGreaterThan(layout.usedRect(for: container).height, wideHeight)
    }
}
