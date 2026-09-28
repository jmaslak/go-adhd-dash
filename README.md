# go-adhd-dash

`adhd-dash` serves a dashboard to TN3270 (mainframe) terminal clients. One
screen shows:

- the **busy indicator**: whether the light is red, green or off, and how
  long until the next meeting, from
  [go-busy-indicator](../go-busy-indicator)'s WebSocket status feed;
- the **agenda**: every meeting under way or starting in the next 24 hours,
  from Google Calendar, with the one in progress marked `NOW` and a
  countdown to the next. All-day events and out-of-office events (by
  title: "out of office" or the word "OOO") are left out. If they do not
  all fit alongside the tasks, the first page gives them the room and the
  tasks move to later pages;
- the **tasks**: the open tasks from [go-task](../go-task), filtered the way
  `task list` filters them (ignored tags, display frequency, maturity date).

The screen redraws itself every `-refresh`, so it can be left up on a spare
terminal. `PF5` turns that off and on again for the session; the title row
shows `AUTO-REFRESH` while it is on.

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
| `-busy-file` | (none) | read the busy status from a JSON file instead of a feed |
| `-busy-control` | `localhost:3333` | busy indicator UDP control port, `host:port` (its `--port`), for `PF1` / `PF2`; empty to turn them off |
| `-calendar` | (none) | comma-separated Google calendars for the agenda |
| `-calendar-alias` | (none) | comma-separated short names for the `-calendar` calendars, in the same order; each event is shown as `[name] title` |
| `-agenda-file` | (none) | read the agenda from a JSON file instead of Google |
| `-agenda-refresh` | `5m` | how often the calendar is read |

Leaving `-busy-url` or `-calendar` empty leaves that section showing "not
configured" rather than failing.

## Keys

- `PF1` / `PF2`: set the busy indicator to busy / turn it off until the
  next meeting, as `busy b` / `busy o` do (not offered with `-busy-control=`)
- `PF5`: turn auto-refresh off / on (on at connect)
- `PF7` / `PF8`: previous / next page of tasks
- `PF9`: calendar
- `Enter`: redraw now
- `PF3`: disconnect

On the calendar, a month is shown with the selected day's events beside it. Move the cursor onto a day and press
`Enter` to select it.

- `PF7` / `PF8`: previous / next month
- `PF4`: back to today
- `PF5`: turn auto-refresh off / on
- `PF3`: back to the dashboard

The calendar reads each month from Google as it is shown and reuses it for
`-agenda-refresh`, separately from the shared today-and-tomorrow agenda.

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

`-busy-file` takes the feed's message as a JSON file instead, for trying the
dashboard without an indicator:

```json
{"status": "red", "minutes-to-next": 20}
```

`status` is `red` (busy), `green` (available) or `off` (not busy); leave out
`minutes-to-next` for no more meetings today. The file is re-read at every
redraw, and its modification time counts as when the status arrived, so the
countdown runs down from when the file was last saved (`touch` it to
restart it).

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
  "end": "2026-09-27T09:15:00-06:00", "all_day": false, "calendar": "work"}]
```

`calendar` is optional; it is shown in brackets as `-calendar-alias` names
are.

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
