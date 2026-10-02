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
| `-max-connections` | `64` | most connections open at once, not counting this machine's; `0` for no limit |
| `-max-connections-per-ip` | `16` | most open at once from one address (an IPv6 one by its /64); `0` for no limit |
| `-login-timeout` | `60s` | how long the login screen waits for a login |
| `-audit-log` | `~/.adhd-dash-audit.log` | file logins, logouts and disconnections are logged to; empty for none |

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
  and `PF3` goes back to the menu. It also redraws every `-refresh`, like
  the dashboard (unless auto-refresh is off), writing over the screen
  without erasing what is typed. Until a key is pressed, each session stays
  in its row, so that a mark stays beside the session it was typed for: one
  that has disconnected shows as `Disconnected`, and new ones are added at
  the end.
- `3`: clear the chat, after a confirmation (`PF4`) saying how many
  messages it deletes. Every session on the chat screen shows it emptied at
  once.
- `4`: the users (see [Users](#users)).

### Users

The users are kept in `-users-file`, which is made when the server starts
if it does not exist, holding one user, `admin`, with the password `admin`.
That password is refused at the login screen: the admin connects as the
console (which is logged in as `admin` with no password) and changes it
there first, and the log says so. The file is readable only by its owner and
rewritten by atomic rename; passwords are stored hashed with Argon2id (RFC
9106's second recommended parameters: 64 MiB, 3 passes, 4 lanes, a 16-byte
random salt), in PHC string format, so the parameters can be raised later
without breaking the hashes already stored.

Admin menu option `4` lists the users, a page at a time (`PF7` / `PF8`),
each with a one-character command field and `Admin`, `Restricted` and
`Console` fields:

- type `D` beside a user to delete them (a confirmation lists them first,
  and saves nothing until `PF4`; `PF3` goes back with what was typed left
  as it was), or `P` to change their password:
  the bottom rows then ask for it (typed hidden), `Enter` saves it and
  `PF3` cancels;
- type `Y` or `N` under `Admin` to make a user an admin or not, and under
  `Restricted` to restrict them or not (see below);
- type `Y` under `Console` to make a user the one the console logs in as
  (see below), taking it from whoever had it;
- on the bottom rows, type a new user's name, `Y` or `N` for `Admin` and
  `Restricted` (blank is `N`), and their password, to add them. There is
  no room there for `Console`: add the user, then type `Y` on their row.

`Enter` (or paging) saves everything typed at once. Names must be unique
(ignoring case) and have no spaces, no admin may be restricted, at least
one user must stay an admin, and exactly one user must be the console's (so
the console's user can be deleted only once another is given `Y` under
`Console`): a change that would break any of these is refused whole, with what was
typed left to fix (passwords aside, which are never drawn again). `PF3`
goes back to the admin menu without saving.

### Login

Every session logs in first, on a `LOGIN` screen under an `exec/3270`
banner, with a user name (ignoring case) and password from the users,
except the console. `admin` with the default password `admin` is refused
(it counts as a wrong try); change it from the console. Three wrong tries,
or a minute without logging in (`-login-timeout`), disconnect it; `PF3` disconnects at once. Each login,
and each failed one, is logged with the address it came from.

The console is a session whose TN3270E client asks for the LU name
`CONSOLE` (with `s3270` or `c3270`, connect to `CONSOLE@127.0.0.1:3270`),
from this machine: `127.0.0.1` or `::1` (or `127.0.0.1` written as
`::ffff:127.0.0.1`). It does not log in: it is logged in as the user with
`Y` under `Console`, which a users file from before there was one takes to
be `admin` (or, with no admin called that, the first admin). A change to
which user that is takes effect when the console next connects. If the
users file cannot be read, the console gets the login screen like any other
session. There is only ever one: while a console is connected, any other
client on this machine asking for `CONSOLE` is refused it (TN3270E
`DEVICE-TYPE REJECT`, reason `DEVICE-IN-USE`), and logged; an
administrator can free it by terminating the console from the activity
viewer. A client anywhere else asking for `CONSOLE` is refused it (reason
`INV-NAME`, whether or not a console is connected), and logged; it may ask again for another name, or none,
and log in, but most clients give up. Any other name a client asks for is
not honoured: it gets one of the server's own, `AD` and a number. A client
that does not speak TN3270E cannot ask for a name, so cannot be the
console.

A user who is not an admin cannot open the admin menu, and neither can the
console when its user is not an admin; a restricted console user has only
the calculator, and logging off there disconnects the console.

### Audit log

`-audit-log` gets a line for every login, failed login, logout and
disconnection, appended and flushed to disk as it happens (the file is made
readable only by its owner). Each line is the time, the event, and
`key=value` fields, a value quoted when it is empty or holds a space:

```
2026-09-29T20:53:56-06:00 LOGIN-FAILED user=bob lu=AD000002 ip=127.0.0.1 session=2 try=1
2026-09-29T20:53:56-06:00 LOGIN user=bob lu=AD000002 ip=127.0.0.1 session=2
2026-09-29T20:53:56-06:00 LOGOUT user=bob lu=AD000002 ip=127.0.0.1 session=2
2026-09-29T20:53:57-06:00 DISCONNECT user=bob lu=AD000003 ip=127.0.0.1 session=3 reason="connection lost: EOF"
```

`LOGIN-FAILED` names the user as typed, with `try` counting the tries. The
console counts as logging in when it connects, as its user. A
session that logged in ends with `LOGOUT` when the user leaves (`PF3` or
`exit` on the dashboard, or `PF3` in a restricted user's calculator), or
else `DISCONNECT` with a `reason`: `connection lost` (and why), `terminated
by an administrator`, or `server shut down`.
Connections that never log in are not logged there, only in the server's
own log.

A restricted user goes straight to the calculator on logging in, and has
nothing else: `PF9` switches between it and the dBm calculator, and `PF3`
(shown as `PF3=Log off`) logs them off. A change to a user's restricted
flag takes effect at their next login.

The activity viewer shows who each session logged in as, the console
included; the chat still names sessions by LU.

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

On the list, each checklist's row has an `S` column, to select it, a `*`
column, to star it, then its name, which can be typed over. On a checklist,
items are protected, so `Tab` goes straight down the column of `X` fields
to their left.

- on the list, type a name on the blank row after the last checklist (on
  the last page) and press `Enter` to add it; the cursor goes to the blank
  row after it, to add another;
- on a checklist, type an item on the `New item ===>` row and press
  `Enter` to add it at the end; the cursor stays there to add another;
- on the list, type `S` (or any character) in a checklist's `S` column
  and press `Enter` to open it, wherever the cursor is. Without one typed,
  `Enter` opens nothing;
- on the list, type `*` (or any character) in a checklist's `*` column to
  star it as active, and blank the `*` to unstar it. Starred checklists are
  listed first, their names green when every item is done and red when
  not; unstarred, a checklist goes back to its place. Starred checklists
  cannot be moved with `PF10` / `PF11`, and unstarred ones move past them;
- on a checklist, type `X` beside an item to check it off, or blank the
  `X` to uncheck it. Items still to do are red, those done green;
- on the list, type over a name to rename the checklist, or blank it to
  remove the checklist and its items. Nothing is saved until it is
  confirmed: the next key shows the renames and removals, with each
  removed checklist's item count, and everything else typed alongside;
  `PF4` there saves it all (then, if the key was `Enter`, opens the
  checklist selected), `PF3` goes back to the list with it all still
  typed, to change, and `PF12` discards it all;
- to change an item, press `PF4` on it: it moves to the bottom row, now
  `Change item ===>`, to be typed over, or blanked to remove it, then
  `Enter`.

`PF10` and `PF11` act on the checklist selected, or with none selected,
the one the cursor is on; on a checklist, `PF4`, `PF10` and `PF11` act on
the item the cursor is on. Selecting more than one checklist is refused, with the selections left
to fix.

Whatever is typed is saved with the next key, whichever key it is, except
that a name typed over or blanked asks for confirmation first (and the
key, unless it is `Enter`, does nothing else), and so does `PF3` with
anything typed. If a mark is anything but `X` or blank, nothing is saved
and what was typed is left on the screen to fix.

- `Enter`: save; on the list, open the checklist selected (unless it is
  being removed)
- `PF4` (on a checklist): change or remove an item
- `PF10` / `PF11`: move a checklist (not a starred one) or an item up /
  down; the cursor goes with it, onto the next page if need be, so the key
  can be pressed again
- `PF6` (on a checklist): uncheck every item, after a confirmation listing
  the items checked; `PF4` there unchecks them, `PF3` goes back
- `PF7` / `PF8`: previous / next page
- `PF3`: back (from a checklist, to the list). With anything typed but a
  selection, it first lists what was typed and asks: `PF4` saves it and
  goes back, `PF12` discards it and goes back, and `PF3` returns to the
  screen with it still typed. While changing an item, `PF3` cancels the
  change instead, saving any marks typed

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

Every screen but the login screen also shows the state in its title row:
black on red across the whole row while the light is red, black on green
while it is green, and unchanged while it is off or the feed is down. (On
the dashboard it runs on into the busy banner below, the same color, with
no gap.) A restricted user's title row is never colored. A screen
that does not redraw on a timer shows a change of state at the next key.

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

### Limits

A connection over `-max-connections` in all, or `-max-connections-per-ip`
from one address (an IPv6 address counted by its /64), is closed before
anything is sent to it. Connections from this machine are not counted, so
that a flood from elsewhere cannot lock out the console. Refusals are
logged at most once every 10 seconds, with a count of those not logged.

Telnet and TN3270E negotiation must finish within 30 seconds, and the login
screen waits `-login-timeout`; with the caps, that bounds how many
connections a client can hold and for how long.

At most 4 passwords are checked (or hashed, in the user editor) at once:
each check uses 64 MiB, on purpose, so many logins at once could otherwise
exhaust the server's memory. A login waits up to 10 seconds for its turn;
if it does not get one, the screen says the server is busy, and it does not
count as a wrong try.

### Screen text

Much of what the screens show was written by someone else: calendar
invitations, Trello cards, chat messages, checklists, user names. Before a
screen is sent, every character that the terminal's code page would encode
to anything but a byte it shows (3270 orders such as start-field or
set-buffer-address, other controls, and FF, the telnet IAC byte) is replaced
with `?`, so text cannot redraw, add fields to, or break into the screen of
whoever reads it. User names may not hold control characters at all.

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
