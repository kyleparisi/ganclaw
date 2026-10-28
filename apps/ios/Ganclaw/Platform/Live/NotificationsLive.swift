import UIKit
import UserNotifications

/// Kept for the app lifetime; Apple holds its notification delegate weakly.
@MainActor
final class NotificationDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    static var tokenReceived: ((String) -> Void)?
    static var opened: ((String) -> Void)?
    static var failed: ((String) -> Void)?
    static var pendingThread: String?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Self.tokenReceived?(deviceToken.map { String(format: "%02x", $0) }.joined())
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        Self.failed?("Notifications could not connect. Try again in Settings.")
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        // The live inbox already displays replies; no duplicate banner or sound.
        completionHandler([])
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        let thread = response.notification.request.content.userInfo["thread"] as? String
        Task { @MainActor in
            if let thread {
                if let opened = Self.opened { opened(thread) }
                else { Self.pendingThread = thread }
            }
            completionHandler()
        }
    }
}

extension NotificationClient {
    static func live(check: CapabilityCheck) -> NotificationClient {
        #if targetEnvironment(macCatalyst)
        return .inert
        #else
        return NotificationClient(setBadge: { count in
            try? await UNUserNotificationCenter.current().setBadgeCount(count)
        }, enable: {
            try check.require(.notifications)
            guard try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) else {
                throw PlatformError.denied(.notifications)
            }
            UIApplication.shared.registerForRemoteNotifications()
        }, registerIfAuthorized: {
            let status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
            guard status == .authorized || status == .provisional else { return false }
            UIApplication.shared.registerForRemoteNotifications()
            return true
        }, observe: { token, opened, failed in
            NotificationDelegate.tokenReceived = token
            NotificationDelegate.opened = opened
            NotificationDelegate.failed = failed
            if let thread = NotificationDelegate.pendingThread {
                NotificationDelegate.pendingThread = nil
                opened(thread)
            }
        })
        #endif
    }
}
