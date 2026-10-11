# CLAUDE.md

`README.md` is the full description of the server: every flag, key,
screen and data file. Read the section you need there; it is not
repeated here.

## Build and test

```
go build -o adhd-dash .
go vet ./...
go test ./...
```

Both `go vet` and `go test` must pass clean before a change is done.

## Running it

`./fake.sh` builds the server and runs it on port 3299 with a fake agenda
(`agenda.json`, regenerated each run) and the hardware off
(`-flag=false -streamdeck=false`). Never run the server with `-flag` or
`-streamdeck` on: they drive the real Luxafor flag and Stream Deck on
this machine.

The data files default to the home directory (`~/adhd-dash.db`,
`~/adhd-dash-users.json`, `~/adhd-dash-audit.log`, `~/adhd-dash-certs`,
...), and `fake.sh` does not override them. When running the server to
try something out, point `-db`, `-users-file`, `-audit-log`,
`-checklist-json`, `-tls-domain-file`, `-tls-dir`, `-web3270-file` and
`-backup-dir` at a scratch directory, and give `-http-port` a free port
or `0`, so a live instance's files and ports are left alone. The users
file and checklist JSON are also moved from their old dotted names
(`~/.adhd-dash-users.json`) when left at their defaults.

## Every change

- **Update `README.md`.** Each feature, flag, key, screen or file change
  is documented there in the same commit, in the README's own style:
  plain sentences saying what the user sees and what it is for.
- **Update `install-ubuntu.sh`** when a change adds a flag the installer
  should set, a file or directory the server writes, a port, or a
  device: it writes `/etc/default/adhd-dash`, a sandboxed systemd unit
  and udev rules, and each may need to match.
- **Add tests** in the package's `_test.go` file alongside the change.

## Layout

- `main.go`: flags, startup, legacy file migration.
- `internal/session`: the TN3270 screens, one file per screen
  (`dashboard.go`, `checklist.go`, `recurring.go`, ...), each with its
  own `_test.go`.
- A dashboard command is added to `commands` in
  `internal/session/commands.go` (name, aliases, PF key, help text) and
  handled in the command `switch` in `internal/session/session.go`.
- The other `internal/` packages are the backends: `agenda` and `google`
  (calendars), `tasks` (Trello), `busy`, `luxafor`, `streamdeck`, `db`
  (SQLite), `checklist`, `recurring`, `timers`, `users`, `certs`
  (TLS/ACME), `web`, `web3270`, `backup`, `audit`, `bigtext`,
  `weather` (Open-Meteo forecasts).

## Style

- Comments are full sentences saying what a thing is for and why, as in
  `internal/session/termtest.go`. Match that.
- Every file that takes comments starts with the copyright header, then a
  blank line (in Go, above any package comment or `//go:build` line):

  ```go
  // Copyright (C) 2026 Joelle Maslak
  // All Rights Reserved - See License
  ```

  Shell scripts put it, with `#`, after the `#!` and `set` lines.
- Commit subjects are short and imperative, with no prefix:
  "Add recurring tasks", "Support TLS natively".
