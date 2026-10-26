import Foundation

/// Parses a pairing link: ganclaw://pair?server=https://…&code=ABCD-EFGH
struct PairLink: Equatable {
    var server: URL
    var code: String

    init?(_ url: URL) {
        guard url.scheme == "ganclaw", url.host == "pair",
              let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems,
              let s = items.first(where: { $0.name == "server" })?.value, let server = URL(string: s),
              server.scheme == "https" || server.scheme == "http",
              let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty
        else { return nil }
        self.server = server
        self.code = code
    }
}
