import Foundation
import XCTest
@testable import Ganclaw

private final class ReadProtocol: URLProtocol {
    static var inspect: ((URLRequest) -> Void)?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.inspect?(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"last_read_id":42,"unread_count":0}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class ReadAPITests: XCTestCase {
    func testReadPositionUsesAuthenticatedThreadEndpoint() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ReadProtocol.self]
        let api = APIClient(baseURL: URL(string: "https://example.com")!, token: "example-token", session: URLSession(configuration: config))
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
}
