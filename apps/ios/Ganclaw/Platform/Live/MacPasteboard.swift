#if os(macOS)
import AppKit
import UniformTypeIdentifiers

extension PasteboardClient {
    static var live: PasteboardClient { macPasteboard(.general) }

    static func macPasteboard(_ pb: NSPasteboard) -> PasteboardClient {
        PasteboardClient(
            string: { pb.string(forType: .string) },
            setString: { s in
                pb.clearContents()
                pb.setString(s, forType: .string)
            },
            pastedImage: {
                guard hasImages(pb) else { return nil }
                return PastedImage.make(png: pb.data(forType: .png), image: NSImage(pasteboard: pb))
            },
            url: { NSURL(from: pb) as URL? },
            hasContent: { hasImages(pb) || pb.canReadObject(forClasses: [NSURL.self, NSString.self]) },
            hasImages: { hasImages(pb) },
            hasFiles: { pb.availableType(from: [.fileURL]) != nil },
            fileURLs: { (pb.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL]) ?? [] }
        )
    }

    private static func hasImages(_ pb: NSPasteboard) -> Bool {
        pb.availableType(from: [.png, .tiff, NSPasteboard.PasteboardType(UTType.jpeg.identifier)]) != nil
    }
}

#endif
