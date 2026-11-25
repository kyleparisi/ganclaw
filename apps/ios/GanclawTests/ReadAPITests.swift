import Foundation
import XCTest
@testable import Ganclaw

private final class ReadProtocol: URLProtocol {
    static var inspect: ((URLRequest) -> Void)?
    static var body = #"{"last_read_id":42,"unread_count":0}"#
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.inspect?(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class ReadAPITests: XCTestCase {
    func testReadPositionUsesAuthenticatedThreadEndpoint() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ReadProtocol.self]
        let api = APIClient(baseURL: URL(string: "https://example.com")!, token: "example-token", session: URLSession(configuration: config))
        ReadProtocol.body = #"{"last_read_id":42,"unread_count":0}"#
        ReadProtocol.inspect = { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/app/v1/threads/support/read")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer example-token")
        }
        defer { ReadProtocol.inspect = nil }
        let result = try await api.markRead(agent: "support", messageID: 42)
        XCTAssertEqual(result.lastReadId, 42)
        XCTAssertEqual(result.unreadCount, 0)
    }

    func testActiveRunsUsesAuthenticatedRunsEndpoint() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ReadProtocol.self]
        let api = APIClient(baseURL: URL(string: "https://example.com")!, token: "example-token", session: URLSession(configuration: config))
        ReadProtocol.body = #"{"runs":[{"id":"run-1","agent":"support","kind":"ask","label":"code-review","app_thread":false,"state":"running","progress":"Tool activity observed","started_at":"2026-11-25T04:00:00Z","updated_at":"2026-11-25T04:00:01Z"}]}"#
        ReadProtocol.inspect = { request in
            XCTAssertEqual(request.httpMethod, "GET")
            XCTAssertEqual(request.url?.path, "/app/v1/runs")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer example-token")
        }
        defer { ReadProtocol.inspect = nil }
        let runs = try await api.activeRuns()
        XCTAssertEqual(runs.count, 1)
        XCTAssertEqual(runs[0].label, "code-review")
        XCTAssertEqual(runs[0].appThread, false)
    }
}
