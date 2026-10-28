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
3. Push is off in a plain checkout, so builds signed by a free Personal Team
   still work. Turn it on with `apps/ios/Local.xcconfig` (git-ignored), then
   rerun `xcodegen generate`:

   ```
   GANCLAW_PUSH_ENTITLEMENTS = Ganclaw/Generated/Ganclaw-ios.entitlements
   APNS_ENVIRONMENT = development   // production for TestFlight/App Store
   ```

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
  The app tells the server first; if it can't be reached, the phone unpairs
  anyway and says the server may keep notifying until `ganclaw app unpair`
  is run there. CLI device revocation also cascades registration
  deletion. Already submitted notifications cannot be recalled.
- Final visible agent replies (including scheduled jobs and delivered worker
  results) notify paired devices. User messages, typing, stream chunks, system
  notes, and hidden task history do not. One streamed turn notifies once, including
  file-only results. Hidden worker history never produces a second push. Tasks
  routed solely to Telegram do not create app notifications.
- Alerts name the agent, and the job for job runs ("infra replied.",
  "infra: daily-check failed."), plus the thread identifier for navigation;
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

## Unread messages and read positions

Read positions are stored in SQLite per agent conversation, shared by all
paired devices. `GET /app/v1/agents` includes `last_read_id` and `unread_count`.
`PUT /app/v1/threads/{agent}/read` accepts `{"message_id":42}` and returns those
two fields. It requires an existing message in that conversation and advances
monotonically. A `read` WebSocket event broadcasts the shared position to other
connected devices; reconnecting clients reload the counts from the server.

Unread counts include finalized incoming messages, job reports, files and
system errors. User messages, streaming drafts and hidden task mirror threads
are excluded. An unfinished reply blocks advancing beyond it, so its eventual
completion cannot be silently marked read. Existing history starts unread
until the conversation is read; upgrading does not discard history.

The app shows per-agent unread counts and a New messages divider at the entry
read position. It opens at the first unread message in the loaded history page
(up to 100 messages); earlier unread history is not automatically paginated.
The divider stays in place for that visit. Reaching the bottom in the active
foreground conversation marks through the displayed message after a short delay;
scrolling above the bottom leaves new messages unread. Jump to latest returns
to the bottom. Reading a job detail alone does not advance the conversation's
cursor past other unseen messages.

APNs alerts carry the total unread count for configured agents as their badge.
The foreground app refreshes its icon badge on reload and read updates and
requests badge authorization along with alerts and sound. APNs is best effort:
a suspended second device's badge may stay stale until its next alert or app
refresh; a read update does not send a separate silent push. Mac Catalyst has
no APNs badge integration; its inbox uses the same shared unread counts.
