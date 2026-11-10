# Security Policy

## Supported versions

Security fixes are currently provided for the `0.1.x` alpha line only.

## Trust model

envlift is not a sandbox. An `envlift.yaml` file can describe commands that
install software and modify user-level or machine-level PATH entries.

- Only run configuration files you trust.
- Review the generated plan before using `restore --apply`.
- Use a VM, test machine, or system snapshot for first-time runs.
- Do not run elevated applies against untrusted configuration.
- `installers/` contains third-party binaries; envlift does not verify their
  origin or integrity.

## Data handling

envlift itself does not collect or upload telemetry; winget and Windows may have their own network and telemetry behavior. Scans and reports can contain
software inventories, paths, usernames, and hostnames.

- Normal reports redact usernames as `${USER}` and hostnames as `${HOST}`.
- `scan --raw` disables redaction. Treat raw output as sensitive local data.
- Do not attach unredacted `envlift.yaml`, `reports/`, journals, or screenshots
  to public issues.

## Reporting a vulnerability

Please use GitHub Security Advisories:

<https://github.com/Elysia4319/envlift/security/advisories/new>

Do not open a public issue containing exploit details, real usernames,
hostnames, tokens, or local absolute paths. Include the affected version,
reproduction steps, and a redacted impact assessment.

## Out of scope

- Vulnerabilities in third-party installers or packages downloaded by the user.
- Damage caused by running an untrusted or modified configuration.
- Scan output or configuration files generated with `--raw` and then
  deliberately published.
