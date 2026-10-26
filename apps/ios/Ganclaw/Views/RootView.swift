import SwiftUI

struct RootView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let s = store.state
        Group {
            if s.connection == .unpaired {
                PairView()
            } else {
                NavigationStack(path: Binding(get: { store.state.path }, set: { store.send(.setPath($0)) })) {
                    InboxView()
                        .navigationDestination(for: Route.self) { route in
                            switch route {
                            case .thread(let a): ThreadView(agent: a)
                            case .jobs: JobsView()
                            case .job(let j): JobView(job: j)
                            }
                        }
                }
            }
        }
        .preferredColorScheme(.light)
        .overlay(alignment: .top) {
            if let b = s.banner, s.connection != .unpaired {
                BannerView(text: b) { store.send(.banner(nil)) }
                    .padding(.top, 4)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
        }
        .overlay(alignment: .bottom) {
            if let t = s.toast {
                ToastView(text: t)
                    .padding(.bottom, 110)
                    .transition(.opacity.combined(with: .scale(scale: 0.95)))
            }
        }
        .animation(.easeOut(duration: 0.2), value: s.toast)
        .animation(.easeOut(duration: 0.2), value: s.banner)
        .fullScreenCover(isPresented: Binding(get: { store.state.drive.active }, set: { if !$0 { store.exitDrive() } })) {
            DriveView()
        }
        .sheet(isPresented: Binding(get: { store.state.attach.presented }, set: { if !$0 { store.send(.attachClose) } })) {
            AttachSheet()
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
                .presentationCornerRadius(28)
        }
        .onOpenURL { url in
            if let link = PairLink(url) { Task { await store.open(link: link) } }
        }
    }
}

struct ToastView: View {
    var text: String
    var body: some View {
        Text(text)
            .font(.system(size: 14, weight: .semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 16)
            .frame(height: 40)
            .background(Theme.ink, in: Capsule())
            .shadow(color: .black.opacity(0.2), radius: 12, y: 6)
            .control("toast")
            .accessibilityAddTraits(.updatesFrequently)
    }
}

struct BannerView: View {
    var text: String
    var dismiss: () -> Void
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Theme.amberText)
            Text(text).font(.system(size: 14)).foregroundStyle(Theme.ink).frame(maxWidth: .infinity, alignment: .leading)
            Button(action: dismiss) { Image(systemName: "xmark").font(.system(size: 13, weight: .semibold)) }
                .foregroundStyle(Theme.text2)
                .frame(width: 32, height: 32)
                .accessibilityLabel("Dismiss")
        }
        .padding(.leading, 14).padding(.trailing, 6).padding(.vertical, 6)
        .background(Theme.amberChip, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .padding(.horizontal, 16)
        .accessibilityElement(children: .contain)
        .control("banner")
    }
}

struct PairView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let p = store.state.pairing
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Ganclaw").font(.system(size: 34, weight: .bold)).tracking(-0.6)
                    Text("A private messenger for your agents.").font(.system(size: 17)).foregroundStyle(Theme.text2)
                }
                .padding(.top, 40)

                VStack(alignment: .leading, spacing: 6) {
                    Text("On your server, run").font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.text2)
                    Text("ganclaw app pair --name iphone")
                        .font(.system(size: 14, design: .monospaced))
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.chip, in: RoundedRectangle(cornerRadius: 12))
                        .textSelection(.enabled)
                }

                field("Server", text: Binding(get: { store.state.pairing.server }, set: { store.send(.pairFields(server: $0, code: store.state.pairing.code)) }),
                      prompt: "https://ganclaw.example.com", id: "pair.server", keyboard: .URL)
                field("Pairing code", text: Binding(get: { store.state.pairing.code }, set: { store.send(.pairFields(server: store.state.pairing.server, code: $0.uppercased())) }),
                      prompt: "ABCD-EFGH", id: "pair.code", keyboard: .asciiCapable)

                if let e = p.error {
                    Text(e).font(.system(size: 14)).foregroundStyle(Theme.red).control("pair.error", label: e)
                }

                CButton("pair.submit") {
                    Task { await store.pair() }
                } label: {
                    HStack {
                        if p.busy { ProgressView().tint(.white) }
                        Text(p.busy ? "Pairing…" : "Pair this phone").font(.system(size: 16, weight: .semibold))
                    }
                    .frame(maxWidth: .infinity).frame(height: 52)
                    .background(Theme.ink, in: Capsule())
                    .foregroundStyle(.white)
                }
                .disabled(p.busy)

                Text("Or open the ganclaw:// link it prints on this phone.").font(.system(size: 13)).foregroundStyle(Theme.muted)
            }
            .padding(.horizontal, 20)
        }
        .background(Theme.ground.ignoresSafeArea())
    }

    private func field(_ label: String, text: Binding<String>, prompt: String, id: String, keyboard: UIKeyboardType) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(label).font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.text2)
            TextField(prompt, text: text)
                .font(.system(size: 16))
                .keyboardType(keyboard)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 14).frame(height: 48)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: 14))
                .overlay(RoundedRectangle(cornerRadius: 14).stroke(Theme.line))
                .accessibilityLabel(label)
                .control(id)
        }
    }
}

/// Keeps the swipe-back gesture working on screens that hide the
/// navigation bar for their own headers.
extension UINavigationController: @retroactive UIGestureRecognizerDelegate {
    override open func viewDidLoad() {
        super.viewDidLoad()
        interactivePopGestureRecognizer?.delegate = self
    }

    public func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
        viewControllers.count > 1
    }
}
