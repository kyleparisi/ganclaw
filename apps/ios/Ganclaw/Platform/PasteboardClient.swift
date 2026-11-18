import Foundation

@MainActor
struct PasteboardClient {
    var string: () -> String?
    var setString: (String) -> Void
    /// The clipboard's image as upload bytes (see `PastedImage.make`).
    var pastedImage: () -> PastedImage?
    var url: () -> URL?
    var hasContent: () -> Bool
    var hasImages: () -> Bool = { false }
    var hasFiles: () -> Bool = { false }
    var fileURLs: () -> [URL] = { [] }
}


extension PasteboardClient {
    static var inert: PasteboardClient {
        PasteboardClient(string: { nil }, setString: { _ in }, pastedImage: { nil }, url: { nil }, hasContent: { false })
    }
}

