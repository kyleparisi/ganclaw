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
                if let url = imageFileURL(pb) {
                    let scoped = url.startAccessingSecurityScopedResource()
                    defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                    guard let data = try? Data(contentsOf: url), NSImage(data: data) != nil,
                          let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType else { return nil }
                    return PastedImage(data: data, mime: mime, name: url.lastPathComponent)
                }
                return PastedImage.make(png: pb.data(forType: .png), image: NSImage(pasteboard: pb))
            },
            url: { NSURL(from: pb) as URL? },
            hasContent: { hasImages(pb) || pb.canReadObject(forClasses: [NSURL.self, NSString.self]) },
            hasImages: { hasImages(pb) }
        )
    }

    private static func hasImages(_ pb: NSPasteboard) -> Bool {
        pb.availableType(from: [.png, .tiff, NSPasteboard.PasteboardType(UTType.jpeg.identifier)]) != nil
            || imageFileURL(pb) != nil
    }

    /// Only attach image files; other Finder items must not become their icons.
    private static func imageFileURL(_ pb: NSPasteboard) -> URL? {
        guard let url = (pb.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL])?.first,
              let type = UTType(filenameExtension: url.pathExtension), type.conforms(to: .image) else { return nil }
        return url
    }
}

#endif
