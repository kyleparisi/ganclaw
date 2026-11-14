import SwiftUI

// Navigation-bar modifiers exist only on iOS. Shared views call these so
// the same file builds for the native Mac app, where they do nothing.
extension View {
    /// Hides the navigation bar: the screen draws its own header.
    func hidesNavigationBar() -> some View {
        #if os(iOS)
        toolbar(.hidden, for: .navigationBar)
        #else
        self
        #endif
    }

    /// A visible bar with a small, centered title.
    func inlineNavigationBar() -> some View {
        #if os(iOS)
        navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
        #else
        self
        #endif
    }
}
