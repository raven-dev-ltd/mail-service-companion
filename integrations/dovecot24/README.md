# Dovecot 2.4 delivery adapter

This optional adapter connects committed LMTP INBOX deliveries to the companion's signed event API. It targets Dovecot 2.4 with the `mail_lua`, `notify`, `push_notification` and `push_notification_lua` plugins installed; it is not a universal Mailcow/provider installer. Configuration follows the [Dovecot Lua notification interface](https://doc.dovecot.org/2.4.1/core/plugins/push_notification_lua.html). Validate your installed version before enabling it.

The Lua hook writes mailbox identity and actual UIDVALIDITY/UID into a private local spool, never a subject, body or password. A Python systemd timer forwards events over HTTPS every five seconds after the previous run finishes. Network outages do not block message delivery. Only successful transactions produce events; multiple messages in a transaction are coalesced to the highest cursor, and the Android client synchronizes intervening messages. This is best-effort notification delivery, not a replacement for IMAP synchronization: disk errors can lose a wakeup and events expire after one hour. Monitor free disk space and adapter failures.

## Installation

1. Configure the companion's Firebase private key and shared event secret as described in [Integration](../../docs/INTEGRATION.md). Both remain server-side. Back up your existing mail configuration before changing it.
2. For Docker Mailserver, locate its persistent `/tmp/docker-mailserver` bind mount. Copy `mail-events.lua` there as `companion-mail-events.lua`. Create `companion-events/` alongside it, owned by the mailbox process UID (commonly 5000), mode 0700. Do not expose this directory over HTTP. Files contain mailbox addresses temporarily.
3. Merge `dovecot.cf` into the existing persistent custom Dovecot configuration; do not replace unrelated settings. Docker Mailserver copies this file to `/etc/dovecot/local.conf` at startup. For a live reload, also apply the merged file there, run `doveconf -n` privately and reload Dovecot only after validation succeeds. Never paste configuration dumps containing secrets into a ticket. Keep rollback copies.
4. Install `scripts/send-mail-event.py` and `scripts/drain-mail-events.py` together into `/opt/mail-companion-events/`, root-owned and not writable by the mail process. Python 3 uses only its standard library. Install the supplied service and timer in `/etc/systemd/system/`. Change the service's `User`, `Group` and `ReadWritePaths` spool path for your host. It runs without Linux capabilities, as the mailbox UID. Give this UID a read-only copy of the shared event secret at `/etc/mail-companion-events/secret` (directory 0750, file 0400, owned by that UID). Do not grant it access to the Firebase private key. Rotate both copies of the event secret together.
The service's `BindPaths` source must point to your actual host spool; its destination stays `/var/spool/mail-companion-events`. If the container's mailbox UID does not exist on the host, create a matching non-login service account first, after checking for UID/GID conflicts. Reuse an existing matching account rather than changing its UID. The example UID/GID 5000 is not universal.

5. Create root-owned `/etc/mail-companion-events.env`, mode 0600, with these **paths/settings**, not the secret value:

```dotenv
COMPANION_EVENT_URL=https://mail-features.example.com/api/integrations/mail-events
COMPANION_EVENT_SECRET_FILE=/etc/mail-companion-events/secret
COMPANION_EVENT_SPOOL=/var/spool/mail-companion-events
COMPANION_EVENT_LOCK=/run/mail-companion-events/adapter.lock
```

6. Validate units with `systemd-analyze verify`, then run `systemctl daemon-reload` and `systemctl enable --now companion-mail-events.timer`. Check `journalctl -u companion-mail-events.service` for aggregate counts, not message data. The one-shot service normally becomes inactive after a successful run; the timer must remain active. Each run inspects at most 500 entries, sends for at most roughly one minute, uses one lock, and retains failed requests for later attempts. Provision a dedicated quota/filesystem and alert on backlog for large deployments; this small adapter is not a high-volume queue.
7. Rebuild the generated Android app with the matching project's public `google-services.json`, enable Firebase notifications, and send a labelled test message to your own mailbox. Verify one real delivery event and an actual background notification. An OAuth test or HTTP 202 alone does not prove end-to-end delivery. On failure, disable the timer and restore the previous Dovecot local configuration, validate, and reload; ordinary mail remains available.

## Tests

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s companion/scripts -p 'test_*.py'
lua companion/integrations/dovecot24/test-mail-events.lua companion/integrations/dovecot24/mail-events.lua
```

Run these commands from the repository root. Tests do not send mail or use private keys. Mail-server-specific deployment validation is still required.
