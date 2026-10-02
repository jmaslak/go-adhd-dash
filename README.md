# go-adhd-dash

`adhd-dash` serves a dashboard to TN3270 (mainframe) terminal clients. One
screen shows:

- the **busy indicator**: whether the light is red, green or off, and how
  long until the next meeting, from
  [go-busy-indicator](../go-busy-indicator)'s WebSocket status feed;
- the **agenda**: every meeting under way or starting in the next 24 hours,
  from the Google calendars the user has connected (see [Agenda](#agenda)),
  with the one in progress marked `NOW` and a
  countdown to the next. All-day events and out-of-office events (by
  title: "out of office" or the word "OOO") are left out. If they do not
  all fit alongside the tasks, the first page gives them the room and the
  tasks move to later pages;
- the **tasks**: the cards on the Trello lists the user has linked (see
  [Tasks](#tasks)), then their starred checklists.

The screen redraws itself every `-refresh`, so it can be left up on a spare
terminal. `PF5` turns that off and on again for the session; the title row
shows `AUTO-REFRESH` while it is on.

## Usage

```
go build -o adhd-dash .
./adhd-dash -busy-url ws://localhost:3334/feed
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
| `-agenda-file` | (none) | show every user the agenda in a JSON file instead of their Google calendars |
| `-agenda-refresh` | `5m` | how often a user's calendars are read |
| `-http-port` | `3280` | TCP port the web pages (home page, privacy policy, terms of service) are served on over plain HTTP, on `-host`; `0` for none (see [Web site](#web-site)) |
| `-checklist-file` | `~/.adhd-dash-checklists.json` | JSON file the checklists are kept in |
| `-users-file` | `~/.adhd-dash-users.json` | JSON file the users are kept in |
| `-max-connections` | `64` | most connections open at once, not counting this machine's; `0` for no limit |
| `-max-connections-per-ip` | `16` | most open at once from one address (an IPv6 one by its /64); `0` for no limit |
| `-login-timeout` | `60s` | how long the login screen waits for a login |
| `-audit-log` | `~/.adhd-dash-audit.log` | file logins, logouts and disconnections are logged to; empty for none |

Leaving `-busy-url` empty leaves that section showing "not configured"
rather than failing. The calendars are each user's own, chosen with the
`google` command and kept in `-users-file`; a user with none sees no
calendar information at all.

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
`checklist`, `chat`, `google` (connect your Google calendar), `trello`
(link your Trello account), `busy`,
`green`, `off`, `auto`, `up`, `down`, `refresh`, `admin`, `help` and `exit`,
in either case, with a few aliases (`task`, `calendar`, `cl`, `gcal`,
`next`, `prev`, `quit`, `logoff`, `?`). The dashboard's timed redraw writes over the screen
without erasing it, so a command half typed survives it.

The chat (`chat` or `PF11`) is shared by every session of this server, each
posting as its user's name (the console with no user database, as its LU
name). Names are shown as wide as the longest posted, up to 16 characters,
and cut beyond that. Type a message on the `Message ===>` line and press
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
- `5`: the Google OAuth client every user connects their calendar through
  (see [Agenda](#agenda)).
- `6`: the web site: its public address, and the organization and contact
  email its privacy policy and terms name (see [Web site](#web-site)).
- `7`: the Trello API key every user links their Trello account through
  (see [Tasks](#tasks)).

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
  as it was). A deleted user's checklists and Google calendar go with
  them: the checklists are deleted (the confirmation says how many), the
  calendar was kept in their entry, and Google is asked to withdraw its
  authorization. Their ID is never given to another user, and any
  checklists of a user no longer there are deleted when the server starts. Or type `P` to change their password:
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
included; so does the chat. Someone on the chat screen in two sessions is
listed there once.

On the calendar, a month is shown with the selected day's events beside it. Move the cursor onto a day and press
`Enter` to select it.

- `PF7` / `PF8`: previous / next month
- `PF4`: back to today
- `PF5`: turn auto-refresh off / on
- `PF3`: back to the dashboard

The calendar reads each month from Google as it is shown and reuses it for
`-agenda-refresh`, separately from the today-and-tomorrow agenda. A user
with no calendar connected sees the month and the clocks, with a note to
use `google`.

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
list to put it on, from those the user chose (with only one, it is picked
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
checked off with an `X`, kept for reuse. Each user has their own: no one
sees, changes or moves another's, and the dashboard lists only the user's
own starred ones. Checklists from before they were each a user's are given
to the admin user (the admin called `admin`, else the console's user if an
admin, else the first admin) when the server starts, which it logs. `PF6` unchecks every item, after
confirmation, to start the list over. The first screen lists the
checklists, with how many of each one's items are done: green when they
all are, red when some are not.

On the list, each checklist's row has an `S` column, to select it, a `*`
column, to star it, then its name. On a checklist, each item's row has an
`X` column, to check it off, then the item. Names and items can be typed
over.

- type names, or items, on the blank rows that fill out the last page
  after the last one, and press `Enter` to add them, in order (rows left
  blank are skipped); the cursor goes to the first blank row after them,
  to add more. A full last page is followed by a page of blank rows. On
  the list, each blank row has a `*` column too: type in it to add the
  checklist starred (it does nothing without a name beside it);
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
- type over a name or an item to change it, or blank it to remove it (a
  checklist's items go with it). Nothing is saved until it is confirmed:
  the next key shows the changes and removals, with each removed
  checklist's item count, and everything else typed alongside; `PF4`
  there saves it all (then, if the key was `Enter`, opens the checklist
  selected), `PF3` goes back with it all still typed, to change, and
  `PF12` discards it all.

`PF10` and `PF11` act on the checklist selected, or with none selected,
the one the cursor is on; on a checklist, on the item the cursor is on.
Selecting more than one checklist is refused, with the selections left to
fix.

Whatever is typed is saved with the next key, whichever key it is, except
that a name or item typed over or blanked asks for confirmation first (and
the key, unless it is `Enter`, does nothing else), and so does `PF3` with
anything typed. If a mark is anything but `X` or blank, nothing is saved
and what was typed is left on the screen to fix.

- `Enter`: save; on the list, open the checklist selected (unless it is
  being removed)
- `PF10` / `PF11`: move a checklist (not a starred one) or an item up /
  down; the cursor goes with it, onto the next page if need be, so the key
  can be pressed again
- `PF6` (on a checklist): uncheck every item, after a confirmation listing
  the items checked; `PF4` there unchecks them, `PF3` goes back
- `PF7` / `PF8`: previous / next page
- `PF3`: back (from a checklist, to the list). With anything typed but a
  selection, it first lists what was typed and asks: `PF4` saves it and
  goes back, `PF12` discards it and goes back, and `PF3` returns to the
  screen with it still typed

Neither screen redraws on a timer, since that would wipe what was typed but
not yet saved.

The checklists are kept in `-checklist-file` as JSON, rewritten (by atomic
rename) on every change and read on every redraw, so every session of a
user sees the same checklists. `owner` is the user's ID in `-users-file`:

```json
{"checklists": [{"id": 1, "name": "Morning", "active": true, "owner": 1,
  "items": [{"id": 2, "text": "Pills", "done": true}]}]}
```

A checklist's heading says how many other sessions of this server (its
user's, logged in elsewhere) have it open too, e.g. `(1 other session viewing)`. Like the rest of the screen, it
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

Each user connects their own Google calendar, and chooses which of its
calendars to show. Their authorization and choices are kept in their entry
in `-users-file` (readable only by its owner). A user with none connected,
or none chosen, sees no agenda section on the dashboard, and the tasks get
the room.

**Once, an admin sets up the OAuth client** every user connects through:
admin menu option `5`. The screen lists the steps: in the Google Cloud
console, create a project, enable the Google Calendar API, set up the
consent screen (Internal for a Workspace domain's own users; otherwise
External, and *Publish app*, since in Testing an authorization lasts only 7
days), and create a client of type **Web application**, whose authorized
redirect URI is the web site's `google/callback` (the screen shows it, once
the site's address is set on option `6`, e.g.
`https://adhd.example.com/google/callback`). Paste its ID (two rows are
there for it) and secret, and press `Enter`. The secret is never shown.
Leaving the secret blank keeps the one set; blanking both removes the
client. Replacing or removing a client that users are connected through
asks first, as they will each have to connect again.

```json
{"users": [{"id": 1, "name": "admin", "google": {"client_id": "...",
  "refresh_token": "...", "calendars": [{"id": "you@example.com",
  "name": "you@example.com", "alias": "me"}]}, ...}],
 "google_client": {"client_id": "....apps.googleusercontent.com",
  "client_secret": "..."}}
```

**Each user then connects on the web site** (see [Web site](#web-site)),
which must be served and have its address set. Typing `google` (or `gcal`)
on the dashboard shows the steps:

1. In a browser, go to the site's `google` page, e.g.
   `https://adhd.example.com/google`.
2. Sign in there with the same user name and password as on the terminal.
3. Choose *Connect*, and at Google allow read-only access to your calendars
   (the dashboard asks for `calendar.readonly`). If the client is published
   but not verified, Google warns first: choose *Advanced*, then continue.
   Google sends the browser back to the site, which stores the
   authorization in your entry and says *Connected*, with a *Go to the
   terminal* button: a link to the site's `3270/`, where the web server in
   front is expected to serve a browser terminal for this server.
4. Back on the terminal, press `Enter`, to go on to choosing calendars.

Once connected, the screen lists every calendar on your Google calendar
list, your own already marked to show, followed by blank rows filling the
page:

- type `X` in the left column to show a calendar, blank it to stop;
- type an alias, up to 10 characters (no commas or brackets), to have its
  events shown as `[alias] title`;
- a calendar not on your list (someone else's shared with you, say) can be
  typed by its ID on a blank row, across both of the row's lines, with an
  alias; it is checked with Google when you press a key, and then added;
- a long name takes two rows.

`Enter` saves the choices. `PF3` goes back; with choices not saved, it
asks first, and a second `PF3` leaves without saving them. `PF7` / `PF8`
page, `PF5` reads your calendar list again, `PF9` shows the steps for
connecting again on the web site, keeping the choices (`Enter` there waits
for the new authorization; `PF3` goes back), and `PF6` disconnects, after a confirmation: your
authorization and choices are removed and Google is asked to withdraw it.

Each user's calendars are read every `-agenda-refresh` while any of their
sessions is showing them, and that agenda is shared by their sessions; one
not used for three intervals is dropped. If a fetch fails, the last agenda
stays up, marked stale. If Google refuses the authorization (it was
withdrawn, or the client changed), the agenda says to type `google` to
reconnect.

Cancelled events and events you declined are left out, as the busy indicator
does.

The token refresh and event reading are go-busy-indicator's `gauth` and
`gcal` packages, imported rather than copied; the authorization flow and the
calendar list are in `internal/google`.

`-agenda-file` takes a JSON list, shown to every user in place of their
Google calendars, for trying the dashboard without Google:

```json
[{"summary": "Standup", "start": "2026-09-27T09:00:00-06:00",
  "end": "2026-09-27T09:15:00-06:00", "all_day": false, "calendar": "work"}]
```

`calendar` is optional; it is shown in brackets as an alias is.

### Web site

Google asks an app that reads its users' calendars for a home page, a
privacy policy and terms of service before it verifies the app. The server
serves them itself, over plain HTTP on `-http-port` (3280), on `-host`:

- `/`: what the dashboard is and does, and what it does with a Google
  calendar;
- `/privacy`: the privacy policy, written for this kind of service, run by
  an organization for its own users: what is kept (accounts, checklists,
  Google authorizations, chat, records of use), who sees it, how long it is
  kept, and the statement Google requires, that Google user data is used
  under the Google API Services User Data Policy's Limited Use
  requirements;
- `/terms`: the terms of service: accounts, acceptable use, monitoring by
  administrators, the Google calendar, and the service provided as it is;
- `/google`: where a user signs in, with their terminal user name and
  password, and connects their Google calendar (see [Agenda](#agenda));
- `/trello`: the same, for linking their Trello account (see
  [Tasks](#tasks)).

They are meant to be reached at an HTTPS address through a web server in
front (a reverse proxy such as nginx or Caddy, terminating TLS on port 443
and passing requests to port 3280). The server never sees that address, so
an admin types it on admin menu option `6`: `Public address`, e.g.
`https://adhd.example.com/`, with the `Organization` running the service
and a `Contact email`, which the policy and terms name. Every link on the
pages, and each page's canonical address, uses the public address; until
it is set they link by path. The screen lists the three pages' public
addresses, for the Google consent screen's Branding page. The settings are
kept in `-users-file`; blanking all three removes them.

The pages run no scripts, and are sent with a strict
`Content-Security-Policy` and the usual headers against framing and
sniffing. The public ones answer only `GET` and `HEAD`. Signing in follows
the terminal's rules (the default admin password is refused, and so is a
restricted user), and is recorded in the audit log with `via=web`, the
address it came from, and the `X-Forwarded-For` the proxy added. Five
failed sign-ins as one name within 15 minutes stop that name signing in
here until the oldest is 15 minutes old. A sign-in lasts 30 minutes, in
memory, kept by an `HttpOnly`, `SameSite=Lax` cookie (`Secure` when the
address is `https`) sent only to the `/google` pages. Forms are accepted
only from the site's own origin, and signing out takes the session's own
token. A connection begun at Google is good for one return, within 10
minutes, to the browser session that began it. The privacy policy and terms are a starting
point: have whoever handles your organization's legal matters read them.

### Tasks

Each user links their own Trello account, and chooses which of its lists'
cards are their tasks, with a short tag for each list, shown in brackets
before its tasks. The token and choices are kept in their entry in
`-users-file`. A user with none linked, or no list chosen, has no tasks:
the dashboard lists only their starred checklists, and the task screen
(`tasks`, `PF10`) says to link Trello. (`~/.task.yaml` is no longer read.)

**Once, an admin sets the Trello API key** every user links through: admin
menu option `7`. The screen lists the steps: signed in to Trello, go to
`trello.com/power-ups/admin`, make a Power-Up (named `exec-3270`) in a
workspace, generate its API key, and add the web site's address (shown on
the screen) under *Allowed origins*. Type the key and press `Enter`; its
secret is not needed. Replacing or removing a key that users are linked
through asks first, as each will have to link again.

**Each user then links on the web site** (see [Web site](#web-site)).
Typing `trello` on the dashboard shows the steps:

1. In a browser, go to the site's `trello` page, e.g.
   `https://adhd.example.com/trello`.
2. Sign in there with the same user name and password as on the terminal
   (one sign-in serves both this page and the Google one).
3. Choose *Link*, and at Trello allow access to your boards (read and
   write, not expiring). Trello sends the browser back to the site with the
   token in the address's `#fragment`, which only the browser sees; a short
   script on that page posts it to the server, which checks it with Trello,
   stores it in your entry, and says *Linked*. Without scripts, the page has
   a field to paste the token into.
4. Back on the terminal, press `Enter`, to go on to choosing lists.

The screen then lists every open list of every open board, as
`Board / List`:

- type `X` in the left column to show a list's cards as your tasks, blank
  it to stop;
- type a tag, up to 10 characters (no spaces, commas or brackets), for its
  tasks to be shown with, as `[tag] title`; a list with none shows its
  tasks untagged;
- a list chosen that Trello no longer has is shown in red, `(gone from
  Trello)`, to unchoose.

`Enter` saves the choices. `PF3` goes back, asking first if they are not
saved. `PF7` / `PF8` page, `PF5` reads the lists again, `PF9` shows the
steps for linking again (keeping the choices), and `PF6` unlinks, after a
confirmation: the token and choices are removed, and Trello is asked to
withdraw the token. Deleting a user does the same.

Lists are kept by Trello's IDs, so a board or list renamed keeps working
(its old name is shown until it is chosen again). Tasks are listed in the
order of the lists on the screen, then the cards' order on each list, and
numbered in that order; the numbers change as tasks come and go. A new task
(`PF4` on the task screen) is added at the bottom of one of the lists
chosen.

Each user's tasks are kept in memory for 15 minutes, shared by their
sessions. When they are older than that, the next redraw starts fetching
them again in the background (one request per list) and goes on showing
the old ones until the new arrive. Adding or archiving a task changes them
at once and starts a fetch in the background. If a fetch fails, the old
tasks are kept, the task heading says they are stale and why, and the fetch
is tried again after a minute.

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
- `internal/agenda`: each user's periodically refreshed calendar cache.
- `internal/google`: connecting a Google calendar: the OAuth flow and the
  calendar list.
- `internal/web`: the web pages: home, privacy policy and terms.
- `internal/tasks`: the tasks from Trello, cached, and the task program's
  configuration.
- `internal/checklist`: the checklist file.

Telnet/TN3270E negotiation is
[`github.com/jmaslak/go-3270e`](../go-3270e).
