import SwiftUI
import UniformTypeIdentifiers

/// Files to send, with an optional note. Files come from the open panel,
/// drag and drop (onto the sheet or the thread), or a pasted image.
struct MacAttachSheet: View {
    @Environment(AppStore.self) private var store
    @State private var showFiles = false
    @State private var dropTargeted = false

    var body: some View {
        let a = store.state.attach
        VStack(alignment: .leading, spacing: 14) {
            Text("Send to \(Theme.displayName(a.agent ?? ""))").font(.headline)

            VStack(spacing: 6) {
                if a.pending.isEmpty {
                    VStack(spacing: 8) {
                        Image(systemName: "tray.and.arrow.down").font(.system(size: 26)).foregroundStyle(Theme.muted)
                        Text("Drop files here").foregroundStyle(Theme.text2)
                        Button("Choose Files…") { showFiles = true }
                    }
                    .frame(maxWidth: .infinity, minHeight: 140)
                } else {
                    ScrollView {
                        VStack(spacing: 6) { ForEach(a.pending) { row($0) } }
                    }
                    .frame(maxHeight: 240)
                    HStack {
                        Button("Add More…") { showFiles = true }
                        Spacer()
                    }
                }
            }
            .padding(10)
            .background(Theme.ground, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(dropTargeted ? Theme.blue : Theme.line, style: StrokeStyle(lineWidth: dropTargeted ? 2 : 1, dash: a.pending.isEmpty ? [5, 4] : [])))
            .dropDestination(for: URL.self) { urls, _ in
                Self.add(urls.filter(\.isFileURL), to: store)
                return !urls.isEmpty
            } isTargeted: { dropTargeted = $0 }

            TextField("Add a note (optional)", text: Binding(get: { store.state.attach.note }, set: { store.send(.attachNote($0)) }), axis: .vertical)
                .lineLimit(1...4)
                .textFieldStyle(.roundedBorder)

            HStack {
                Spacer()
                Button("Cancel") { store.send(.attachClose) }
                    .keyboardShortcut(.cancelAction)
                Button(sendLabel(a)) { Task { await store.sendAttachments() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!a.ready || a.sending)
            }
        }
        .padding(20)
        .frame(width: 460)
        .fileImporter(isPresented: $showFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { Self.add(urls, to: store) }
        }
    }

    private func row(_ p: PendingAttachment) -> some View {
        HStack(spacing: 10) {
            Image(systemName: p.mime.hasPrefix("image/") ? "photo" : "doc")
                .foregroundStyle(Theme.text2)
                .frame(width: 20)
            VStack(alignment: .leading, spacing: 1) {
                Text(p.name).lineLimit(1).truncationMode(.middle)
                Text(p.error ?? (p.uploading ? "Uploading…" : p.size.fileSize))
                    .font(.caption)
                    .foregroundStyle(p.error == nil ? Theme.muted : Theme.red)
            }
            Spacer()
            if p.uploading { ProgressView().controlSize(.small) }
            Button { store.removeAttachment(p.id) } label: { Image(systemName: "xmark.circle.fill") }
                .buttonStyle(.borderless)
                .foregroundStyle(Theme.muted)
                .help("Remove")
        }
        .padding(.horizontal, 8).padding(.vertical, 6)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 8))
    }

    private func sendLabel(_ a: AttachState) -> String {
        if a.sending { return "Sending…" }
        let n = a.pending.count
        return n <= 1 ? "Send" : "Send \(n) Files"
    }

    /// Reads each file and starts its upload. Folders and unreadable files
    /// are reported instead of sent.
    static func add(_ urls: [URL], to store: AppStore) {
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            var isDir: ObjCBool = false
            if FileManager.default.fileExists(atPath: url.path, isDirectory: &isDir), isDir.boolValue {
                store.showToast("Folders can't be attached")
                continue
            }
            guard let data = try? Data(contentsOf: url) else {
                store.send(.banner("Couldn't read \(url.lastPathComponent)."))
                continue
            }
            let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
            store.addAttachment(name: url.lastPathComponent, mime: mime, data: data)
        }
    }
}
