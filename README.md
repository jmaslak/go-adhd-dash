# go-adhd-dash

`adhd-dash` serves a dashboard to TN3270 (mainframe) terminal clients. One
screen shows:

- the **busy light**: whether it is red, green or off, as this server
  drives the Luxafor flags attached to it (see [Busy light](#busy-light)),
  and,
  for a user with a calendar connected, their meeting under way or how long
  until their next one (or "No meetings in next 24 hours");
- the **agenda**: every meeting under way or starting in the next 24 hours,
  from the Google calendars the user has connected (see [Agenda](#agenda)),
  with the one in progress marked `NOW` and a
  countdown to the next. All-day events and out-of-office events (by
  title: "out of office" or the word "OOO") are left out. A meeting every
  other person invited to declined has a red `X` before its title. If the
  meetings do not all fit alongside the tasks, the first page gives them the room and the
  tasks move to later pages;
- the **tasks**: the cards on the Trello lists the user has linked (see
  [Tasks](#tasks)), then their starred checklists.

The screen redraws itself every second (`-refresh`), keeping its clock
current, so it can be left up on a spare terminal; each redraw writes over
the screen without erasing it, so a command half typed survives it. A
change of the user's busy state (see [Busy light](#busy-light)) redraws it
at once.

## Usage

```
go build -o adhd-dash .
./adhd-dash
```

Then connect a TN3270 emulator (c3270, x3270, s3270, ...) to `host:port`.
Any screen size the client reports is used: Model 2's 24x80 up through
Model 5's 27x132 or larger.

The files are kept in the home directory, under names `ls` shows.
They were once dotfiles (`~/.adhd-dash-users.json` and so on): at startup,
a file left at its default name that is missing, where its old dotfile is
there, is given the dotfile, renamed, and the server logs it. With both,
the new one is used and the old one left alone.

### Installing on Ubuntu

`install-ubuntu.sh` installs the server as a systemd service. Run again,
it reinstalls it, to upgrade or repair it, and restarts it on the new
program; the program is replaced while the old one runs, so a failure on
the way leaves the server running, and the restart comes only once all is
in place:

```
sudo ./install-ubuntu.sh                      # builds it here, with Go 1.27+
sudo ./install-ubuntu.sh --binary adhd-dash   # or one built elsewhere:
                                              # GOOS=linux GOARCH=amd64 go build
```

It makes a system user, `adhd-dash`, whose home, `/var/lib/adhd-dash`,
holds the server's files; installs `/usr/local/bin/adhd-dash`; writes the
options to `/etc/default/adhd-dash` the first time only (`--host`, `--port`
and `--http-port` set them then; by default only this machine can
connect); writes a sandboxed systemd unit, restarting the server only
after a failure (shutting it down from the admin menu stops it); and
enables and starts it. Logs go to the journal: `journalctl -u adhd-dash`.

It also writes `/etc/udev/rules.d/60-adhd-dash.rules`, giving the server's
user the Luxafor flag and the Stream Deck Mini, and first sets aside any
other rules for them, which would fight over their permissions: files of
nothing but such rules are moved to `/var/backups/adhd-dash-<date>/`, and in
files with other rules too, those rules are commented out (a copy goes to
the same place). Busy-indicator's old usbhid quirk
(`/etc/modprobe.d/luxafor.conf`), which leaves the flag no `/dev/hidraw`
node, is set aside likewise; the flag must then be plugged in again.

### Backups

`-backup`, or admin menu option `8`, saves the configuration and data,
the users file (users, passwords, Google and Trello links, the OAuth
clients, the site address) and the checklist database, in one archive in
`-backup-dir` (`~/backup`), named for when it was made (`-backup` then
exits):

```
adhd-dash -backup
# backed up to /home/you/backup/adhd-dash-20261002-174428.tar.gz
```

It is safe while the server runs: the database is copied as it is at one
moment. A second one in the same second is numbered (`...-174428-2`). The
archive and its directory are readable only by their owner, since the users
file holds password hashes and tokens. The audit log, and options kept
outside these files (such as `/etc/default/adhd-dash`), are not backed up.

`-restore FILE` puts them back, then exits. A file named without a
directory is looked for in `-backup-dir` if it is not in the current one.
The server must be stopped first: while it runs it holds
`<checklist-db>.lock` locked, and a restore (or a second server on the
same files) refuses to start. Everything in the archive is checked before
anything is replaced (the users file must be valid, the database sound),
and what is replaced is backed up first, to undo the restore with. What the
archive does not hold is left as it is.

```
adhd-dash -restore adhd-dash-20261002-174428.tar.gz
```

Both take the same `-users-file` and `-checklist-db` as the server, so give
them any it is given. Installed with `install-ubuntu.sh`, run them as its
user, in its home:

```
sudo -u adhd-dash env HOME=/var/lib/adhd-dash adhd-dash -backup
sudo systemctl stop adhd-dash
sudo -u adhd-dash env HOME=/var/lib/adhd-dash adhd-dash -restore adhd-dash-20261002-174428.tar.gz
sudo systemctl start adhd-dash
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-host` | `localhost` | address to listen on |
| `-port` | `3270` | TCP port to listen on |
| `-refresh` | `1s` | how often the screens that redraw themselves (the dashboard, calendar, chat and activity viewer) do, at whole seconds |
| `-flag` | `true` | drive the Luxafor flags attached to this machine as the busy light; `-flag=false` for none (`-externalrgb` and the banner still work) |
| `-streamdeck` | `true` | use a Stream Deck Mini attached to this machine as the busy light's buttons (see [Busy light](#busy-light)); `-streamdeck=false` for none |
| `-control-port` | `0` (none) | UDP port, on `-host`, for go-busy-indicator's `busy` command to set the busy light; unauthenticated, so firewall it |
| `-externalrgb` | (none) | command run with red, green and blue arguments (0–255) at each change of the busy light's color |
| `-agenda-file` | (none) | show every user the agenda in a JSON file instead of their Google calendars |
| `-agenda-refresh` | `5m` | how often a user's calendars are read |
| `-http-port` | `3280` | TCP port the web pages (home page, privacy policy, terms of service) are served on over plain HTTP, on `-host`; `0` for none (see [Web site](#web-site)) |
| `-checklist-db` | `~/adhd-dash-checklists.db` | SQLite database the checklists are kept in |
| `-checklist-json` | `~/adhd-dash-checklists.json` | JSON file checklists were kept in before `-checklist-db`, imported into it at startup, then renamed with `.imported` added |
| `-users-file` | `~/adhd-dash-users.json` | JSON file the users are kept in |
| `-backup` | `false` | back up `-users-file` and `-checklist-db` into `-backup-dir`, then exit (see [Backups](#backups)) |
| `-restore` | (none) | restore `-users-file` and `-checklist-db` from a backup, backing up what it replaces first, then exit; the server must be stopped |
| `-backup-dir` | `~/backup` | where `-backup` writes, and where `-restore` looks for a file named without a directory |
| `-max-connections` | `64` | most connections open at once, not counting this machine's; `0` for no limit |
| `-max-connections-per-ip` | `16` | most open at once from one address (an IPv6 one by its /64); `0` for no limit |
| `-login-timeout` | `60s` | how long the login screen waits for a login |
| `-audit-log` | `~/adhd-dash-audit.log` | file logins, logouts and disconnections are logged to; empty for none |

The calendars are each user's own, chosen on the
`settings` screen and kept in `-users-file`; a user with none sees no
calendar information at all.

## Keys

- `PF1` / `PF2`: mark yourself busy (red) / not busy until the meetings
  under way end: the busy light, for a user who controls it, else your own
  busy state (see [Busy light](#busy-light))
- `PF4`: calculator
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
`checklist`, `chat`, `settings`, `busy`, `green`, `off`, `up`, `down`,
`refresh`, `admin`, `help` and `exit`, in either case, with a few aliases
(`task`, `calendar`, `cl`, `next`, `prev`, `quit`, `logoff`, `?`).

`settings` is the user's own settings, one option per row with how each
stands, chosen by number as the admin menu's are: `1` changes their
password, `2` connects their Google calendar and chooses its calendars
(showing, e.g., "connected, 2 calendars shown"), and `3` links Trello and
chooses their task lists. Each of those comes back to the settings screen, saying
what was done. The commands `google` (or `gcal`), `trello` and `password`
(or `passwd`) still open them straight from the dashboard, and come back
there, but `help` does not list them. The dashboard's timed redraw writes over the screen
without erasing it, so a command half typed survives it.

The chat (`chat` or `PF11`) is shared by every session of this server, each
posting as its user's name. Names are shown as wide as the longest posted, up to 16 characters,
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
  the order they connected, with its LU name (one the server gave it, in
  the same `AD` form, if its client does not speak TN3270E and so
  negotiated none; blank only while it is still connecting), the screen it is on (a
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
  the dashboard, writing over the screen
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
- `8`: back up the users and checklists, at once, as `-backup` does; the
  menu says the archive's name and directory, or why it failed (see
  [Backups](#backups)).

### Users

Any user changes their own password on the `settings` screen (option `1`,
or `password` on the dashboard): their current password, then the new one twice, all typed
hidden, and `Enter`. It is refused if the current one is wrong, the two
new ones differ, or it is the same as the old one; `admin` cannot go back
to the default password `admin`.

A user name has at most 8 characters, only ASCII letters, digits, `.`,
`-` and `_`, not starting with `.` or `-`, and is unique, ignoring case
(logging in ignores case too); the login screen's and the user editor's
name fields hold no more.
The rule holds for the whole users file: one with a name breaking it
(written by hand, or by an older version) refuses every change until that
name is fixed in the file, and says which. Such a user can still log in.

A password, set here or by an admin (adding a user, or `P` in the user
editor), must have at least 8 characters, and must not contain the user
name, in any case. Passwords set before this rule keep working. Three wrong current passwords go back to
the dashboard. Sessions already logged in, here and on the web site, stay
so. An admin changes anyone's with `P` in the user editor, below.

The users are kept in `-users-file`, which is made when the server starts
if it does not exist, holding one user, `admin`, with the password `admin`.
Logged in with it, at the login screen like anyone, the admin gets the
CHANGE PASSWORD screen and nothing else until it is changed (`PF3` there,
or three wrong current passwords, log off), and the log says so. The web
site refuses it. The file is readable only by its owner and
rewritten by atomic rename; passwords are stored hashed with Argon2id (RFC
9106's second recommended parameters: 64 MiB, 3 passes, 4 lanes, a 16-byte
random salt), in PHC string format, so the parameters can be raised later
without breaking the hashes already stored.

Admin menu option `4` lists the users, a page at a time (`PF7` / `PF8`),
each with a one-character command field, a `Type` field, and a `Flag`
field:

- type `D` beside a user to delete them (a confirmation lists them first,
  and saves nothing until `PF4`; `PF3` goes back with what was typed left
  as it was). A deleted user's checklists and Google calendar go with
  them: the checklists are deleted (the confirmation says how many), the
  calendar was kept in their entry, and Google is asked to withdraw its
  authorization. Their ID is never given to another user, and any
  checklists of a user no longer there are deleted when the server starts. Or type `P` to change their password:
  the bottom rows then ask for it (typed hidden), `Enter` saves it and
  `PF3` cancels;
- type over a user's `Type` (in any case) to change what kind of user they
  are: `user`, an ordinary user; `admin`, who can open the admin menu;
  `restricted`, who can use the calculator and nothing else (see below); or
  `newuser`, an account for signing up users of their own (see below),
  which cannot control the busy light. In the users
  file, `admin` and `restricted` are the `admin` and `restricted` flags,
  and `newuser` the `new_user` flag; at most one is set, and none for
  `user`;
- type `Y` or `N` under `Flag` for a user to control the busy light or
  not: their calendar's meetings light it, and they can set it by hand
  (see [Busy light](#busy-light)). A restricted user cannot;
- on the bottom rows, type a new user's name, their `Type` (blank is
  `user`), and their password, to add them. There is no room there for
  `Flag`: add the user, then type `Y` on their row.

`Enter` (or paging) saves everything typed at once. Names must be unique
(ignoring case) and made only of the characters allowed, at least
one user must stay an admin: a change that would break any of these is refused whole, with what was
typed left to fix (passwords aside, which are never drawn again). `PF3`
goes back to the admin menu without saving.

### Login

Every session logs in first, on a `LOGIN` screen under an `exec/3270`
banner, with a user name (ignoring case) and password from the users.
`admin` with the default password `admin` logs in only to change it (see
[Users](#users)). Three wrong tries,
or a minute without logging in (`-login-timeout`), disconnect it; `PF3` disconnects at once. Each login,
and each failed one, is logged with the address it came from.

Every session goes by an LU name of the server's own, `AD` and a number,
whatever name its client asks for; one without TN3270E, which asks for
none, is given one all the same.

A user who is not an admin cannot open the admin menu.

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
2026-09-29T20:54:10-06:00 USER-CREATED user=amy lu=AD000004 ip=127.0.0.1 session=4 by=signup
```

`LOGIN-FAILED` names the user as typed, with `try` counting the tries. A
session that logged in ends with `LOGOUT` when the user leaves (`PF3` or
`exit` on the dashboard, `PF3` in a restricted user's calculator, or `PF3`
or signing up on the sign-up screen), or
else `DISCONNECT` with a `reason`: `connection lost` (and why), `terminated
by an administrator`, or `server shut down`.
Connections that never log in are not logged there, only in the server's
own log. `USER-CREATED` records a user signing up on the sign-up screen:
`user` is the user made, `by` the new-user account they signed in as.

A restricted user goes straight to the calculator on logging in, and has
nothing else: `PF9` switches between it and the dBm calculator, and `PF3`
(shown as `PF3=Log off`) logs them off. A change to a user's restricted
flag takes effect at their next login.

A user of type `newuser` is an account for letting people sign
themselves up: give out its name and password, and whoever logs in as it
gets the SIGN UP screen and nothing else. There they type a user name of
their own (by the same rules as any) and a password twice (at least 8
characters, not containing the name), and press `Enter`: an ordinary
user is made (type `user`, not controlling the busy
light), recorded in the audit log, and the session ends, saying to
connect again and log in as the new user. `PF3` logs off. One user is
made per connection; a name taken, or a password that will not do, is
said so, and the passwords must be typed again. A new-user account cannot
sign in on the web site. If an admin changes its type while someone is on
the sign-up screen, it makes no user, and the session ends.

The activity viewer shows who each session logged in as; so does the
chat. Someone on the chat screen in two sessions is
listed there once.

On the calendar, a month is shown with the selected day's events beside it. Move the cursor onto a day and press
`Enter` to select it.

- `PF7` / `PF8`: previous / next month
- `PF4`: back to today
- `PF3`: back to the dashboard

The calendar reads each month from Google as it is shown and reuses it for
`-agenda-refresh`, separately from the today-and-tomorrow agenda. A user
with no calendar connected sees the month and the clocks, with a note to
connect one in `settings`.

The task screen lists every open task, whatever its tags. Type `X` in the
`S` column beside tasks, then press `PF5` to move them or `PF6` to archive
them; a confirmation lists them, and `PF4` does it. Marks are kept while
paging. Typing `X` moves the cursor on to the task's title.

- `PF4`: add a task
- `PF5`: move the marked tasks to another Trello list, after confirmation
- `PF6`: archive the marked tasks, after confirmation
- `PF7` / `PF8`: previous / next page
- `PF9`: show the cards of any of your Trello lists, including those not on
  the task list, on a task screen of their own
- `PF10` / `PF11`: move the task marked (or, with none marked, the one the
  cursor is on) up / down one place in its Trello list
- type over a title, and press any key: rename the task, after confirmation
- `Enter`: keep the marks typed
- `PF4` (on a confirmation): archive, or rename
- `PF3`: back (from a confirmation, to the list with the marks, and titles
  typed, kept)

To rename tasks, type over their titles; whatever key is pressed then, a
confirmation lists each task with its new title. `PF4` renames them,
stopping at the first that fails, which stays typed; `PF3` goes back to
change what was typed; `PF12` discards it. A title cannot be blanked (to
finish a task, archive it). A title too long to fit is shown cut short;
typing over it renames the task to what is typed.

`PF10` and `PF11` move one task within its own Trello list, past the task
before or after it there: on the task screen, which lists several lists'
tasks in turn, tasks of other lists in between are passed over. The cursor
follows the task. It cannot go above the first of its list or below the
last; to move it to another list, use `PF5`.

`PF4` adds a task: type its title, and the number of the Trello board and
list to put it on, from those the user chose (with only one, it is picked
already), then press `Enter`. A card is added at the bottom of that list, and
the task screen comes back saying which task number it got; marks typed
before `PF4` are kept. If the card cannot be added, the screen says why.
`PF3` goes back without adding.

`PF5` moves the marked tasks: it lists every open list on every Trello
board you can see, not only those your tasks come from (those show their
tag), a page at a time (`PF7` / `PF8`). Type any character beside one and
press `Enter`; a confirmation lists the tasks and the list, and `PF4` moves
their cards to the bottom of it, which may be on another board, then goes
back to the task screen saying how many moved. A task moved to one of your
lists stays on the task screen, with that list's tag; moved anywhere else,
it leaves. `PF3` on the confirmation picks another list; on the lists, it
goes back with nothing moved. Moving stops at the first task that fails;
it stays marked, to try again.

`PF9` shows other lists: it lists every open list on every Trello board you
can see, as `PF5` does (those your tasks come from show their tag), a page
at a time. Type any character beside one and press `Enter` to show its open
cards on a task screen of their own (TRELLO LIST), numbered from 1; the
heading says whether they are on your task list. It works as the task
screen does: mark, move, archive, rename, reorder and add (`PF4` adds to
the bottom of that list), with the same keys. Its cards are read again
after every change, and once a minute. `PF3` goes back to the lists, and
`PF3` there to the task screen as it was left, with any marks kept; `PF9`
also goes back to the lists.

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
to the admin user (the admin called `admin`, else the first admin) when the server starts, which it logs. `PF6` unchecks every item, after
confirmation, to start the list over. The first screen lists the
checklists, with how many of each one's items are done: green when they
all are, red when some are not.

On the list, each checklist's row has an `S` column, to select it, a `*`
column, to star it, then its name. On a checklist, each item's row has an
`X` column, to check it off, then the item. Names and items can be typed
over.

- on the list, type a name on the blank row after the last checklist and
  press `Enter` to add it; the row has a `*` column too: type in it to add
  the checklist starred (it does nothing without a name beside it). A full
  last page is followed by a page with just the blank row;
- on a checklist, type items on the blank rows that fill out the last page
  after the last one, and press `Enter` to add them, in order (rows left
  blank are skipped); the cursor goes to the first blank row after them,
  to add more. A full last page is followed by a page of blank rows;
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

The checklists are kept in `-checklist-db`, a SQLite database (in WAL
mode, so it comes with `-wal` and `-shm` files beside it while the server
runs, readable only by its owner). Each change is one transaction, and the
checklists are read on every redraw, so every session of a user sees the
same ones. Its tables:

- `checklists`: `id`, `owner` (the user's ID in `-users-file`), `name`,
  `active` (starred), `position`;
- `items`: `id`, `checklist` (its checklist's `id`; removing the checklist
  removes its items), `text`, `done`, `position`;
- `meta`: `last_id`, the highest ID given, so that none is given twice.

Checklists kept as JSON, as they were before, in `-checklist-json` are
imported once at startup, IDs and all, and the file is renamed with
`.imported` added. If any of their IDs is already in the database, the
server refuses to start, saying so, and leaves the file as it is.

To look at or back up the database while the server runs, use `sqlite3`
(`.backup` makes a consistent copy); copying the `.db` file alone may miss
changes still in its `-wal` file.

A checklist's heading says how many other sessions of this server (its
user's, logged in elsewhere) have it open too, e.g. `(1 other session viewing)`. Like the rest of the screen, it
is as of the last key pressed.

IDs are unique across the file. Changes are made by ID, and only to what
was typed over, so two sessions changing the same checklist do not undo
each other's work, except where both typed over the same entry.

## Where the data comes from

### Busy light

The server drives the Luxafor flags attached to its machine itself, with
go-busy-indicator's logic brought in (`busy-indicator` is no longer needed,
and should not run beside it, or the two will fight over the flag). The
light is lit by the users marked with `Y` under `Flag` in the user editor:

- **red** from two minutes before one of their meetings to two minutes
  after, by their calendar's agenda (see [Agenda](#agenda)): all-day and
  out-of-office events do not count, nor do meetings of more than four hours
  (day-blockers) or under two minutes (spam);
- set by hand by any of those users: `busy` (`red`, `PF1`) forces it red,
  `green` forces it green, until another of these; `off` (`PF2`) turns it
  off until the meetings under way end. On `-control-port`, `KEY b`,
  `KEY g`, `KEY o` and `KEY .` do the same (`.` decides afresh), as
  go-busy-indicator's `busy` command sends them; anything else is ignored.

Every other user has a **busy state of their own**, by the same rules from
their own calendar, and set by their own `busy`, `green` and `off` (`PF1`,
`PF2`), which touch nothing but it. It is what their banner and title row
show; it is kept, in memory, across their sessions until the server
restarts. Whether a user controls the light is read afresh at every redraw,
so a change in the user editor applies at once.

With no user marked, the light is kept off, and its controllers' banner
with it. The
light is decided every 15 seconds, and at every key; the calendars are read
every `-agenda-refresh`, so a meeting added to one shows within that. The
flag is driven dimly (red is `20,0,0`), and an unchanged color is sent twice
and then not again until it changes. A flag can be plugged in and out while
the server runs; with none, the server logs it once, and the banner's line,
for users who control the light, says `(busy light: no Luxafor flag
attached)`. `-externalrgb` runs a command for another light at each change,
at full brightness (`255 0 0` red, `0 255 0` green, `250 0 250` off),
given ten seconds.

**A Stream Deck Mini** (six keys, the original or the MK.2) attached to the
server's machine is the light's buttons, as the `sd` program's were, but
setting the light directly rather than running `busy.raku`:

| Key | Sets the light |
|---|---|
| Busy | red, as `busy` / `PF1` |
| Free | off until the meetings under way end, as `off` / `PF2` |
| Green | green, as `green` |
| (blank) | |
| (blank) | (was Agenda) |
| Remind | nothing: each press turns its picture between "no reminder" and "reminder", a reminder to oneself |

The keys show `sd`'s pictures, built into the server, with their labels
under them (in Go Mono, for Liberation Mono). The deck is set to full
brightness when found, can be plugged in and out while the server runs
(it is looked for every 5 seconds, and its absence logged once), and is
blanked when the server stops. Remind's picture starts as "no reminder"
each time the deck is found.

On Linux the flag and the deck need udev rules, for the server's user to
open them:

    SUBSYSTEM=="hidraw", ATTRS{idVendor}=="04d8", ATTRS{idProduct}=="f372", MODE="0660", GROUP="plugdev"
    SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0fd9", ATTRS{idProduct}=="0063", MODE="0660", GROUP="plugdev"
    SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0fd9", ATTRS{idProduct}=="0090", MODE="0660", GROUP="plugdev"

The `sd` program should not run beside the server, or both will drive the
deck.

Every screen but the login screen also shows the state in its title row:
black on red across the whole row while the light is red, black on green
while it is green, and unchanged while it is off or no one controls it.
(On the dashboard it runs on into the busy banner below, the same color,
with no gap.) A restricted user's title row is never colored. A screen
that does not redraw on a timer shows a change of state at the next key.
For every user, the line under the banner is their own next meeting, from
their calendar alone, and is blank for a user with none.

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
which must be served and have its address set. Option `2` on the `settings`
screen shows the steps:

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
withdrawn, or the client changed), the agenda says to reconnect in
`settings`.

Cancelled events and events you declined are left out, as the busy indicator
does.

A meeting others were invited to, every one of whom declined, has a red
`X` before its title (after its calendar alias, if it has one: `[work] X
Standup`) wherever it is shown: the agenda, the next meeting or meetings under way
below the banner, and the calendar's day list. Rooms and other resources
are not counted as people. When Google leaves the guest list out (for an
event with very many guests), there is no telling, and nothing is marked.
A meeting on more than one of your calendars is marked only if every copy
is. It still counts as a meeting for the busy light.

The token refresh is go-busy-indicator's `gauth` package, imported rather
than copied; the authorization flow, the calendar list and event reading
are in `internal/google`.

`-agenda-file` takes a JSON list, shown to every user in place of their
Google calendars, for trying the dashboard without Google:

```json
[{"summary": "Standup", "start": "2026-09-27T09:00:00-06:00",
  "end": "2026-09-27T09:15:00-06:00", "all_day": false, "calendar": "work",
  "others_declined": false}]
```

`calendar` is optional; it is shown in brackets as an alias is.
`others_declined`, optional, marks the meeting with the red `X`.

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
Option `3` on the `settings` screen shows the steps:

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
that a flood from elsewhere cannot lock out an admin connecting from
there. Refusals are
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
- `internal/busy`: the busy light's logic and control port, and each
  other user's busy state.
- `internal/luxafor`: the Luxafor flag's USB driver.
- `internal/streamdeck`: the Stream Deck Mini's USB driver, its keys'
  images, and the busy light's buttons.
- `internal/agenda`: each user's periodically refreshed calendar cache.
- `internal/google`: connecting a Google calendar: the OAuth flow and the
  calendar list.
- `internal/web`: the web pages: home, privacy policy and terms.
- `internal/tasks`: the tasks from Trello, cached, and the task program's
  configuration.
- `internal/checklist`: the checklist file.

Telnet/TN3270E negotiation is
[`github.com/jmaslak/go-3270e`](../go-3270e).
