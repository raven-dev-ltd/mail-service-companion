# Security policy

Report suspected vulnerabilities privately to **support@raven-dev.tech** with the affected commit, deployment type, impact and safe reproduction steps. Never include live mailbox passwords, service-account private keys, SMTP credentials, signing secrets or unredacted customer data. Do not post exploitable details in a public issue before coordination.

If a credential was exposed, revoke/rotate it immediately; deleting a Git file does not remove copies or Git history. Preserve relevant evidence privately. Security reports are reviewed on a best-effort basis; this project does not promise a response deadline, bounty or support for every historical release.

Deploy behind HTTPS, restrict allowed mailbox domains and origins, keep the SMTP relay narrowly scoped, authenticate signed mail events, and protect the SQLite volume and backups. The service stores queued content, contacts and device/session metadata: it is not a zero-data proxy. Review the [deployment](docs/DEPLOYMENT.md) and [integration](docs/INTEGRATION.md) guides before production use. Automated checks are not a security certification.
