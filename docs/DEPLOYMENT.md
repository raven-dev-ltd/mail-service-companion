# Deploy Mail Companion

## 1. Prerequisites and DNS

Use an organization-controlled Linux server with Docker Compose, outbound TLS IMAP, and a domain you control. Add an A record such as `mail-features.example.com` pointing at the VPS IPv4 address. Add AAAA only when IPv6 works. Any custom subdomain works. Leave existing MX records unchanged.

Allow inbound 80/443 for your HTTPS proxy; do not expose the companion port publicly. On an existing Mailcow/web host, reuse its proxy instead of competing for ports. Clone the standalone repository into a private server directory (or use the initialized `companion/` submodule inside Mail App Maker):

```sh
git clone https://github.com/raven-dev-ltd/mail-service-companion.git
cd mail-service-companion
cp .env.example .env
chmod 600 .env
```

Edit `.env`: set the fixed IMAP hostname, TLS port/security and allowed mailbox domains. Use 993/implicit-tls or 143/starttls. Allowed domains are exact, comma-separated values; no wildcard. Keep the default Android origins unless you deliberately add an exact HTTPS browser origin. Employee passwords are not configuration values.

## 2. Start the service and HTTPS

### Routing and discovery

Configuration runs automatically on each service start. To generate and inspect the routing configuration before starting the HTTP service or workers, use:

```sh
docker compose build companion
docker compose run --rm --no-deps companion --configure
docker compose up -d companion
```

For a native deployment, run `./mail-companion --configure` with the same environment as the service. The generated `COMPANION_DATA_DIR/feature-routing.json` lives in the persistent data volume (`/data/feature-routing.json` inside Docker). It contains source, protocol, enabled state, execution support, installation-check results and any required mailbox verification. It contains no credentials. The service atomically regenerates it from current environment settings on restart, then reads that exact file for runtime routing. Configure settings in `.env` and recreate the container with `docker compose up -d --force-recreate companion`; manual edits to the generated file are replaced on startup.

Use `COMPANION_DISABLED_FEATURES=templates,campaigns` to disable new API access to selected companion resources. Existing durable delivery jobs are not cancelled by disabling an API; cancel them before changing policy if required. The companion continues authenticating and isolating every request by mailbox. It dispatches companion resources through the routing wrapper and returns the manifest in authenticated `GET /api/native/capabilities`. The app caches the manifest per mailbox/configuration and prefers a usable native protocol adapter over a companion fallback. Older clients still receive the existing v1 feature fields.

Installation discovery verifies TLS and unauthenticated IMAP capabilities without employee credentials. It cannot establish per-mailbox permissions, writable keywords, authenticated-only extensions, SMTP send-as permission, CardDAV collections or Sieve access. The app checks those after sign-in, so an installation probe failure never logs out a working mailbox. Unsupported client adapters remain unavailable even if their server protocol is detected. The wrapper does not translate arbitrary provider APIs or proxy IMAP through HTTP: ordinary mail goes directly from app to mail server, and only implemented companion features go to companion storage/executors. No mail-server database or filesystem is accessed.

Choose an unused `COMPANION_HTTP_PORT` (default 8080). Container port stays 8080. Create the secrets directory even if optional integrations are disabled, then start:

```sh
mkdir -p secrets
docker compose up --build -d
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=50 companion
```

Use your chosen port in the health check and proxy. For Caddy running on the host:

```caddyfile
mail-features.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Validate/reload your proxy and test `https://mail-features.example.com/healthz`; expect `{"ok":true}`. The subdomain belongs in DNS, the proxy and Maker; the companion has no public-hostname setting. A separately managed proxy does not automatically read this project's `.env`. A containerized proxy must use a shared private network/upstream, not its own loopback. HTTPS must use a publicly trusted certificate. Health checks test SQLite readiness, not SMTP, IMAP credentials or actual push delivery.

## 3. Optional administrator SMTP relay

Without a relay, contacts/templates/labels work but scheduling/campaign execution is unavailable. Choose one mode:

- `service`: a dedicated administrator-managed SMTP service identity authorized to submit as your allowed employee domain(s).
- `ip-allowlist`: the mail administrator allows only this companion's fixed source IP to relay, with sender-domain restrictions. No SMTP AUTH is sent, but verified TLS is still mandatory.
- `disabled`: safe default; never implicitly falls back to an unauthenticated relay.

Set `COMPANION_SMTP_HOST`, port/security, and mode. In service mode also set username and `COMPANION_SMTP_PASSWORD_FILE=/run/secrets/smtp-password`; put the service password in `secrets/smtp-password` on the VPS. This is an administrator credential, never an employee password. Use a narrowly scoped relay identity, not the mail-server superadmin account. Configure sender restrictions, recipient/rate limits, DKIM/SPF alignment and abuse monitoring on your relay. Never enable an open relay or disable certificate verification. SMTP configurations differ by provider; the companion cannot create these permissions automatically.

The container runs as UID/GID 10001. Protect files while allowing that identity to read them:

