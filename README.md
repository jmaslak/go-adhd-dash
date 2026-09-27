# go-adhd-dash

`adhd-dash` serves a dashboard to TN3270 (mainframe) terminal clients. One
screen shows:

- the **busy indicator**: whether the light is red, green or off, and how
  long until the next meeting, from
  [go-busy-indicator](../go-busy-indicator)'s WebSocket status feed;
- the **agenda**: meetings that have not yet ended, today and tomorrow,
  from Google Calendar, with the one in progress marked `NOW` and a
  countdown to the next;
- the **tasks**: the open tasks from [go-task](../go-task), filtered the way
  `task list` filters them (ignored tags, display frequency, maturity date).

The screen redraws itself every `-refresh`, so it can be left up on a spare
terminal.

## Usage

```
go build -o adhd-dash .
./adhd-dash -busy-url ws://localhost:3334/feed -calendar you@example.com
```

Then connect a TN3270 emulator (c3270, x3270, s3270, ...) to `host:port`.
Any screen size the client reports is used: Model 2's 24x80 up through
Model 5's 27x132 or larger.

### Flags

| Flag | Default | Description |
|---|---|---|
| `-host` | `localhost` | address to listen on |
| `-port` | `3270` | TCP port to listen on |
| `-refresh` | `10s` | how often an idle screen is redrawn |
| `-tasks-dir` | `$TASKDIR`, then `~/.task` | the task program's directory |
| `-busy-url` | (none) | busy indicator feed, `ws://host:port/feed` |
| `-calendar` | (none) | comma-separated Google calendars for the agenda |
| `-agenda-file` | (none) | read the agenda from a JSON file instead of Google |
| `-agenda-refresh` | `5m` | how often the calendar is read |

Leaving `-busy-url` or `-calendar` empty leaves that section showing "not
configured" rather than failing.

## Keys

- `PF7` / `PF8`: previous / next page of tasks
- `Enter`: redraw now
- `PF3`: disconnect

## Where the data comes from

### Busy indicator

Run `busy-indicator` with `--ws-port=<port>` and point `-busy-url` at
`ws://<host>:<port>/feed`. The busy indicator's feed listens on localhost
unless it is also given `--ws-host`, e.g. `--ws-host=0.0.0.0`, so
`adhd-dash` on another machine needs that (the feed is unauthenticated,
so firewall it) or a tunnel.

The indicator publishes on its own `--interval` (60 seconds by default), so
the countdown is adjusted locally between messages. A dropped feed is
redialed every 5 seconds; while it is down the screen says so and shows the
last state received.

### Agenda

Calendars are read with the same credentials as `busy-indicator`:
`~/.gcalcli_oauth`, falling back to `~/.busy-indicator-oauth` (written by
`busy-indicator --login`). No separate authorization is needed. The fetch
happens once per `-agenda-refresh` and is shared by every connected session.
If a fetch fails, the last agenda stays up, marked stale.

Cancelled events and events you declined are left out, as the busy indicator
does.

The OAuth and Calendar API clients are go-busy-indicator's `gauth` and
`gcal` packages, imported rather than copied, so the dashboard and the busy
indicator share one implementation.

`-agenda-file` takes a JSON list, for trying the dashboard without Google:

```json
[{"summary": "Standup", "start": "2026-09-27T09:00:00-06:00",
  "end": "2026-09-27T09:15:00-06:00", "all_day": false}]
```

### Tasks

Task files are read directly on every redraw, without the task program's
directory lock; the task program replaces files by atomic rename, so a read
never sees a half-written task. `ignore-tags` is read from `~/.task.yaml`
and `~/.task.secret.yaml` the same way the task program reads it.

## Layout

- `main.go`: flags, background watchers, and the accept loop.
- `internal/session`: per-connection TN3270 session and dashboard rendering.
- `internal/busy`: busy indicator WebSocket feed client.
- `internal/agenda`: shared, periodically refreshed calendar cache.
- `internal/tasks`: task file reader and `task list` filtering.

Telnet/TN3270E negotiation is
[`github.com/jmaslak/go-3270e`](../go-3270e).
