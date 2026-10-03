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
	"slices"
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
	// TaskPool keeps each user's tasks, from their Trello lists, while in
	// use; nil for no tasks.
	TaskPool *tasks.Pool

	// TrelloBaseURL is the Trello API's address; "" for Trello's own.
	// Tests replace it.
	TrelloBaseURL string

	// Busy supplies the busy light's status; nil for none.
	Busy busy.Source

	// BusyKeys sets the busy light by hand, for the users who control it
	// (see users.User.Flag): b busy, g green, o off; nil for none.
	BusyKeys interface{ Key(rune) error }

	// Personal keeps the busy state of each user who does not control the
	// light, from their own calendar and keys, shown to them in its place;
	// nil for none, when they see the light's.
	Personal *busy.Personal

	// Agenda is a calendar shown to every user, for trying the dashboard
	// without Google (-agenda-file); nil normally, when each user sees
	// their own Google calendars', kept in Agendas.
	Agenda *agenda.Cache

	// Agendas keeps each user's Google calendars' agenda while in use; nil
	// for no Google calendars.
	Agendas *agenda.Pool

	// Checklists keeps the checklists.
	Checklists *checklist.Store

	// Shutdown stops the server from the admin menu; nil when it cannot be
	// stopped from there.
	Shutdown *Shutdown

	// Backup backs up the users and checklists from the admin menu,
	// returning the archive made; nil when it cannot.
	Backup func() (string, error)

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

	// Refresh is how often an idle screen that redraws itself is redrawn;
	// zero for every second.
	Refresh time.Duration

	// HTTPListen is where the web pages are served, for the admin to see;
	// "" when they are not.
	HTTPListen string

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
		// There is one console: claimed here, so that a client asking for
		// it while another has it is refused it, as a remote one is.
		if err == nil && lu == consoleLU && !cfg.Activity.claimLU(sessionID, consoleLU) {
			lu, err = "", errConsoleInUse
		}
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
	// The console claimed its LU name while negotiating.
	console := neg.LUName == consoleLU
	if !console {
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
	mode := modeDashboard
	// Every session logs in first but the console, which is logged in as
	// the user marked as the console's. user is who is logged in: nil until
	// then, and for a console with no user database, which has everything.
	// A console whose user cannot be read logs in like any other session.
	var user *users.User
	var login loginState
	var calc calcState // kept while the session lasts, as a calculator's stack is
	// logIn makes u the session's user. A restricted user has the
	// calculator, in either mode, and nothing else.
	logIn := func(u users.User) {
		user, mode = &u, modeDashboard
		cfg.Activity.update(sessionID, func(s *SessionActivity) { s.User = u.Name })
		if u.Restricted {
			mode, calc = modeCalc, calcState{logOff: true}
		}
	}
	consoleLoggedIn := false
	if console {
		switch u, err := consoleLogin(cfg.Users); {
		case err != nil:
			log.Printf("session %d (%s): console logs in: %v", sessionID, rawConn.RemoteAddr(), err)
		case u != nil:
			logIn(*u)
			consoleLoggedIn = true
		default:
			consoleLoggedIn = true
		}
	}
	if !consoleLoggedIn {
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
		case user == nil && !consoleLoggedIn:
		case loggedOut:
			cfg.Audit.Record(audit.Logout, auditFields(auditName(user))...)
		default:
			cfg.Audit.Record(audit.Disconnect, auditFields(auditName(user), audit.F("reason", endReason))...)
		}
	}()
	if consoleLoggedIn {
		cfg.Audit.Record(audit.Login, auditFields(auditName(user))...)
	}
	// tint colors screen's title row for the user's busy state (see
	// lightFor), on every screen but the login screen, unless the user is
	// restricted.
	tint := func(screen go3270.Screen) go3270.Screen {
		if mode == modeLogin || (user != nil && user.Restricted) {
			return screen
		}
		src, _ := cfg.lightFor(user)
		if color, ok := headerColor(src); ok {
			return tintTitle(screen, rows, cols, color)
		}
		return screen
	}
	// bye leaves text on the terminal as the session disconnects.
	bye := func(text string) {
		_, _ = go3270.ShowScreenOpts(sanitizeScreen(tint(buildFarewell(cols, time.Now(), text)), cp), nil, conn, go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: cp, NoResponse: true,
		})
	}
	var cal calendarState
	var tp taskPageState
	var cl checklistState
	var at addTaskState
	var mv moveTaskState
	var act activityState
	var ch chatState
	var us usersState
	var gs googleState
	var gc googleClientState
	var tr trelloState
	var pw passwordState
	var tk trelloKeyState
	var site siteState
	defer cfg.Chat.unwatch(sessionID)
	var lights lightWatch // the busy state the dashboard shows, to redraw when it changes
	defer lights.stop()
	var drawnBusy busy.Status      // that state, as the dashboard last drew it
	shownMode := -1                // the screen last sent to the terminal
	var allTasks []tasks.Task      // the task screen's tasks, as last shown
	var sessions []SessionActivity // the activity viewer's sessions, as last shown
	var checklistAt map[int]int    // the dashboard's checklists, as last shown, by row
	message := ""                  // shown on the command line until the next key
	messageOK := false             // the message reports something done (on the admin menu)
	timedOut := false              // the last screen was left for the refresh interval
	logf := func(format string, args ...any) {
		log.Printf("session %d (%s): %s", sessionID, rawConn.RemoteAddr(), fmt.Sprintf(format, args...))
	}

	// settingsBack is the screen the Google calendar, Trello and password
	// screens go back to: the settings screen, if opened from it, else the
	// dashboard.
	settingsBack := modeDashboard

	// openSetting opens the screen of the user's own setting command
	// (google, trello or password), or says why it cannot.
	openSetting := func(command string) (why string) {
		id := 0
		if user != nil {
			id = user.ID
		}
		switch command {
		case "google":
			g, why := startGoogle(cfg.Users, id, cfg.HTTPListen != "")
			if why != "" {
				return why
			}
			mode, gs = modeGoogle, g
		case "password":
			if user == nil || cfg.Users == nil {
				return "There is no user database to keep a password in."
			}
			mode, pw = modePassword, passwordState{}
		case "trello":
			t, why := startTrello(cfg.Users, id, cfg.HTTPListen != "", cfg.TrelloBaseURL)
			if why != "" {
				return why
			}
			mode, tr = modeTrello, t
		}
		return ""
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
			if cfg.tasksFor(user) == nil {
				message = "No Trello lists are linked; link them in SETTINGS."
				break
			}
			mode, tp = modeTasks, taskPageState{}
		case "cal":
			mode = modeCalendar
			cal.goTo(time.Now())
		case "calc", "dbm":
			mode, calc.dbm = modeCalc, c.name == "dbm"
		case "checklist":
			mode, cl = modeChecklist, checklistState{owner: ownerOf(user)}
		case "busy", "green", "off":
			if _, keys := cfg.lightFor(user); keys == nil {
				message = "There is no busy light."
			} else if err := keys.Key(rune(c.name[0])); err != nil {
				message = "Could not set the busy light: " + err.Error()
			}
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
		case "settings":
			if user == nil || cfg.Users == nil {
				message = "There is no user database to keep your settings in."
				break
			}
			mode = modeSettings
		case "google", "password", "trello":
			settingsBack = modeDashboard
			message = openSetting(c.name)
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
		redrawOnTimer := true
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
			cfg.Chat.watch(sessionID, chatName(user, neg.LUName), rawConn)
		} else {
			cfg.Chat.unwatch(sessionID)
		}
		// So too the dashboard, for a change of the user's busy state, which
		// its title row and banner show: it is redrawn at once, as a timed
		// redraw is, over the screen, keeping a command half typed.
		if mode == modeDashboard {
			src, _ := cfg.lightFor(user)
			lights.follow(src, func() { _ = rawConn.SetReadDeadline(time.Now()) })
		} else {
			lights.stop()
		}
		switch mode {
		case modeLogin:
			screen, cursorRow, cursorCol = buildLogin(rows, cols, now, neg.LUName, &login)
			redrawOnTimer = false
		case modeChat:
			// A timed redraw, or one woken by a message posted, writes over
			// the screen without erasing it, so what is typed survives.
			screen, cursorRow, cursorCol = buildChat(rows, cols, now, cfg.Chat, chatName(user, neg.LUName), &ch)
		case modeActivity:
			// Redrawn on the timer like the dashboard, keeping what has
			// been typed, and each session in its row so that marks typed
			// stay beside theirs.
			if timedOut {
				sessions = keepRows(sessions, cfg.Activity.List())
			} else {
				sessions = cfg.Activity.List()
			}
			screen, totalPages, cursorRow, cursorCol = buildActivity(rows, cols, now, sessions, sessionID, &act)
		case modeTerminateConfirm:
			screen, cursorRow, cursorCol = buildTerminateConfirm(rows, cols, now, sessionID, &act), rows-1, 0
			redrawOnTimer = false
		case modeCalendar:
			cache := cfg.agendaFor(user)
			cal.load(cache, cfg.AgendaRefresh, now)
			screen, cursorRow, cursorCol = buildCalendar(rows, cols, cal.view(now, cache != nil))
		case modeTasks:
			snap := cfg.tasksFor(user).Snapshot()
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
			screen, cursorRow, cursorCol = buildAddTask(rows, cols, now, taskAdder(cfg.tasksFor(user)), &at)
			redrawOnTimer = false
		case modeMoveTask:
			screen, cursorRow, cursorCol = buildMoveTask(rows, cols, now, &mv)
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
		case modeGoogle:
			screen, cursorRow, cursorCol = gs.build(rows, cols, now)
			redrawOnTimer = false
		case modeGoogleClient:
			list, client, err := cfg.Users.GoogleClient()
			settings, siteErr := cfg.Users.Site()
			screen, cursorRow, cursorCol = buildGoogleClient(rows, cols, now, list, client, settings, errors.Join(err, siteErr), &gc)
			redrawOnTimer = false
		case modeTrello:
			screen, cursorRow, cursorCol = tr.build(rows, cols, now)
			redrawOnTimer = false
		case modePassword:
			screen, cursorRow, cursorCol = buildPassword(rows, cols, now, user.Name, &pw)
			redrawOnTimer = false
		case modeSettings:
			screen, cursorRow, cursorCol = buildSettings(rows, cols, now, user.Name, settingsStatus(cfg.Users, user.ID), message, !messageOK)
			redrawOnTimer = false
		case modeTrelloKey:
			list, client, err := cfg.Users.TrelloClient()
			settings, siteErr := cfg.Users.Site()
			screen, cursorRow, cursorCol = buildTrelloKey(rows, cols, now, list, client, settings, errors.Join(err, siteErr), &tk)
			redrawOnTimer = false
		case modeSite:
			settings, err := cfg.Users.Site()
			screen, cursorRow, cursorCol = buildSite(rows, cols, now, settings, err, cfg.HTTPListen, &site)
			redrawOnTimer = false
		case modeShutdownConfirm:
			screen, cursorRow, cursorCol = buildShutdownConfirm(rows, cols, now, cfg.Shutdown.Sessions()), rows-1, 0
			redrawOnTimer = false
		default:
			light, keys := cfg.lightFor(user)
			v := gather(cfg, now, cfg.agendaFor(user), cfg.tasksFor(user), ownerOf(user), light)
			v.BusyKeys, v.Message = keys != nil, message
			drawnBusy = v.Busy
			screen, page, totalPages, checklistAt = buildDashboard(rows, cols, v, page, neg.LUName)
			cursorRow, cursorCol = dashboardCommandRow(rows), commandInputCol+1
		}

		screen = tint(screen)

		// A timed redraw of the dashboard, calendar, chat or activity viewer
		// writes over the screen without erasing it, and leaves the input
		// fields and the cursor alone, so that a command half typed, the
		// cursor moved to a day not yet picked, or marks typed, survive it.
		opts := go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: cp, CursorRow: cursorRow, CursorCol: cursorCol,
		}
		if timedOut && (mode == modeDashboard || mode == modeCalendar || mode == modeChat || mode == modeActivity) {
			screen, opts.NoClear = fillScreen(screen, rows, cols), true
		}

		// Without a redraw on a timer there is no deadline, so the screen
		// stays as drawn until a key is pressed.
		deadline := time.Time{}
		switch {
		case mode == modeLogin:
			deadline = login.expires
		case redrawOnTimer:
			deadline = nextRedraw(time.Now(), cfg.Refresh)
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
			endReason = "terminated by an administrator"
			bye(cfg.Activity.farewellFor(sessionID))
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
		// Likewise a change of the busy state since the dashboard was drawn.
		// Drawn over the screen, keeping what is typed, if the dashboard is
		// what is there; else, as nothing is typed yet, afresh.
		if mode == modeDashboard && lights.differs(drawnBusy) {
			timedOut = shownMode == modeDashboard
			continue
		}
		shownMode = mode
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
			switch tp.handleList(resp, allTasks, totalPages, taskArchiver(cfg.tasksFor(user)), taskMover(cfg.tasksFor(user))) {
			case taskListLeave:
				mode = modeDashboard
			case taskListConfirm:
				mode = modeConfirm
			case taskListAdd:
				mode, at = modeAddTask, addTaskState{}
			case taskListMove:
				mode, mv = modeMoveTask, startMove(taskMover(cfg.tasksFor(user)), tp.moving)
			}
		case modeMoveTask:
			if back, said, isError := mv.handle(resp, taskMover(cfg.tasksFor(user)), &tp, logf); back {
				mode, tp.message, tp.isError = modeTasks, said, isError
			}
		case modeConfirm:
			if tp.handleConfirm(resp, taskArchiver(cfg.tasksFor(user)), logf) {
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
			if leave, added := at.handle(resp, taskAdder(cfg.tasksFor(user))); leave {
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
				mode, us = modeUsers, usersState{checklists: cfg.Checklists, trelloBaseURL: cfg.TrelloBaseURL}
			case action == adminGoogleClient && cfg.Users == nil:
				message = "There is no user database to keep a Google client in."
			case action == adminGoogleClient:
				mode, gc = modeGoogleClient, googleClientState{}
			case action == adminTrelloKey && cfg.Users == nil:
				message = "There is no user database to keep a Trello API key in."
			case action == adminTrelloKey:
				mode, tk = modeTrelloKey, trelloKeyState{}
			case action == adminSite && cfg.Users == nil:
				message = "There is no user database to keep the web site's settings in."
			case action == adminSite:
				mode, site = modeSite, siteState{}
			case action == adminClearChat:
				mode = modeClearChatConfirm
			case action == adminActivity:
				mode, act = modeActivity, activityState{}
			case action == adminBackup:
				message, messageOK = runBackup(cfg.Backup, logf)
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
				logIn(*u)
			}
		case modeChat:
			if ch.handle(resp, cfg.Chat, sessionID, chatName(user, neg.LUName), rows) {
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
		case modeGoogle:
			if leave, said := gs.handle(resp, cfg.Users, logf); leave {
				mode, message, messageOK = settingsBack, said, true
			}
		case modeGoogleClient:
			if gc.handle(resp, cfg.Users, logf) {
				mode = modeAdmin
			}
		case modePassword:
			if leave, said := pw.handle(resp, cfg.Users, user, logf); leave {
				mode, message, messageOK = settingsBack, said, true
			}
		case modeTrello:
			if leave, said := tr.handle(resp, cfg.Users, logf); leave {
				mode, message, messageOK = settingsBack, said, true
			}
		case modeSettings:
			switch command, leave, bad := settingsChoice(resp); {
			case leave:
				mode = modeDashboard
			case command != "":
				settingsBack = modeSettings
				message = openSetting(command)
			default:
				message = bad
			}
		case modeTrelloKey:
			if tk.handle(resp, cfg.Users, logf) {
				mode = modeAdmin
			}
		case modeSite:
			if site.handle(resp, cfg.Users, logf) {
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
					mode, cl = modeChecklist, checklistState{owner: ownerOf(user), open: id}
				}
				break
			}
			if resp.AID != go3270.AIDEnter {
				_, keys := cfg.lightFor(user)
				typed = pfCommand(resp.AID, keys != nil)
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
	modeGoogle
	modeGoogleClient
	modeSite
	modeTrello
	modeTrelloKey
	modePassword
	modeMoveTask
	modeSettings
)

// chatName is who a session is on the chat: its user's name, or with no
// user (a console with no user database), its LU name.
func chatName(u *users.User, lu string) string {
	if u == nil {
		return lu
	}
	return u.Name
}

// taskArchiver is c as a TaskArchiver, nil (not a nil *tasks.Cache in an
// interface) when c is nil.
func taskArchiver(c *tasks.Cache) TaskArchiver {
	if c == nil {
		return nil
	}
	return c
}

// taskMover is c as a TaskMover, nil when c is nil.
func taskMover(c *tasks.Cache) TaskMover {
	if c == nil {
		return nil
	}
	return c
}

// taskAdder is c as a TaskAdder, nil when c is nil.
func taskAdder(c *tasks.Cache) TaskAdder {
	if c == nil {
		return nil
	}
	return c
}

// ownerOf is the owner of u's checklists: their ID, or zero with no user.
func ownerOf(u *users.User) int {
	if u == nil {
		return 0
	}
	return u.ID
}

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

// nextRedraw is when a screen drawn at now, redrawn every interval (zero
// for a second), is next redrawn: at the next whole multiple of a second,
// just after it, so that the clock's seconds tick evenly.
func nextRedraw(now time.Time, interval time.Duration) time.Time {
	interval = max(interval, time.Second)
	next := now.Add(interval).Truncate(time.Second)
	if !next.After(now) {
		next = next.Add(time.Second)
	}
	return next.Add(5 * time.Millisecond)
}

// lightWatch is the busy state a session's dashboard follows, to be woken
// when it changes.
type lightWatch struct {
	src    busy.Source
	stopFn func()
}

// follow watches src, calling wake at each change, in place of whatever it
// watched before; it does nothing if it watches src already. A source that
// cannot be watched is followed, but wakes nothing.
func (w *lightWatch) follow(src busy.Source, wake func()) {
	if src == w.src && src != nil {
		return
	}
	w.stop()
	w.src = src
	if ws, ok := src.(interface{ Watch(func()) func() }); ok {
		w.stopFn = ws.Watch(wake)
	}
}

// stop stops watching.
func (w *lightWatch) stop() {
	if w.stopFn != nil {
		w.stopFn()
	}
	w.src, w.stopFn = nil, nil
}

// differs reports whether the state followed now shows otherwise than drawn.
func (w *lightWatch) differs(drawn busy.Status) bool {
	if w.src == nil {
		return false
	}
	s := w.src.Status()
	return s.Light != drawn.Light || s.Enabled != drawn.Enabled
}

// controlsLight reports whether u controls the busy light, and so can set it
// by hand: a user marked so, or a console with no user database, which has
// everything.
func controlsLight(u *users.User) bool {
	return u == nil || u.Flag
}

// lightFor is the busy state u sees and sets: the light, for a user who
// controls it, by their entry as it is now; else their own (see Personal).
// Either is nil for none.
func (cfg Config) lightFor(u *users.User) (busy.Source, interface{ Key(rune) error }) {
	if u != nil && cfg.Users != nil {
		if list, _, err := cfg.Users.Load(); err == nil {
			if i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == u.ID }); i >= 0 {
				u = &list[i]
			}
		}
	}
	if !controlsLight(u) && cfg.Personal != nil {
		p := cfg.Personal.For(u.ID)
		return p, p
	}
	if cfg.Busy == nil || cfg.BusyKeys == nil {
		return cfg.Busy, nil
	}
	return cfg.Busy, cfg.BusyKeys
}

