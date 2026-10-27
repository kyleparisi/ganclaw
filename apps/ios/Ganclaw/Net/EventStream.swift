import Foundation

/// Keeps a WebSocket to /app/v1/events open, reconnecting with backoff.
/// Events and connection changes are delivered on the main actor.
@MainActor
final class EventStream {
    enum Status: String, Codable { case disconnected, connecting, connected }

    var onEvent: (ServerEvent) -> Void = { _ in }
    var onStatus: (Status) -> Void = { _ in }

    private var client: APIClient?
    private var task: URLSessionWebSocketTask?
    private var loop: Task<Void, Never>?
    private var generation = 0

    func connect(_ client: APIClient) {
        disconnect()
        self.client = client
        generation += 1
        let gen = generation
        loop = Task { [weak self] in await self?.run(gen) }
    }

    func disconnect() {
        generation += 1
        loop?.cancel()
        loop = nil
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        onStatus(.disconnected)
    }

    private func run(_ gen: Int) async {
        var backoff: UInt64 = 1
        while !Task.isCancelled, gen == generation, let client {
            onStatus(.connecting)
            let t = client.session.webSocketTask(with: client.eventsRequest())
            task = t
            t.resume()
            var gotHello = false
            do {
                while !Task.isCancelled, gen == generation {
                    let msg = try await t.receive()
                    let data: Data
                    switch msg {
                    case .string(let s): data = Data(s.utf8)
                    case .data(let d): data = d
                    @unknown default: continue
                    }
                    guard let event = try? JSON.decoder.decode(ServerEvent.self, from: data) else { continue }
                    if event.type == "hello" {
                        gotHello = true
                        backoff = 1
                        onStatus(.connected)
                    }
                    onEvent(event)
                }
            } catch {
                // Fall through to reconnect.
            }
            t.cancel(with: .goingAway, reason: nil)
            guard !Task.isCancelled, gen == generation else { return }
            onStatus(.disconnected)
            if gotHello { backoff = 1 }
            try? await Task.sleep(nanoseconds: backoff * 1_000_000_000)
            backoff = min(backoff * 2, 30)
        }
    }
}
