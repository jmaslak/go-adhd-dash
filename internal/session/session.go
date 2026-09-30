// Package session drives one TN3270 client connection: telnet negotiation,
// then a dashboard that redraws itself on a timer until the client quits,
// with a calendar screen, a task screen, a calculator and checklists a key
// or a command away.
package session

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tn3270e "github.com/jmaslak/go-3270e"
	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// negotiateTimeout bounds telnet and TN3270E negotiation.
const negotiateTimeout = 30 * time.Second

// nextSessionID numbers connections for the log and for LU names.
var nextSessionID atomic.Uint64

// Config holds what every session shares.
type Config struct {
	// Tasks holds the tasks, fetched from Trello; nil when there are none.
	Tasks *tasks.Cache

	// Busy supplies the busy indicator's status; nil when none is
	// configured.
	Busy busy.Source

	// BusyControl sends keys to the busy indicator; nil when its control
	// port is not configured.
	BusyControl *busy.Control

	// Archiver archives tasks from the task screen; nil when archiving is
	// not available.
	Archiver TaskArchiver

	// Adder adds tasks from the add-task screen; nil when adding is not
	// available.
	Adder TaskAdder

	// Agenda holds the calendar; nil when no calendar is configured.
	Agenda *agenda.Cache

	// Checklists keeps the checklists.
	Checklists *checklist.Store

	// Shutdown stops the server from the admin menu; nil when it cannot be
	// stopped from there.
	Shutdown *Shutdown

	// Users keeps the user database; nil for none.
	Users *users.Store

	// Audit is the audit log of logins, logouts and disconnections; nil
	// for none.
	Audit *audit.Log

	// Chat holds the chat's messages; nil for no chat.
	Chat *Chat

	// Activity tracks what each session is doing, for the activity viewer;
	// nil to not track it.
	Activity *Activity

	// Viewers tracks which checklist each session has open; nil to not
	// count them.
	Viewers *Viewers

	// Limits caps the connections open at once; nil for no caps.
	Limits *ConnLimiter

	// LoginTimeout is how long the login screen waits; zero for
	// defaultLoginTimeout.
	LoginTimeout time.Duration

	// Refresh is how often an idle screen is redrawn.
	Refresh time.Duration

	// AgendaRefresh is how long the calendar screen reuses a month it has
	// read before reading it again.
	AgendaRefresh time.Duration
}

