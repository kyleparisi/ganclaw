import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

struct AttachSheet: View {
    @Environment(AppStore.self) private var store
    @State private var photoItems: [PhotosPickerItem] = []
    @State private var showFiles = false
    @State private var showCamera = false
    @State private var showLink = false
    @State private var link = ""

    var body: some View {
        let a = store.state.attach
        let agentName = Theme.displayName(a.agent ?? "")
        NavigationStack {
            VStack(alignment: .leading, spacing: 16) {
                HStack(spacing: 8) {
                    source("Camera", "camera", id: "attach.camera", enabled: store.platform.device.cameraAvailable()) { showCamera = true }
                    source("Files", "doc", id: "attach.files") { showFiles = true }
                    source("Link", "link", id: "attach.link") { showLink = true }
                    source("Paste", "doc.on.clipboard", id: "attach.paste", enabled: store.platform.pasteboard.hasContent()) { paste() }
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("Recent photos").font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.text2)
                    PhotosPicker(selection: $photoItems, maxSelectionCount: 10, selectionBehavior: .continuousAndOrdered, matching: .images, photoLibrary: .shared()) {
                        Text("All photos")
                    }
                    .photosPickerStyle(.inline)
                    .photosPickerDisabledCapabilities([.search, .collectionNavigation, .stagingArea])
                    .photosPickerAccessoryVisibility(.hidden, edges: .all)
                    .frame(height: 96)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                    .control("attach.photos")
                }

                tray(a, agentName: agentName)

                Spacer(minLength: 0)

                CButton("attach.send") { Task { await store.sendAttachments() } } label: {
                    Text(sendLabel(a))
                        .font(.system(size: 16, weight: .semibold))
                        .frame(maxWidth: .infinity).frame(height: 52)
                        .background(a.ready && !a.sending ? Theme.blue : Theme.blue.opacity(0.4), in: Capsule())
                        .foregroundStyle(.white)
                }
                .disabled(!a.ready || a.sending)
            }
            .padding(.top, 16)
            .padding(.horizontal, 16)
            .padding(.bottom, 12)
            .background(Color.white)
            .toolbar(.visible, for: .navigationBar)
            .navigationTitle("Attach")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    CButton("attach.cancel") { store.send(.attachClose) } label: {
                        Text("Cancel")
                            .frame(minWidth: 44, minHeight: 44)
                            .contentShape(Rectangle())
                    }
                    .foregroundStyle(Theme.blue)
                }
            }
        }
        .onChange(of: photoItems) { _, items in Task { await addPhotos(items) } }
        .fileImporter(isPresented: $showFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { urls.forEach(addFile) }
        }
        .sheet(isPresented: $showCamera) {
            CameraPicker { image in
                if let data = image.jpegData(compressionQuality: 0.85) {
                    store.addAttachment(name: "photo.jpg", mime: "image/jpeg", data: data)
                }
            }
            .ignoresSafeArea()
        }
        .alert("Add a link", isPresented: $showLink) {
            TextField("https://", text: $link).keyboardType(.URL).textInputAutocapitalization(.never)
            Button("Add") { addLink(link); link = "" }
            Button("Cancel", role: .cancel) { link = "" }
        }
    }

    private func sendLabel(_ a: AttachState) -> String {
        if a.sending { return "Sending…" }
        if a.pending.contains(where: \.uploading) { return "Uploading…" }
        let n = a.pending.count
        return n == 0 ? "Choose something to send" : "Send \(n) \(n == 1 ? "attachment" : "attachments")"
    }

    private func source(_ title: String, _ icon: String, id: String, enabled: Bool = true, action: @escaping () -> Void) -> some View {
        CButton(id, action: action) {
            VStack(spacing: 6) {
                Image(systemName: icon).font(.system(size: 20))
                Text(title).font(.system(size: 12, weight: .medium))
            }
            .frame(maxWidth: .infinity).frame(height: 76)
            .background(Theme.ground, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(Theme.line))
            .foregroundStyle(enabled ? Theme.ink : Theme.muted)
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
    }

    private func tray(_ a: AttachState, agentName: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            let count = a.pending.isEmpty ? "Nothing selected" : "Ready to send · \(a.pending.count)"
            Text(count)
                .font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.text2)
                .control("attach.count", label: count)
            if !a.pending.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 10) {
                        ForEach(a.pending) { PendingChip(item: $0) }
                    }
                    .padding(.vertical, 6).padding(.trailing, 6)
                }
            }
            TextField("Add a note for \(agentName) (optional)", text: Binding(get: { store.state.attach.note }, set: { store.send(.attachNote($0)) }), axis: .vertical)
                .font(.system(size: 14))
                .lineLimit(1...4)
                .padding(.horizontal, 12).padding(.vertical, 12)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: 14))
                .overlay(RoundedRectangle(cornerRadius: 14).stroke(Theme.line))
                .accessibilityLabel("Caption")
                .control("attach.note")
        }
        .padding(12)
        .background(Theme.ground, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
    }

    // MARK: Sources

    private func addPhotos(_ items: [PhotosPickerItem]) async {
        guard !items.isEmpty else { return }
        photoItems = []
        for (i, item) in items.enumerated() {
            guard let data = try? await item.loadTransferable(type: Data.self) else { continue }
            let type = item.supportedContentTypes.first
            let ext = type?.preferredFilenameExtension ?? "jpg"
            let mime = type?.preferredMIMEType ?? "image/jpeg"
            store.addAttachment(name: "photo-\(i + 1).\(ext)", mime: mime, data: data)
        }
    }

    private func addFile(_ url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        guard let data = try? Data(contentsOf: url) else {
            store.send(.banner("Couldn't read \(url.lastPathComponent)."))
            return
        }
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        store.addAttachment(name: url.lastPathComponent, mime: mime, data: data)
    }

    private func addLink(_ s: String) {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        let note = store.state.attach.note
        store.send(.attachNote(note.isEmpty ? t : note + "\n" + t))
    }

    private func paste() {
        let pb = store.platform.pasteboard
        if let img = pb.image(), let data = img.pngData() {
            store.addAttachment(name: "pasted.png", mime: "image/png", data: data)
        } else if let url = pb.url() {
            addLink(url.absoluteString)
        } else if let s = pb.string() {
            store.addAttachment(name: "pasted.txt", mime: "text/plain", data: Data(s.utf8))
        }
    }

}

