# Mail-server events and Firebase push

## Architecture and prerequisites

Mail server delivers into INBOX → administrator event hook → signed HTTPS request → companion queue → FCM data wakeup → generated Android app fetches mail directly using its device-stored session. No employee password is retained in companion storage or supplied with a push registration. Normal sign-in still verifies the password transiently over TLS. Google receives a device token and opaque mailbox cursor, not the sender, subject or body.

This integration requires a mail-server/provider hook that supplies the authenticated mailbox identity and actual INBOX IMAP UIDVALIDITY and delivered UID. SMTP acceptance/queue IDs and Message-ID are **not** IMAP UIDs. Emit only after mailbox delivery is committed. Configure the hook through your server's supported mechanisms; never modify Mailcow's internal database. There is no universal auto-installed Mailcow/Dovecot/provider hook in this repository. The supplied signing helper is the protocol adapter; your administrator must connect it to your supported delivery-event source. If the provider exposes no suitable hook, use device IMAP IDLE rather than inventing cursor values or storing employee passwords.

For Dovecot 2.4 with LMTP delivery, an optional [queued delivery adapter](../integrations/dovecot24/README.md) is provided. It requires administrator installation and version-specific validation; other servers still need their own supported event source.

## Firebase setup: two different files

1. In Firebase Console create/select a project and register an Android app with the generated organization's exact package ID, not Maker's ID.
2. Download the **public Android client** `google-services.json`. In Maker select Firebase and upload it; the package ID is validated.
3. In the same project's service-account settings obtain a private server service-account key with FCM sending access. Enable the FCM HTTP v1 API. Put this file only on the VPS as `secrets/firebase-private.json`.
4. Set `COMPANION_FCM_CREDENTIALS=/run/secrets/firebase-private.json`. Never upload this private file in Maker, put it in the APK, Git, or a public artifact.
5. Generate an independent event-signing secret (`openssl rand -hex 32`) and securely save it as `secrets/mail-events-secret`. Supply the same secret to your mail-server hook via a protected file. Set `COMPANION_EVENT_SECRET_FILE=/run/secrets/mail-events-secret`.
6. Apply the UID/GID 10001 read permissions from [Deployment](DEPLOYMENT.md), restart, rebuild the organization app with the same signing identity, and enable notifications/push. Both files are needed for the companion to advertise push.

The server uses OAuth access tokens with FCM HTTP v1. See [Firebase server authorization](https://firebase.google.com/docs/cloud-messaging/send/v1-api) and [Android priority behavior](https://firebase.google.com/docs/cloud-messaging/android-message-priority). High-priority wakeups are for user-visible new-mail notifications; Android, connectivity and FCM policy still affect delivery. This is not a guarantee of instant delivery.

## Signed event contract

POST `/api/integrations/mail-events`, JSON up to 8 KiB:

```json
{"id":"unique-stable-event-id-123","mailbox":"employee@example.com","validity":123456,"lastUid":987}
```

`id` is 16–128 characters and stable across retries; `validity` and `lastUid` are nonzero uint32 values from the actual INBOX. Mailbox must match a configured allowed domain. Include:

- `X-Companion-Timestamp`: current Unix seconds, within five minutes of server time.
- `X-Companion-Signature`: lowercase hexadecimal HMAC-SHA256 of `timestamp + "." + exact request body bytes`, signed with the shared secret after trimming surrounding whitespace from the secret file.

A valid event returns 202. Repeated IDs with identical content are accepted without requeuing for 24 hours; different content under the same ID returns 409. A producer must stop retries within that retention window. Invalid signature/timestamp returns 401, invalid event 400, unavailable configuration/queue 503. Retry temporary failures with bounded exponential backoff and the same ID/body, but a fresh timestamp/signature. Do not block SMTP delivery while waiting on the hook: use your mail server's durable event mechanism.

Use the included Python 3 helper from your hook:

```sh
export COMPANION_EVENT_URL=https://mail-features.example.com/api/integrations/mail-events
export COMPANION_EVENT_SECRET_FILE=/secure/path/mail-events-secret
python3 scripts/send-mail-event.py < delivered-inbox-event.json
```

The JSON above is illustrative, not a valid real-mail test until replaced with actual mailbox UIDs. The helper refuses HTTP/redirects and never needs a mailbox password. Do not pass secret contents in command-line arguments. Keep the clock synchronized, restrict the event endpoint to the producer's IP at the proxy where possible, and set proxy request/rate limits. A compromised signing secret can generate notification events for allowed domains; rotate it on both sides.

## Device registration and queue

Bearer-authenticated `POST /api/native/push` accepts `{id,token,consent:true,projectId}` only; password fields are rejected. Tokens are bound to the authenticated mailbox, up to 10 devices/mailbox and 1,000 registrations total, with a seven-day lease renewed by active clients. `DELETE /api/native/push?id=...` revokes only that mailbox/device and cancels its queued wakeups. Offline remote revocation is best-effort; local notifications stop immediately and leases expire.

Events create jobs for current registrations only. Worker sends at most four per tick (five seconds), retries up to five attempts within a five-minute expiry and deletes expired jobs. A crash after FCM accepts a request can cause a repeated wakeup; the app's UID/generation handling prevents treating old mail as a new arrival. Event IDs are retained 24 hours with a 10,000-event defensive limit; size capacity before broad rollout. No notification body or mailbox credential enters the event queue.

Capabilities use `adapter: companion-v1` and `pushVersion: 2`. Older password-polling push versions are not enabled by the client. Direct IMAP remains the message source; a companion URL must not replace the IMAP endpoint.

## Deployment acceptance checklist

Use a dedicated mailbox and a real newly delivered test message. Confirm mailbox delivery creates one signed event, duplicates do not create more queue jobs, wrong signatures fail, and only that mailbox's registered phone receives the notification while backgrounded. Check Reply/Mark as read, revoke push, switch accounts, renew registration and test expired tokens/network outages. Generic Firebase Console notifications do not test this pipeline. Check private service logs without logging request bodies, tokens or authentication headers.

Source-level and local fixture tests do not replace this deployment-specific test. If no events arrive, fix the mail-server hook first; do not add employee password storage as a fallback.
