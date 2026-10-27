import Foundation
import XCTest
@testable import Ganclaw

private final class NotificationProtocol: URLProtocol {
    static var respond: ((URLRequest) -> Void)?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.respond?(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{\"ok\":true}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class NotificationAPITests: XCTestCase {
    func testRegistrationAndRemovalUseAuthenticatedOwnDeviceRoutes() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [NotificationProtocol.self]
        let session = URLSession(configuration: config)
        defer { session.invalidateAndCancel(); NotificationProtocol.respond = nil }
        let api = APIClient(baseURL: URL(string: "https://gateway.example")!, token: "example-pairing", session: session)
        NotificationProtocol.respond = { request in
            XCTAssertEqual(request.httpMethod, "PUT")
            XCTAssertEqual(request.url?.path, "/app/v1/me/push")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer example-pairing")
        }
        try await api.registerPush("example-apns-token")
        NotificationProtocol.respond = { request in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url?.path, "/app/v1/me/push")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer example-pairing")
        }
        try await api.removePush()
        NotificationProtocol.respond = { request in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url?.path, "/app/v1/me")
        }
        try await api.unpair()
    }
}
