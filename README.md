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
- the **tasks**: the cards on the Trello lists named in `~/.task.yaml`
  (see [Tasks](#tasks)), less those with an ignored tag.

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
| `-busy-url` | (none) | busy indicator feed, `ws://host:port/feed` |
| `-busy-file` | (none) | read the busy status from a JSON file instead of a feed |
| `-busy-control` | `localhost:3333` | busy indicator UDP control port, `host:port` (its `--port`), for `PF1` / `PF2`; empty to turn them off |
| `-calendar` | (none) | comma-separated Google calendars for the agenda |
| `-calendar-alias` | (none) | comma-separated short names for the `-calendar` calendars, in the same order; each event is shown as `[name] title` |
| `-agenda-file` | (none) | read the agenda from a JSON file instead of Google |
| `-agenda-refresh` | `5m` | how often the calendar is read |
| `-checklist-file` | `~/.adhd-dash-checklists.json` | JSON file the checklists are kept in |
| `-users-file` | `~/.adhd-dash-users.json` | JSON file the users are kept in |

Leaving `-busy-url` or `-calendar` empty leaves that section showing "not
configured" rather than failing.

## Keys

- `PF1` / `PF2`: set the busy indicator to busy / turn it off until the
  next meeting, as `busy b` / `busy o` do (not offered with `-busy-control=`)
- `PF4`: calculator
- `PF5`: turn auto-refresh off / on (on at connect)
- `PF7` / `PF8`: previous / next page of tasks
- `PF9`: calendar
- `PF10`: every open task, for archiving
- `PF11`: chat (shown on the help row only when it fits; `help` lists it)
- `Enter`: run the command typed, or with none, open the checklist the
  cursor is on in the task list, or redraw now
- `PF3`: disconnect

The dashboard's task list ends with the starred checklists, each as a task
numbered `-` and tagged `[checklist]` with how many of its items are done, and
counts them in the number of tasks open. Move the cursor onto one and press `Enter` to open
it. The task screen (`PF10`) lists only the tasks.

The dashboard has a command line (`Command ===>`) above the bottom banner.
Type a command and press `Enter`; `help` lists them all, with the PF key that
does the same where there is one, and has a command line of its own. The
commands are `tasks`, `cal`, `calc`, `dbm` (the calculator in dBm mode),
`checklist`, `chat`, `busy`, `green`, `off`, `auto`, `up`, `down`, `refresh`,
`admin`, `help` and `exit`, in either case, with a few aliases (`task`,
`calendar`, `cl`,
`next`, `prev`, `quit`, `logoff`, `?`). The dashboard's timed redraw writes over the screen
without erasing it, so a command half typed survives it.

The chat (`chat` or `PF11`) is shared by every session of this server, each
posting as its LU name. Type a message on the `Message ===>` line and press
`Enter` to send it; the newest messages are at the bottom, above that line,
with your own names in white. Every other session on the chat screen shows
a message the moment it is sent, redrawing over the screen without erasing
it, so a message half typed there survives. The heading lists who is on the
chat screen. `PF7` / `PF8` scroll a page older / newer (a message arriving
meanwhile does not move what is shown), and sending goes back to the
newest. `PF3` goes back to the dashboard. Messages are kept in memory only,
the newest 1000, and are lost when the server stops.

The admin menu (`admin`) lists its options by number; type one on the
`Option ===>` line and press `Enter`, or `PF3` to go back.

- `1`: shut down the server, after a confirmation (`PF4`) saying how many
  other sessions it disconnects. Every session is shown a goodbye screen and
  disconnected, a session in the middle of saving a checklist or archiving
  tasks finishes first, and the server exits (status 0). Sessions that have
  not disconnected within 5 seconds have their connections closed, but the
  server still waits for them to finish anything they are writing. Any
  session can do this; the server listens on `localhost` unless `-host` says
  otherwise.
- `2`: the activity viewer: every session connected to this server, in
  the order they connected, with its LU name, the screen it is on (a
  checklist by name), who it logged in as, when it connected, how long
  since a key was last pressed, and its IP address; this session is marked `*`. Type `X` in the
  `S` column beside sessions and press `PF6` to terminate them: a
  confirmation lists them (and says so if this session is among them), and
  `PF4` there terminates them, `PF3` goes back with the marks kept. Each
  terminated session is shown a goodbye screen and disconnected, finishing
  anything it is in the middle of first; one not gone within 5 seconds has
  its connection closed. Until it is gone its screen shows as
  `Terminating`. `PF7` / `PF8` page, `Enter` refreshes (keeping the marks),
  and `PF3` goes back to the menu. It does not redraw on a timer, which
  would wipe marks typed but not yet sent.
- `3`: clear the chat, after a confirmation (`PF4`) saying how many
  messages it deletes. Every session on the chat screen shows it emptied at
  once.
- `4`: the users (see [Users](#users)).

### Users

The users are kept in `-users-file`, which is made when the server starts
if it does not exist, holding one user, `admin`, with the password `admin`
(the log says so: change it). The file is readable only by its owner and
rewritten by atomic rename; passwords are stored hashed with Argon2id (RFC
9106's second recommended parameters: 64 MiB, 3 passes, 4 lanes, a 16-byte
random salt), in PHC string format, so the parameters can be raised later
without breaking the hashes already stored.

Admin menu option `4` lists the users, a page at a time (`PF7` / `PF8`),
each with a one-character command field and an `Admin` field:

- type `D` beside a user to delete them (a confirmation lists them first,
  and saves nothing until `PF4`; `PF3` goes back with what was typed left
  as it was), or `P` to change their password:
  the bottom rows then ask for it (typed hidden), `Enter` saves it and
  `PF3` cancels;
- type `Y` or `N` under `Admin` to make a user an admin or not, and under
  `Restricted` to restrict them or not (see below);
- on the bottom rows, type a new user's name, `Y` or `N` for `Admin` and
  `Restricted` (blank is `N`), and their password, to add them.

`Enter` (or paging) saves everything typed at once. Names must be unique
(ignoring case) and have no spaces, no admin may be restricted, and at least
one user must stay an admin: a change that would break either is refused whole, with what was
typed left to fix (passwords aside, which are never drawn again). `PF3`
goes back to the admin menu without saving.

### Login

Every session logs in first, on a `LOGIN` screen under an `exec/3270`
banner, with a user name (ignoring case) and password from the users,
except the console. Three wrong tries, or two minutes without logging in,
disconnect it; `PF3` disconnects at once. Each login, and each failed one,
is logged with the address it came from.

The console is a session whose TN3270E client asks for the LU name
`CONSOLE` (with `s3270` or `c3270`, connect to `CONSOLE@127.0.0.1:3270`),
from this machine: `127.0.0.1` or `::1` (or `127.0.0.1` written as
`::ffff:127.0.0.1`). It does not log in. There is only ever one: a new
console boots the one before, which is left saying so. A client anywhere
else asking for `CONSOLE` is refused it (TN3270E `DEVICE-TYPE REJECT`,
reason `INV-NAME`), and logged; it may ask again for another name, or none,
and log in, but most clients give up. Any other name a client asks for is
not honoured: it gets one of the server's own, `AD` and a number. A client
that does not speak TN3270E cannot ask for a name, so cannot be the
console.

A user who logged in and is not an admin cannot open the admin menu. The
console can.

A restricted user goes straight to the calculator on logging in, and has
nothing else: `PF9` switches between it and the dBm calculator, and `PF3`
(shown as `PF3=Log off`) logs them off. A change to a user's restricted
flag takes effect at their next login.

The activity viewer shows who each session logged in as, blank for the
console; the chat still names sessions by LU.

On the calendar, a month is shown with the selected day's events beside it. Move the cursor onto a day and press
`Enter` to select it.

- `PF7` / `PF8`: previous / next month
- `PF4`: back to today
- `PF5`: turn auto-refresh off / on
- `PF3`: back to the dashboard

The calendar reads each month from Google as it is shown and reuses it for
`-agenda-refresh`, separately from the shared today-and-tomorrow agenda.

The task screen lists every open task, whatever its tags. Type `X` in the `S` column beside the tasks to archive,
then press `PF6`; a confirmation lists them, and `PF4` archives them. Marks
are kept while paging.

- `PF4`: add a task
- `PF6`: archive the marked tasks, after confirmation
- `PF7` / `PF8`: previous / next page
- `Enter`: keep the marks typed, archiving nothing
- `PF4` (on the confirmation): archive
- `PF3`: back (from the confirmation, to the list with the marks kept)

`PF4` adds a task: type its title, and the number of the Trello board and
list to put it on, from those in `~/.task.yaml` (with only one, it is picked
already), then press `Enter`. A card is added at the bottom of that list, and
the task screen comes back saying which task number it got; marks typed
before `PF4` are kept. If the card cannot be added, the screen says why.
`PF3` goes back without adding.

The task screen never redraws on a timer, since that would wipe marks typed
but not yet sent.

Archiving a task marks its Trello card's due date complete and archives the
card. If Trello fails, the task is left open. Tasks are marked by card, so
a task's number changing since it was shown does not matter. Archiving
stops at the first task that fails; it stays marked, to try again.

The calculator is RPN. Type numbers and the operators `+ - * / ^`,
separated by spaces, and press `Enter`: numbers are pushed and operators
applied in order, so `0x1F 3 ^` pushes 31 and cubes it. Numbers are decimal
(`42`, `-1.5`, `1e3`) or hex integers (`0x1F`, `-0x10`). Each stack level is
shown in decimal and, for an integer, hex. Integers are exact at any size,
and `/` gives an integer when it divides evenly; anything else is a float,
with no hex. A line with an error changes nothing and is left to fix. The
stack lasts as long as the connection.

- `PF4` / `PF5` / `PF6`: drop the top number / swap the top two / clear,
  after running whatever is typed
- `PF9`: switch between plain numbers and dBm
- `PF3`: back to the dashboard

In dBm mode every value carries a unit, written straight after the number:
`dBm` or `mW` for a power, `dB` for a gain or loss (the `B` and `W` may be
either case: `10dbm`, `5mw`). A power is shown in both dBm and mW, a gain in
dB and as the ratio it multiplies by. A number with no unit is a plain
number, for scaling. The operators work by what the values are:

| | power, power | power, dB | dB, dB | with a plain number |
| --- | --- | --- | --- | --- |
| `+` | sum of the powers (mW) | gain: dBm + dB | dB + dB | - |
| `-` | difference of the powers (mW) | loss: dBm - dB | dB - dB | - |
| `*` | - | - | - | scales a power (mW) or a gain (dB) |
| `/` | ratio, in dB | - | - | scales a power (mW) or a gain (dB) down |
| `^` | - | - | - | raises a power (mW) to the number |

So `0dBm 0dBm +` is 3.01 dBm (1 mW + 1 mW), `10dBm 3dB +` is 13 dBm, and
`10mW 2 *` is 20 mW. `dB + dBm` is a gain too; anything else not in the table
is refused. Each mode has its own stack.

`sum` and `avg` replace the whole stack with the total, or the mean, of
everything on it: `1 2 3 4 avg` leaves 2.5. In dBm mode the values must all
be of one kind: powers add in mW, so `avg` of powers is the average power
(`1mW 3mW avg` is 2 mW, 3.01 dBm); dB values add in dB.

The checklists (`checklist` or `cl`) are named lists of items, each
checked off with an `X`, kept for reuse: `PF6` unchecks every item, after
confirmation, to start the list over. The first screen lists the
checklists, with how many of each one's items are done: green when they
all are, red when some are not.

Names and items are protected, so `Tab` goes straight down the column of
one-character fields to their left: `S` on the list, to select a
checklist, and `X` on a checklist, to check an item off.

- type a name, or an item, on the `New ... ===>` row and press `Enter` to
  add it at the end; the cursor stays there to add another;
- on the list, type `S` (or any character) beside a checklist and press
  `Enter` to open it, or with nothing typed, press `Enter` with the cursor
  in its `S` column;
- on the list, type `*` beside a checklist to star it as active, and blank
  the `*` to unstar it. Starred checklists are listed first, their names
  green when every item is done and red when not; unstarred, a checklist
  goes back to its place. Starred checklists cannot be moved with `PF10` /
  `PF11`, and unstarred ones move past them. Typing `S` over a star selects
  the checklist without unstarring it;
- on a checklist, type `X` beside an item to check it off, or blank the
  `X` to uncheck it. Items still to do are red, those done green;
- to change a name or an item, press `PF4` on it: it moves to the bottom
  row, now `Change ... ===>`, to be typed over, or blanked to remove it
  (a checklist's items go with it), then `Enter`.

`PF4`, `PF10` and `PF11` act on the checklist selected, or with none
selected, the one the cursor is on; on a checklist, the item the cursor is
on. Selecting more than one checklist is refused, with the selections left
to fix.

Whatever is typed is saved with the next key, whichever key it is. If a
mark is anything but `X` or blank, nothing is saved and what was typed is
left on the screen to fix.

- `Enter`: save; on the list, open the checklist selected, or with none
  and nothing changed, the one whose `S` column the cursor is in
- `PF4`: change or remove a name or an item
- `PF10` / `PF11`: move a checklist (not a starred one) or an item up /
  down; the cursor goes with it, onto the next page if need be, so the key
  can be pressed again
- `PF6` (on a checklist): uncheck every item, after a confirmation listing
  the items checked; `PF4` there unchecks them, `PF3` goes back
- `PF7` / `PF8`: previous / next page
- `PF3`: back (from a checklist, to the list); while changing a name or an
  item, cancel the change

Neither screen redraws on a timer, since that would wipe what was typed but
not yet saved.

The checklists are kept in `-checklist-file` as JSON, rewritten (by atomic
rename) on every change and read on every redraw, so every session sees
the same checklists:

```json
{"checklists": [{"id": 1, "name": "Morning", "active": true, "items": [
  {"id": 2, "text": "Pills", "done": true}]}]}
```

A checklist's heading says how many other sessions of this server have it
open too, e.g. `(1 other session viewing)`. Like the rest of the screen, it
is as of the last key pressed.

IDs are unique across the file. Changes are made by ID, and only to what
was typed over, so two sessions changing the same checklist do not undo
each other's work, except where both typed over the same entry.

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

The tasks are the cards on Trello lists, read from Trello; nothing is kept
on disk. Which lists, and the tag each one's cards get, come from the
`trello:` section of `~/.task.yaml`, the Trello credentials from
`~/.task.secret.yaml` (settings there override the first file), and
`ignore-tags` from either:

```yaml
# ~/.task.yaml
ignore-tags: [shopping]
trello:
  tasks:
    "Work Tasks":          # board
      "Today": work        # list: tag
    "Personal Tasks":
      "Today": personal

# ~/.task.secret.yaml
trello:
  api-key: ...
  token: ...
```

Tasks are listed by board, then list, then the cards' order on the list,
and numbered in that order; the numbers change as tasks come and go.

Every session shares one copy of the tasks, kept in memory for 15 minutes.
When it is older than that, the next redraw starts fetching them again in
the background (one request for the boards, then two per board) and goes on
showing the old copy until the new one arrives; the next redraw after that
shows it. Adding or archiving a task changes the copy at once and starts a
fetch in the background, to catch up with anything else changed on Trello.
If a fetch fails, the old copy is kept, the task heading says it is stale
and why, and the fetch is tried again after a minute. Until the first fetch
finishes, the heading says the tasks are being fetched. The configuration is
read at each fetch, so a change to it shows up within 15 minutes, without a
restart.

## Layout

- `main.go`: flags, background watchers, and the accept loop.
- `internal/session`: per-connection TN3270 session and dashboard rendering.
- `internal/busy`: busy indicator WebSocket feed client.
- `internal/agenda`: shared, periodically refreshed calendar cache.
- `internal/tasks`: the tasks from Trello, cached, and the task program's
  configuration.
- `internal/checklist`: the checklist file.

Telnet/TN3270E negotiation is
[`github.com/jmaslak/go-3270e`](../go-3270e).
