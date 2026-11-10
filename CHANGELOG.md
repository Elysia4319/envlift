# Changelog

## 0.1.0-alpha - 2026-11-10

### Added

- Read-only Windows environment scan: installed software, user/machine PATH,
  language runtimes, and system information.
- Reviewable `envlift.yaml` configuration with `${USER}` / `${HOST}` redaction.
- Restore planning with dry-run by default and explicit `APPLY` confirmation.
- Tiered execution: winget, local installer packages, and manual guidance.
- PATH backup, journal, rollback, and md/json restore reports.
- Local Web GUI for scan, plan, decisions, PATH, and report views.
- Report redaction at build time, including md/json files written to disk.
- Single-binary Windows distribution.

### Known limitations

- Software recognition is about 8% (18/221) with 33 built-in recipes.
- Some `silent_args` entries are incomplete, so local package install may be
  unavailable for those recipes.
- F6 real reboot continuation has not been executed; the state machine and
  idempotent skip path have been verified.
- F8 "winget missing" is simulated, not a real App Installer removal.
- Do not run two envlift instances against the same working directory.
- This is an early alpha; configuration and GUI behavior may change.