// isTimeout reports whether err is the read deadline expiring, meaning it is
// time to redraw.
func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// Handle runs the dashboard on one connection until the client disconnects
// or presses PF3, or the server shuts down.
func Handle(rawConn net.Conn, cfg Config) {
	defer rawConn.Close() //nolint:errcheck
	// The caps come first, before any work is done for the connection.
	release := cfg.Limits.admit(rawConn.RemoteAddr())
	if release == nil {
		return
	}
	defer release()
	if !cfg.Shutdown.add(rawConn) {
		return
	}
	defer cfg.Shutdown.remove(rawConn)

	sessionID := nextSessionID.Add(1)
	cfg.Activity.add(sessionID, rawConn, time.Now())
	defer cfg.Activity.remove(sessionID)
	local := isLocal(rawConn.RemoteAddr())
	// Negotiation reads what the client sends with deadlines of its own,
	// which it clears as it goes; this bounds the whole of it, so that a
	// client cannot hold a connection by stalling partway through.
	watchdog := time.AfterFunc(negotiateTimeout, func() { _ = rawConn.Close() })
	neg, err := tn3270e.NegotiateLU(rawConn, func(requested string) (string, error) {
		lu, err := chooseLU(requested, local, sessionID)
		if err != nil {
			log.Printf("session %d (%s): refused LU %q: %v", sessionID, rawConn.RemoteAddr(), requested, err)
		}
		return lu, err
	})
	if !watchdog.Stop() {
		log.Printf("session %d (%s): negotiation took over %v", sessionID, rawConn.RemoteAddr(), negotiateTimeout)
		return
	}
	if err != nil {
		log.Printf("session %d (%s): %v", sessionID, rawConn.RemoteAddr(), err)
		return
	}
	conn, devinfo := neg.Conn, neg.DevInfo
	log.Printf("session %d (%s): connected, LU %s", sessionID, rawConn.RemoteAddr(), neg.LUName)
	defer log.Printf("session %d (%s): disconnected", sessionID, rawConn.RemoteAddr())
	defer cfg.Viewers.Set(sessionID, 0)
	// There is one console: a new one boots any other.
	console := neg.LUName == consoleLU
	if console {
		for _, id := range cfg.Activity.claimLU(sessionID, consoleLU, consoleBootedFarewell) {
			log.Printf("session %d (%s): took over the console from session %d", sessionID, rawConn.RemoteAddr(), id)
		}
	} else {
		cfg.Activity.update(sessionID, func(s *SessionActivity) { s.LU = neg.LUName })
	}

	rows, cols := devinfo.AltDimensions()
	// Every screen's text is made safe to show in the terminal's code page
	// before it is sent (see sanitizeScreen). go3270 falls back to 1047
	// when the terminal reports none, and so does this.
	cp := devinfo.Codepage()
	if cp == nil {
		cp = go3270.Codepage1047()
	}
	page := 0
	autoRefresh := true
	mode := modeDashboard
	// Every session logs in first but the console. user is who logged
	// in, nil for the console.
	var user *users.User
	var login loginState
	if !console {
		timeout := cfg.LoginTimeout
		if timeout <= 0 {
			timeout = defaultLoginTimeout
		}
		mode, login.expires = modeLogin, time.Now().Add(timeout)
	}

	// The audit log records each login, and how each session logged in
	// (the console included) ended: logged out, or disconnected, and why.
	// Every way out sets loggedOut or endReason first.
	ip := rawConn.RemoteAddr().String()
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	auditFields := func(name string, extra ...audit.Field) []audit.Field {
		return append([]audit.Field{
			audit.F("user", name), audit.F("lu", neg.LUName), audit.F("ip", ip), audit.F("session", strconv.FormatUint(sessionID, 10)),
		}, extra...)
	}
	loggedOut, endReason := false, "connection lost"
	defer func() {
		switch {
		case user == nil && !console:
		case loggedOut:
			cfg.Audit.Record(audit.Logout, auditFields(auditName(user))...)
		default:
			cfg.Audit.Record(audit.Disconnect, auditFields(auditName(user), audit.F("reason", endReason))...)
		}
	}()
	if console {
		cfg.Audit.Record(audit.Login, auditFields(auditName(nil))...)
	}
	// bye leaves text on the terminal as the session disconnects.
	bye := func(text string) {
		_, _ = go3270.ShowScreenOpts(sanitizeScreen(buildFarewell(cols, time.Now(), text), cp), nil, conn, go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: cp, NoResponse: true,
		})
	}
	var cal calendarState
	var tp taskPageState
	var cl checklistState
	var at addTaskState
	var act activityState
	var ch chatState
	var us usersState
	defer cfg.Chat.unwatch(sessionID)
	var calc calcState             // kept while the session lasts, as a calculator's stack is
	var allTasks []tasks.Task      // the task screen's tasks, as last shown
	var sessions []SessionActivity // the activity viewer's sessions, as last shown
	var checklistAt map[int]int    // the dashboard's checklists, as last shown, by row
	message := ""                  // shown on the command line until the next key
	messageOK := false             // the message reports something done (on the admin menu)
	timedOut := false              // the last screen was left for the refresh interval
	logf := func(format string, args ...any) {
		log.Printf("session %d (%s): %s", sessionID, rawConn.RemoteAddr(), fmt.Sprintf(format, args...))
	}

	// runCommand does what a command typed on the dashboard or the command
	// list asks, or what the PF key doing the same asks, reporting whether
	// it is to disconnect. Unless the command goes to another screen, it
	// comes back to the dashboard.
	runCommand := func(typed string) (quit bool) {
		c, ok := lookupCommand(typed)
		if !ok {
			message = fmt.Sprintf("Unknown command %q; type help.", strings.TrimSpace(typed))
			return false
		}
		mode = modeDashboard
		switch c.name {
		case "tasks":
			mode, tp = modeTasks, taskPageState{}
		case "cal":
			mode = modeCalendar
			cal.goTo(time.Now())
		case "calc", "dbm":
			mode, calc.dbm = modeCalc, c.name == "dbm"
		case "checklist":
			mode, cl = modeChecklist, checklistState{}
		case "busy", "green", "off":
			if cfg.BusyControl == nil {
				message = "No busy indicator control port (-busy-control)."
			} else {
				message = sendBusyKey(cfg, rune(c.name[0]))
			}
		case "auto":
			autoRefresh = !autoRefresh
		case "up":
			page = max(page-1, 0)
		case "down":
			page++ // the dashboard keeps it to the pages there are
		case "help":
			mode = modeHelp
		case "admin":
			if user != nil && !user.Admin {
				message = "The admin menu is for admins."
				break
			}
			mode = modeAdmin
		case "chat":
			mode, ch = modeChat, chatState{}
		case "exit":
			loggedOut = true
			return true
		}
		return false
	}

	for {
		now := time.Now()
		var screen go3270.Screen
		var totalPages, cursorRow, cursorCol int
		// A timed redraw would wipe marks typed on the task screen, a line
		// typed on the calculator, or changes typed on a checklist, before
		// they were sent, so those only ever redraw when a key is pressed.
		redrawOnTimer := autoRefresh
		viewing := 0
		if mode == modeChecklist {
			viewing = cl.open
		}
		cl.others = cfg.Viewers.Set(sessionID, viewing)
		setScreen := func() {
			name := screenName(mode, &cl, &calc)
			cfg.Activity.update(sessionID, func(s *SessionActivity) { s.Screen = name })
		}
		setScreen() // before building, so the activity viewer shows its own
		// Before the read deadline is set, so that a wake meant for the chat
		// screen cannot cut short the wait for a key on another.
		if mode == modeChat {
			cfg.Chat.watch(sessionID, neg.LUName, rawConn)
		} else {
			cfg.Chat.unwatch(sessionID)
		}
		switch mode {
		case modeLogin:
			screen, cursorRow, cursorCol = buildLogin(rows, cols, now, neg.LUName, &login)
			redrawOnTimer = false
		case modeChat:
			// A timed redraw, or one woken by a message posted, writes over
			// the screen without erasing it, so what is typed survives.
			screen, cursorRow, cursorCol = buildChat(rows, cols, now, cfg.Chat, neg.LUName, &ch)
		case modeActivity:
			sessions = cfg.Activity.List()
			screen, totalPages, cursorRow, cursorCol = buildActivity(rows, cols, now, sessions, sessionID, &act)
			redrawOnTimer = false
		case modeTerminateConfirm:
			screen, cursorRow, cursorCol = buildTerminateConfirm(rows, cols, now, sessionID, &act), rows-1, 0
			redrawOnTimer = false
		case modeCalendar:
			cal.load(cfg.Agenda, cfg.AgendaRefresh, now)
			screen, cursorRow, cursorCol = buildCalendar(rows, cols, cal.view(now, autoRefresh, cfg.Agenda != nil))
		case modeTasks:
			snap := cfg.Tasks.Snapshot()
			allTasks = snap.Tasks
			screen, tp.page, totalPages, cursorRow, cursorCol = buildTaskList(rows, cols, now, snap, &tp)
			redrawOnTimer = false
		case modeConfirm:
			screen, cursorRow, cursorCol = buildArchiveConfirm(rows, cols, now, tp.confirming), rows-1, 0
			redrawOnTimer = false
		case modeCalc:
			screen, cursorRow, cursorCol = buildCalc(rows, cols, now, &calc)
			redrawOnTimer = false
		case modeChecklist:
			lists, err := cfg.Checklists.Load()
			screen, cursorRow, cursorCol = buildChecklist(rows, cols, now, lists, err, &cl)
			redrawOnTimer = false
		case modeHelp:
			screen, cursorRow, cursorCol = buildHelp(rows, cols, now, message)
			redrawOnTimer = false
		case modeAddTask:
			screen, cursorRow, cursorCol = buildAddTask(rows, cols, now, cfg.Adder, &at)
			redrawOnTimer = false
		case modeAdmin:
			screen, cursorRow, cursorCol = buildAdmin(rows, cols, now, message, messageOK, cfg.Shutdown.Sessions())
			redrawOnTimer = false
		case modeUsers:
			var list []users.User
			err := errors.New("there is no user database")
			if cfg.Users != nil {
				list, _, err = cfg.Users.Load()
			}
			screen, cursorRow, cursorCol = buildUsers(rows, cols, now, list, err, &us)
			redrawOnTimer = false
		case modeClearChatConfirm:
			screen, cursorRow, cursorCol = buildClearChatConfirm(rows, cols, now, cfg.Chat.count()), rows-1, 0
			redrawOnTimer = false
		case modeShutdownConfirm:
			screen, cursorRow, cursorCol = buildShutdownConfirm(rows, cols, now, cfg.Shutdown.Sessions()), rows-1, 0
			redrawOnTimer = false
		default:
			v := gather(cfg, now)
			v.AutoRefresh, v.BusyControl, v.Message = autoRefresh, cfg.BusyControl != nil, message
			screen, page, totalPages, checklistAt = buildDashboard(rows, cols, v, page, neg.LUName)
			cursorRow, cursorCol = dashboardCommandRow(rows), commandInputCol+1
		}

		// A timed redraw of the dashboard or calendar writes over the screen
		// without erasing it, and leaves the command field and the cursor
		// alone, so that a command half typed, or the cursor moved to a day
		// not yet picked, survives it.
		opts := go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: cp, CursorRow: cursorRow, CursorCol: cursorCol,
		}
		if timedOut && (mode == modeDashboard || mode == modeCalendar || mode == modeChat) {
			screen, opts.NoClear = fillScreen(screen, rows, cols), true
		}

		// Without a redraw on a timer there is no deadline, so the screen
		// stays as drawn until a key is pressed.
		deadline := time.Time{}
		switch {
		case mode == modeLogin:
			deadline = login.expires
		case redrawOnTimer:
			deadline = time.Now().Add(cfg.Refresh)
		}
		_ = conn.SetReadDeadline(deadline)

		// Checked after the deadline is set, so that a shutdown requested
		// since either is seen here or ends the wait for a key at once.
		// A terminated session is the same.
		switch {
		case cfg.Shutdown.Requested():
			endReason = "server shut down"
			bye(shutdownFarewell)
			return
		case cfg.Activity.terminated(sessionID):
			logf("terminated")
			farewell := cfg.Activity.farewellFor(sessionID)
			endReason = "terminated by an administrator"
			if farewell == consoleBootedFarewell {
				endReason = "another console connected"
			}
			bye(farewell)
			return
		case mode == modeLogin && !time.Now().Before(login.expires):
			logf("login timed out")
			bye("The login timed out. Goodbye.")
			return
		}
		setScreen() // and after, once a checklist just opened has its name
		// A message posted since the chat was drawn, and before the deadline
		// was set, did not wake this session: draw it now.
		if mode == modeChat && cfg.Chat.latest() != ch.shown {
			continue
		}
		resp, err := go3270.ShowScreenOpts(sanitizeScreen(screen, cp), nil, conn, opts)
		if timedOut = isTimeout(err); timedOut {
			continue
		}
		if err != nil {
			log.Printf("session %d (%s): %v", sessionID, rawConn.RemoteAddr(), err)
			endReason = "connection lost: " + err.Error()
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		message, messageOK = "", false
		cfg.Activity.update(sessionID, func(s *SessionActivity) { s.LastKey = time.Now() })

		switch mode {
		case modeCalendar:
			switch resp.AID {
			case go3270.AIDPF3:
				mode = modeDashboard
			case go3270.AIDPF4:
				cal.goTo(time.Now())
			case go3270.AIDPF5:
				autoRefresh = !autoRefresh
			case go3270.AIDPF7:
				cal.changeMonth(-1, time.Now())
			case go3270.AIDPF8:
				cal.changeMonth(1, time.Now())
			case go3270.AIDEnter:
				if day, ok := calendarDayAt(cal.month, resp.Row, resp.Col); ok {
					cal.selected = day
				}
			}
		case modeTasks:
			switch tp.handleList(resp, allTasks, totalPages, cfg.Archiver) {
			case taskListLeave:
				mode = modeDashboard
			case taskListConfirm:
				mode = modeConfirm
			case taskListAdd:
				mode, at = modeAddTask, addTaskState{}
			}
		case modeConfirm:
			if tp.handleConfirm(resp, cfg.Archiver, logf) {
				mode = modeTasks
			}
		case modeCalc:
			if calc.handle(resp) {
				if user != nil && user.Restricted {
					logf("logged off")
					loggedOut = true
					bye("Logged off. Goodbye.")
					return
				}
				mode = modeDashboard
			}
		case modeChecklist:
			if cl.handle(resp, cfg.Checklists) {
				mode = modeDashboard
			}
		case modeAddTask:
			if leave, added := at.handle(resp, cfg.Adder); leave {
				mode = modeTasks
				if added != "" {
					tp.message, tp.isError = added, false
					logf("%s", added)
				}
			}
		case modeAdmin:
			switch action, msg := adminChoice(resp); {
			case action == adminLeave:
				mode = modeDashboard
			case action == adminShutdown && cfg.Shutdown == nil:
				message = "This server cannot be shut down from here."
			case action == adminShutdown:
				mode = modeShutdownConfirm
			case action == adminUsers && cfg.Users == nil:
				message = "There is no user database."
			case action == adminUsers:
				mode, us = modeUsers, usersState{}
			case action == adminClearChat:
				mode = modeClearChatConfirm
			case action == adminActivity:
				mode, act = modeActivity, activityState{}
			default:
				message = msg
			}
		case modeActivity:
			switch leave, confirm := act.handleList(resp, sessions, totalPages); {
			case leave:
				mode = modeAdmin
			case confirm:
				mode = modeTerminateConfirm
			}
		case modeLogin:
			failures := login.failures
			u, quit, farewell := login.handle(resp, cfg.Users, logf)
			if login.failures > failures {
				cfg.Audit.Record(audit.LoginFailed, auditFields(login.name, audit.F("try", strconv.Itoa(login.failures)), audit.F("reason", login.failReason))...)
			}
			if u != nil {
				cfg.Audit.Record(audit.Login, auditFields(u.Name)...)
			}
			switch {
			case quit:
				bye(farewell)
				return
			case u != nil:
				user, mode = u, modeDashboard
				cfg.Activity.update(sessionID, func(s *SessionActivity) { s.User = u.Name })
				// A restricted user has the calculator, in either mode, and
				// nothing else.
				if u.Restricted {
					mode, calc = modeCalc, calcState{logOff: true}
				}
			}
		case modeChat:
			if ch.handle(resp, cfg.Chat, sessionID, neg.LUName, rows) {
				mode = modeDashboard
			}
		case modeTerminateConfirm:
			if act.handleConfirm(resp, cfg.Activity, logf) {
				mode = modeActivity
			}
		case modeUsers:
			if us.handle(resp, cfg.Users, logf) {
				mode = modeAdmin
			}
		case modeClearChatConfirm:
			switch resp.AID {
			case go3270.AIDPF3:
				mode = modeAdmin
			case go3270.AIDPF4:
				n := cfg.Chat.clear()
				logf("cleared the chat, %d messages", n)
				mode, message, messageOK = modeAdmin, "Cleared the chat ("+countText(n, "message", 0, 1)+").", true
			}
		case modeShutdownConfirm:
			switch resp.AID {
			case go3270.AIDPF3:
				mode = modeAdmin
			case go3270.AIDPF4:
				logf("shutdown requested")
				cfg.Shutdown.Request() // the next redraw says goodbye
			}
		case modeHelp:
			switch typed := resp.Values[commandField]; {
			case resp.AID == go3270.AIDPF3:
				mode = modeDashboard
			case resp.AID == go3270.AIDEnter && typed != "":
				if runCommand(typed) {
					return
				}
			}
		default:
			// Each PF key does what its command does; Enter runs the
			// command typed, and with none typed opens the checklist the
			// cursor is on, or like Clear and anything else, just redraws.
			typed := resp.Values[commandField]
			if resp.AID == go3270.AIDEnter && typed == "" {
				if id, ok := checklistAt[resp.Row]; ok {
					mode, cl = modeChecklist, checklistState{open: id}
				}
				break
			}
			if resp.AID != go3270.AIDEnter {
				typed = pfCommand(resp.AID, cfg.BusyControl != nil)
			}
			if typed != "" && runCommand(typed) {
				return
			}
		}
	}
}

