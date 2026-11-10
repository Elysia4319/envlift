# Contributing

Thanks for helping improve envlift.

## Before you start

Read these first:

- [`docs/workflow.md`](docs/workflow.md) — development and verification rules.
- [`docs/decisions.md`](docs/decisions.md) — locked decisions; these win over
  ad-hoc preferences.
- [`docs/PRD.md`](docs/PRD.md) — command, schema, and API specifications.

## Development

Requirements:

- Go 1.27+
- Node.js 20+ for the JavaScript syntax check

Use test-driven development where practical. Keep changes focused and make one
commit do one thing.

```powershell
gofmt -w .
go vet ./...
go test ./... -count=1
node --check internal/webui/static/app.js
gofmt -l .
```

`gofmt -l .` must print nothing before submitting a pull request.

## Do not commit local data

Never commit:

- `envlift.yaml`, `envlift-*.yaml`, or `*.raw.yaml`
- `reports/`, `tmp/`, or `installers/`
- journals, screenshots, or logs containing real usernames, hostnames, or
  local absolute paths

Use fake identities such as `olduser` / `OLD-PC` in tests and fixtures.

## Pull requests

- Explain the problem and the smallest change that solves it.
- Include the commands and real output used for verification.
- Add or update tests for behavior changes.
- Do not include unrelated formatting or refactoring.
- Report security issues through [`SECURITY.md`](SECURITY.md), not a public PR.