```sh
sudo chown root:10001 secrets
sudo chmod 750 secrets
sudo chown root:10001 secrets/smtp-password
sudo chmod 640 secrets/smtp-password
docker compose up -d
```

Apply the same ownership/mode to optional Firebase/event files. Use only the files you enabled; do not create fake private keys. Restart after rotating secret files. The secret directory is read-only in the container and excluded from image builds/Git. Never share `.env`, real secrets, private backups or service logs containing custom proxy request bodies.

Configuring the administrator SMTP relay enables scheduled sending and campaigns for authenticated organization users by default; there is no employee consent checkbox or stored employee password. The authenticated session determines the sender; another sender address cannot be supplied. Users cancel pending messages from Scheduled or cancel campaigns before submission. Signing out does not cancel queued jobs. The legacy delivery-consent URL reports administrator-managed status; it cannot disable the relay. Updating does not restart previously cancelled or failed jobs.

Successful SMTP authentication alone does not prove send-as permission. Postfix may reject an employee sender at `RCPT TO` with `553 ... not owned by user`. An administrator must add the relay identity to the permitted sender owners for the approved organization domain while preserving existing owner mappings and anti-spoofing checks. Do not disable `reject_authenticated_sender_login_mismatch` globally. Postfix's [sender-login maps](https://www.postfix.org/postconf.5.html#smtpd_sender_login_maps) control this permission. Verify an employee sender is accepted and an outside-domain sender is still rejected; use `RSET` before `DATA` for a non-delivery test. A separate, consented real-message test is needed to verify scheduling and final delivery.

## 4. Connect the generated app

In Maker, choose **Included companion** under Mail server → Advanced options and enter the HTTPS base URL without `/api`. Keep IMAP/SMTP pointing at the actual mail server. Test a mailbox, save and rebuild using the same organization app ID/signing key and a higher build code. Install as an update; do not uninstall to upgrade. Employees use normal mailbox sign-in. The companion verifies it transiently against your fixed IMAP endpoint; OAuth-only mailbox authentication is not implemented.

For FCM, follow [Integration](INTEGRATION.md). Without mail-server events, keep IMAP IDLE/device checks instead. A capability flag means configured, not proof that your external server hook is working.

## 5. Data, limits and delivery semantics

One submission every 10 seconds, one worker, 30-second SMTP deadline. Scheduled messages allow 100 recipients, 256 KiB body and 200-character subject; 1,000 pending jobs per mailbox. Basic campaigns snapshot templates/recipients, personalize name/email fields and track per-recipient submission status; no click/open tracking or automated sequences. Queue attachments and automatic IMAP Sent copies are not implemented. SMTP acceptance does not guarantee recipient inbox delivery.

An ambiguous SMTP failure or restart during submission is marked failed/uncertain and is never automatically resent. Check delivery before rescheduling. Server clock must be synchronized. Timings are best-effort around the configured date and queue load, not hard real-time.

Contacts (10,000/mailbox), lists (500), CSV imports (5,000 rows), templates (500) and campaigns (500) have defensive limits. Companion stores content and metadata needed for these features; it does not store employee mailbox passwords. Message bodies/history remain until removed by supported application operations or operator retention procedures. Set a backup/retention policy and disk monitoring; there is no automatic blanket content deletion policy. Do not promise unlimited storage.

## 6. Updates, migration and recovery

Back up the data volume consistently with the service stopped, or use SQLite's supported online backup. Keep the image/version and private configuration with the backup. Test restoration on an isolated instance with relay/push disabled. Never use `docker compose down -v` for an upgrade.

Schema 6 automatically deletes old encrypted employee credential tables and old push registrations. Pending/in-flight legacy sends are cancelled, not silently delivered through the new relay. Reschedule explicitly after the administrator configures the relay; rebuild clients and re-enable push. Existing contacts/templates/labels/history remain. SQLite secure deletion is enabled for active migration, but backups, filesystem snapshots and old WAL copies may retain prior secrets; manage these under your organization's retention policy. Remove the obsolete `COMPANION_CREDENTIAL_KEY` from environment/secret management after migration and backup policy review.

```sh
docker compose up --build -d
curl --fail https://mail-features.example.com/healthz
docker compose logs --tail=50 companion
```

Do not run an older binary against schema 6. Rollback requires the matching pre-upgrade database snapshot, with consequences for changes made since it. Run only one instance per volume; horizontal/shared-volume operation is unsupported.

## Troubleshooting and acceptance tests

- 404 health: wrong proxy URL/path. 401 protected API without bearer: expected. Login rejection: check IMAP endpoint/TLS and mailbox authentication, not SQLite credentials.
- Scheduling unavailable: check relay mode/configuration, then refresh Server features. Relay rejection: verify service-account send-as/source-IP permissions on the mail server.
- Before rollout, test isolated contacts, templates, future scheduling with phone offline, cancellation, disabled relay, Unicode mail and campaign recipient isolation. Use dedicated test mailboxes and explicit recipient permission.
- Push troubleshooting and signed event test: see [Integration](INTEGRATION.md). Live FCM and provider-hook qualification are required for each deployment.