// The screens a session can show.
const (
	modeDashboard = iota
	modeCalendar
	modeTasks
	modeConfirm
	modeCalc
	modeChecklist
	modeHelp
	modeAdmin
	modeShutdownConfirm
	modeAddTask
	modeActivity
	modeTerminateConfirm
	modeChat
	modeClearChatConfirm
	modeUsers
	modeLogin
)

// pfCommand is the command a PF key on the dashboard runs, or "" for none.
// The busy indicator's keys do nothing without its control port, where they
// are not offered.
func pfCommand(aid go3270.AID, busyControl bool) string {
	for _, c := range commands {
		if c.key != "" && c.key != "Enter" && pfKeys[c.key] == aid {
			if (c.name == "busy" || c.name == "off") && !busyControl {
				return ""
			}
			return c.name
		}
	}
	return ""
}

// pfKeys are the PF keys commands are given, by name.
var pfKeys = map[string]go3270.AID{
	"PF1": go3270.AIDPF1, "PF2": go3270.AIDPF2, "PF3": go3270.AIDPF3, "PF4": go3270.AIDPF4,
	"PF5": go3270.AIDPF5, "PF7": go3270.AIDPF7, "PF8": go3270.AIDPF8, "PF9": go3270.AIDPF9,
	"PF10": go3270.AIDPF10, "PF11": go3270.AIDPF11,
}

