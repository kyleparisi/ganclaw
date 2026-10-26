import UIKit

/// On the Mac (Catalyst) the app keeps its phone layout: a narrow window
/// that can grow, with no title text.
enum MacWindow {
    static func configure() {
        #if targetEnvironment(macCatalyst)
        for scene in UIApplication.shared.connectedScenes.compactMap({ $0 as? UIWindowScene }) {
            scene.sizeRestrictions?.minimumSize = CGSize(width: 380, height: 640)
            scene.sizeRestrictions?.maximumSize = CGSize(width: 720, height: 1400)
            scene.titlebar?.titleVisibility = .hidden
            scene.titlebar?.toolbar = nil
        }
        #endif
    }
}
