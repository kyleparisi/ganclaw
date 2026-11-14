#if canImport(UIKit)
import UIKit
/// The platform's image type, for views that show downloaded images.
typealias PlatformImage = UIImage
#else
import AppKit
typealias PlatformImage = NSImage
#endif
import SwiftUI

extension Image {
    init(platformImage: PlatformImage) {
        #if canImport(UIKit)
        self.init(uiImage: platformImage)
        #else
        self.init(nsImage: platformImage)
        #endif
    }
}

/// An image read from the clipboard, ready to upload.
///
/// `make` is the only place pasted bytes are chosen, so the same clipboard
/// always produces the same upload:
/// - PNG bytes on the clipboard (screenshots, most copied images) pass
///   through unchanged.
/// - Anything else is redrawn upright at its pixel size, so EXIF orientation
///   can't rotate it on the way to the agent, then encoded as PNG if it has
///   transparency and JPEG (the camera's quality) if it doesn't.
struct PastedImage: Equatable {
    var data: Data
    var mime: String
    var name: String

    static let jpegQuality: CGFloat = 0.85

    #if canImport(UIKit)
    static func make(png: Data?, image: UIImage?) -> PastedImage? {
        if let png, !png.isEmpty {
            return PastedImage(data: png, mime: "image/png", name: "pasted.png")
        }
        guard let image, let upright = upright(image) else { return nil }
        if hasAlpha(image), let data = upright.pngData() {
            return PastedImage(data: data, mime: "image/png", name: "pasted.png")
        }
        guard let data = upright.jpegData(compressionQuality: jpegQuality) else { return nil }
        return PastedImage(data: data, mime: "image/jpeg", name: "pasted.jpg")
    }

    /// The image drawn with its orientation applied, one point per pixel.
    static func upright(_ image: UIImage) -> UIImage? {
        let size = CGSize(width: (image.size.width * image.scale).rounded(),
                          height: (image.size.height * image.scale).rounded())
        guard size.width > 0, size.height > 0 else { return nil }
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = !hasAlpha(image)
        format.preferredRange = .standard
        return UIGraphicsImageRenderer(size: size, format: format).image { _ in
            image.draw(in: CGRect(origin: .zero, size: size))
        }
    }

    static func hasAlpha(_ image: UIImage) -> Bool {
        switch image.cgImage?.alphaInfo {
        case .first, .last, .premultipliedFirst, .premultipliedLast: return true
        default: return false
        }
    }
    #else
    /// The Mac clipboard has no EXIF orientation to apply: images are
    /// re-encoded from their bitmap as-is.
    static func make(png: Data?, image: NSImage?) -> PastedImage? {
        if let png, !png.isEmpty {
            return PastedImage(data: png, mime: "image/png", name: "pasted.png")
        }
        guard let image, let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else { return nil }
        let rep = NSBitmapImageRep(cgImage: cg)
        switch cg.alphaInfo {
        case .first, .last, .premultipliedFirst, .premultipliedLast:
            guard let data = rep.representation(using: .png, properties: [:]) else { return nil }
            return PastedImage(data: data, mime: "image/png", name: "pasted.png")
        default:
            guard let data = rep.representation(using: .jpeg, properties: [.compressionFactor: jpegQuality]) else { return nil }
            return PastedImage(data: data, mime: "image/jpeg", name: "pasted.jpg")
        }
    }
    #endif
}
