# Mail Service Companion

Optional, organization-hosted mail features in one Go executable, one SQLite data volume and one in-process delivery worker. No Redis, external database server or access to the mail server's internal database is required. This is a small stateful feature service, not a stateless proxy.

This is an independent source-available project maintained by RAVEN Developers Ltd. It can be deployed without the Mail App Maker source tree; Maker includes it as an optional Git submodule. Organizations host and operate their own service. The companion does not host a mail server or replace IMAP/SMTP.

| Feature | Implementation |
| --- | --- |
| Contacts, lists, photos, templates | Private per-mailbox SQLite records |
| Labels | Companion definitions; IMAP keywords hold message assignments |
| Scheduled sending and basic campaigns | Durable queue through an administrator-configured TLS SMTP relay |
| Android push | Signed mail-server events → companion → FCM → direct IMAP fetch on phone |
| Calendar, server snooze, tracking analytics | Not implemented |

Employee passwords are used transiently for TLS IMAP sign-in verification and **never persisted by the companion**. Background sending uses an administrator relay identity or an explicitly IP-allowlisted relay. Push uses signed server events, not password-based mailbox polling. Queued message contents, recipients, contacts, device tokens and session metadata are still stored: this is not a zero-data service.

## Start here

### Standalone installation

Install Git and Docker Engine with the Compose plugin on your organization-controlled server. Then:

```sh
git clone https://github.com/raven-dev-ltd/mail-service-companion.git
cd mail-service-companion
cp .env.example .env
chmod 600 .env
```

Edit `.env` before starting: configure your fixed TLS IMAP endpoint and exact allowed mailbox domains. Select an unused `COMPANION_HTTP_PORT`, create `secrets/`, and configure an HTTPS reverse proxy for your chosen subdomain. Do not expose the container's HTTP port publicly. Optional scheduling/campaigns need an authorized administrator SMTP relay; FCM needs a server-only Firebase service-account key and a signed mail-server event source. `google-services.json` is the Android client file, not that server key.

```sh
mkdir -p secrets
docker compose up --build -d
# Substitute COMPANION_HTTP_PORT if you changed its default of 8080.
curl --fail http://127.0.0.1:8080/healthz
```

Follow [Deployment and configuration](docs/DEPLOYMENT.md) for DNS, HTTPS, secrets permissions, backups and upgrades, and [Mail-server events and Firebase](docs/INTEGRATION.md) for push. In Maker, select the lightweight companion and enter its HTTPS base URL. Employees authenticate with their own mailbox; never embed administrator secrets in an APK.

### Routing and storage

The companion creates `feature-routing.json` in its data directory during configuration and each startup. It checks the fixed IMAP endpoint using verified TLS, records advertised capabilities and the source of each feature, and uses that policy to dispatch companion requests. The authenticated `/api/native/capabilities` response includes the same routing manifest for the mobile app to cache. Mail, flags, drafts, folders and Sieve rules remain mail-server operations; contacts, lists, template definitions and optional relay/event features use companion handlers. Labels combine companion definitions with IMAP keywords. See [Routing and discovery](docs/DEPLOYMENT.md#routing-and-discovery).

Read [Deployment and configuration](docs/DEPLOYMENT.md), then [Mail-server events and Firebase](docs/INTEGRATION.md) if push is needed. Copy `.env.example` to `.env`, configure your server, and run `docker compose up --build -d`. Schema creation and upgrades are automatic. Never run the schema against your mail server's database.

```text
cmd/mail-companion/    executable entry point
internal/companion/   HTTP API, storage schema, relay, event worker and tests
scripts/              mail-event signing/submission helper
integrations/         optional server-specific adapters (Dovecot 2.4)
docs/                 operator deployment and integration guides
secrets/              ignored server-only files; read-only container mount
compose.yml           standalone service and persistent volume
Dockerfile            multi-stage, non-root runtime image
.env.example          all service and deployment settings
```

## Development

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/mail-companion ./cmd/mail-companion
python3 -m unittest discover -s scripts -p 'test_*.py'
```

Use the Go version specified in `go.mod` or a compatible newer toolchain. For a native binary, export environment variables first; the binary does not parse `.env` automatically. Run one instance per data directory. Tests use temporary databases and local TLS fixtures, never real mail delivery.

The container and CI use Go 1.27.1. SQLite uses the pure-Go driver; release binaries can be built with `CGO_ENABLED=0`. Race tests need a working C compiler. See [Contributing](CONTRIBUTING.md) for pull requests and [Security](SECURITY.md) for private vulnerability reporting. CI tests and build checks do not certify a production deployment.

## Upgrade notice

Schema 6 removes legacy employee-credential tables and old push registrations. Existing queued/in-flight sends are cancelled and require explicit rescheduling after relay setup. Contacts, templates, labels, campaign records and delivery history are preserved. Old clients must be rebuilt to use the administrator-managed delivery/push contract. Back up first: old backups may still contain encrypted employee credentials and must follow your secure retention policy. Do not downgrade the binary against the migrated database.

## License

Copyright (c) 2026 RAVEN Developers Ltd. This revision uses the full [PolyForm Noncommercial 1.0.0 license](LICENSE): personal and permitted noncommercial use are free; commercial use requires a separate paid written license. Contact **sales@raven-dev.tech**. Keep [NOTICE](NOTICE), including the Required Notice, with redistributed copies. This restriction means the project is source-available, not OSI open source.

Previously published MIT revisions retain their original grants; this change does not revoke earlier permissions. Third-party dependencies keep their own licenses. No trademark rights, warranty or hosted service are included. Commercial licensing is not a per-mailbox database quota or an automatic recurring subscription; its scope/price must be agreed separately.