struct PendingChip: View {
    var item: PendingAttachment
    @Environment(AppStore.self) private var store

    var body: some View {
        HStack(spacing: 10) {
            Text(AppFile(id: "", name: item.name, mime: item.mime, size: item.size).badge)
                .font(.system(size: 9, weight: .bold, design: .monospaced))
                .foregroundStyle(item.mime == "application/pdf" ? Theme.red : Theme.text2)
                .frame(width: 30, height: 36)
                .background(item.mime == "application/pdf" ? Theme.redSoft : Theme.chip, in: RoundedRectangle(cornerRadius: 6))
            VStack(alignment: .leading, spacing: 5) {
                Text(item.name).font(.system(size: 12, weight: .semibold)).lineLimit(1)
                if let e = item.error {
                    Text(e).font(.system(size: 11)).foregroundStyle(Theme.red).lineLimit(1)
                } else if item.uploading {
                    ProgressView().progressViewStyle(.linear).tint(Theme.blue)
                } else {
                    Text(item.size.fileSize).font(.system(size: 11)).foregroundStyle(Theme.muted)
                }
            }
            .frame(width: 110, alignment: .leading)
        }
        .padding(.horizontal, 10).frame(height: 56)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(item.error == nil ? Theme.line : Theme.red.opacity(0.5)))
        .overlay(alignment: .topTrailing) {
            CButton("attach.remove.\(item.name)") { store.removeAttachment(item.id) } label: {
                Image(systemName: "xmark").font(.system(size: 9, weight: .bold))
                    .frame(width: 22, height: 22)
                    .background(Theme.ink, in: Circle())
                    .overlay(Circle().stroke(Theme.ground, lineWidth: 2))
                    .foregroundStyle(.white)
            }
            .offset(x: 6, y: -6)
            .accessibilityLabel("Remove \(item.name)")
        }
        .accessibilityElement(children: .contain)
        .control("attach.item.\(item.name)")
    }
}

struct CameraPicker: UIViewControllerRepresentable {
    var onImage: (UIImage) -> Void
    @Environment(\.dismiss) private var dismiss

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let c = UIImagePickerController()
        c.sourceType = .camera
        c.delegate = context.coordinator
        return c
    }

    func updateUIViewController(_ c: UIImagePickerController, context: Context) {}
    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let parent: CameraPicker
        init(_ p: CameraPicker) { parent = p }
        func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            if let img = info[.originalImage] as? UIImage { parent.onImage(img) }
            parent.dismiss()
        }
        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) { parent.dismiss() }
    }
}