// UserMeetings are the meetings on the calendar of the user with id, by the
// agenda's rules (no all-day or out-of-office events), for their busy state.
func (cfg Config) UserMeetings(id int) []agenda.Event {
	if cfg.Users == nil {
		return nil
	}
	c := cfg.agendaFor(&users.User{ID: id})
	if c == nil {
		return nil
	}
	var out []agenda.Event
	for _, e := range c.Snapshot().Events {
		if isMeeting(e) {
			out = append(out, e)
		}
	}
	return out
}

// FlagEvents are the meetings that light the busy light, and how many users
// control it: those marked so, with each one's calendar's meetings, by the
// agenda's rules (no all-day or out-of-office events). Reading them keeps
// their calendars' agendas refreshing, whether or not anyone is looking.
func (cfg Config) FlagEvents() (meetings []agenda.Event, controllers int) {
	if cfg.Users == nil {
		return nil, 0
	}
	list, _, err := cfg.Users.Load()
	if err != nil {
		return nil, 0
	}
	for _, u := range list {
		if !u.Flag || u.Restricted {
			continue
		}
		controllers++
		meetings = append(meetings, cfg.UserMeetings(u.ID)...)
	}
	return meetings, controllers
}

// view is everything one redraw shows, gathered up front so that building
// the screen is a pure function.
type view struct {
	Now time.Time

	// BusyKeys is whether the busy keys are offered, and Message what the
	// last key reported; they are the session's, not gathered from a
	// source.
	BusyKeys bool
	Message  string

	BusyEnabled bool
	Busy        busy.Status

	AgendaEnabled bool
	Agenda        agenda.Snapshot

	// TasksEnabled is whether the user has Trello lists linked; without,
	// there are no tasks, and nothing of Trello is shown.
	TasksEnabled bool

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

// gather reads the current state of every source: the agenda from
// agendaCache, the tasks from taskCache and the busy state from light, nil
// for none, and the checklists owner's.
func gather(cfg Config, now time.Time, agendaCache *agenda.Cache, taskCache *tasks.Cache, owner int, light busy.Source) view {
	v := view{Now: now}
	if light != nil {
		v.Busy = light.Status()
		v.BusyEnabled = v.Busy.Enabled
	}
	if agendaCache != nil {
		v.AgendaEnabled = true
		v.Agenda = agendaCache.Snapshot()
	}

	if cfg.Checklists != nil {
		v.Checklists, v.ChecklistsErr = cfg.Checklists.Load()
		v.Checklists = checklist.Owned(v.Checklists, owner)
	}

	if taskCache != nil {
		snap := taskCache.Snapshot()
		v.TasksEnabled = true
		v.Tasks, v.TasksErr, v.TasksFetched, v.TasksLoading = snap.Tasks, snap.Err, snap.Fetched, snap.Loading
	}
	return v
}
