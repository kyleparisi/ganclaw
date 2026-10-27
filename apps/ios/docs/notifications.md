# Native iOS notifications

Push is optional. Without `[app.apns]`, the gateway makes no APNs requests.
Telegram delivery, worker status and hidden task history are unchanged.

## Apple Developer and signing setup (required before live delivery)

1. In your Apple Developer team, enable Push Notifications on the explicit
   App ID matching your app's bundle identifier. Regenerate provisioning
   profiles with the `aps-environment` entitlement and select that team in Xcode.
2. Create an APNs authentication key with permission for that team/topic and
   environment. Download its `.p8` once; retain the key ID and team ID.
   Do not commit the key, device tokens, profiles or signing credentials.
3. Build with `APNS_ENVIRONMENT=development` for development signing.
   For distribution/TestFlight/App Store, use `APNS_ENVIRONMENT=production`.
   `tools/gencap` generates the iOS entitlement from the capability manifest;
   Xcode expands the setting. Confirm the **signed** app's entitlement and
   provisioning profile agree. Simulator builds do not use this entitlement.
4. Place the key outside the repository, readable only by the gateway service
   user (file mode 0600). Add the following to your own gateway configuration:

   ```toml
   [app.apns]
   key_file = "/path/to/private/AuthKey.p8"
   key_id = "APPLE_KEY_ID"
   team_id = "APPLE_TEAM_ID"
   topic = "com.example.ganclaw" # exact signed bundle identifier
   environment = "sandbox"    # production for distribution builds
   ```

   This gateway uses one topic/environment per deployment; pair only matching
   builds. Allow outbound HTTPS/HTTP2 on port 443 to Apple's APNs endpoint.
   Partial configuration or an invalid key fails gateway startup.
5. On a signed physical iPhone, pair with the gateway, open Settings in Ganclaw,
   and tap **Enable notifications**. The system permission prompt appears only
   after this explicit action. Return to the app after changing system permissions.

No Apple services, credentials or signing profiles are provisioned by this
change. Linux checks and simulator CI cannot prove live APNs delivery.

## Behavior and API

- `PUT /app/v1/me/push {"token":"hex APNs token"}` associates the token only
  with the authenticated pairing. Registration runs on each authorized app
  activation and after pairing, handling rotation without persisting a token cache.
  A token belongs to at most one pairing; pairing the same installation again
  transfers its registration. `DELETE /app/v1/me/push` removes that registration.
- `DELETE /app/v1/me` revokes the authenticated pairing and its registration.
  The app waits for server confirmation before forgetting a pairing; offline
  unpair asks you to reconnect. CLI device revocation also cascades registration
  deletion. Already submitted notifications cannot be recalled.
- Final visible agent replies (including scheduled jobs and delivered worker
  results) notify paired devices. User messages, typing, stream chunks, system
  notes, and hidden task history do not. One streamed turn notifies once, including
  file-only results. Hidden worker history never produces a second push. Tasks
  routed solely to Telegram do not create app notifications.
- Alerts contain generic text and the agent thread identifier for navigation,
  never reply text, file names or user messages. Tapping refreshes history and
  opens that agent if it still exists. Foreground banners/sounds are suppressed;
  the app continues showing replies through its existing event stream.
- Delivery is bounded, best effort, with a 5-second request timeout and 15-second
  fanout budget. At most 16 fanouts run concurrently; overload drops hints.
  Notifications expire immediately when a device is offline. There is no retry
  after ambiguous network errors (avoids duplicate alerts), nor a durable queue.
  History remains the source of truth. APNs collapse IDs also coalesce repeated
  requests for a message. Multiple distinct messages can still produce alerts.
- APNs 410 and permanent bad-token/topic responses remove stale registrations;
  registration generations and APNs invalidation timestamps protect refreshed
  tokens from stale in-flight responses. Temporary failures keep registrations.
  Logs omit tokens, provider JWTs, response bodies and key contents.

## Validation before release

Run Go checks and PR iOS/Mac CI, including generated capability checks. On a
signed physical device, verify permission denial/re-enable, token registration,
foreground suppression, background and terminated-app taps, streamed replies,
worker results and scheduled jobs. Re-pair/unpair and revoke a second device to
confirm isolation. Confirm development and distribution signing against their
respective APNs environments. APNs acceptance is not proof a device displayed
an alert: Focus, user settings and OS delivery policy can suppress it.

Apple references: [register with APNs](https://developer.apple.com/documentation/usernotifications/registering-your-app-with-apns),
[send notification requests](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns),
[handle APNs responses](https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns).