// busyKeyWait is how long to wait for the indicator's feed to report the
// change a key caused, so that the redraw after it shows the new state.
const busyKeyWait = time.Second

// sendBusyKey sends key to the busy indicator's control port, then waits
// for its feed to publish, as it does after every key. It returns a message
// for the screen when something went wrong, empty otherwise.
func sendBusyKey(cfg Config, key rune) string {
	if cfg.BusyControl == nil {
		return ""
	}
	var before time.Time
	if cfg.Busy != nil {
		before = cfg.Busy.Status().Updated
	}
	if err := cfg.BusyControl.Send(key); err != nil {
		return "Could not send to busy indicator: " + err.Error()
	}
	if cfg.Busy == nil {
		return ""
	}
	for deadline := time.Now().Add(busyKeyWait); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if !cfg.Busy.Status().Updated.Equal(before) {
			return ""
		}
	}
	return "No change seen from " + cfg.BusyControl.Addr + "."
}

// view is everything one redraw shows, gathered up front so that building
// the screen is a pure function.
type view struct {
	Now time.Time

	// AutoRefresh is whether the session redraws on its own, BusyControl
	// whether the busy indicator's keys are offered, and Message what the
	// last key reported; they are the session's, not gathered from a
	// source.
	AutoRefresh bool
	BusyControl bool
	Message     string

	BusyEnabled bool
	Busy        busy.Status

	AgendaEnabled bool
	Agenda        agenda.Snapshot

	// Tasks are those the dashboard shows. TasksErr is why the last fetch
	// of them failed, TasksFetched when they were last fetched (zero if
	// never: with TasksErr, there are none), and TasksLoading whether the
	// first fetch is still under way.
	Tasks        []tasks.Task
	TasksErr     error
	TasksFetched time.Time
	TasksLoading bool

	Checklists    []checklist.Checklist
	ChecklistsErr error
}

// gather reads the current state of every source.
func gather(cfg Config, now time.Time) view {
	v := view{Now: now}
	if cfg.Busy != nil {
		v.BusyEnabled = true
		v.Busy = cfg.Busy.Status()
	}
	if cfg.Agenda != nil {
		v.AgendaEnabled = true
		v.Agenda = cfg.Agenda.Snapshot()
	}

	if cfg.Checklists != nil {
		v.Checklists, v.ChecklistsErr = cfg.Checklists.Load()
	}

	snap := cfg.Tasks.Snapshot()
	v.Tasks = tasks.Visible(snap.Tasks, snap.IgnoreTags)
	v.TasksErr, v.TasksFetched, v.TasksLoading = snap.Err, snap.Fetched, snap.Loading
	return v
}
