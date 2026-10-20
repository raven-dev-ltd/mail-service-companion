# Contributing

Fork this repository, create a focused branch and open a pull request against `main`. Describe the behavior change and include regression tests. Keep unrelated formatting changes separate.

```sh
gofmt -w cmd internal
go vet ./...
go test -race ./...
python3 -m unittest discover -s scripts -p 'test_*.py'
CGO_ENABLED=0 go build -trimpath -o bin/mail-companion ./cmd/mail-companion
```

Preserve mailbox isolation, verified TLS, bounded inputs and compatible API contracts. Keep employee passwords out of persistent storage. Do not add direct access to mail-server databases or silently replace server scheduling with device execution. Database migrations must preserve supported data and document any irreversible changes. Tests must use temporary data and local fixtures, not production mailboxes or delivery endpoints.

Never commit `.env`, service-account JSON, SMTP passwords, webhook secrets, databases or operational logs. Report suspected vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in a public issue. Contributions are submitted under [GNU GPLv3 only](LICENSE), without a commercial-use restriction. Contributors retain their copyright; submitting a pull request does not assign ownership to RAVEN Developers Ltd. Retain existing attribution and third-party notices.

When working inside Mail App Maker's `companion/` submodule, commit and push here first. Then return to the parent repository and commit its updated submodule reference. A parent commit does not publish child commits.
