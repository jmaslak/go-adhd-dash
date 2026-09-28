// Package session drives one TN3270 client connection: telnet negotiation,
// then a dashboard that redraws itself on a timer until the client quits,
// with a calendar screen a key away.
package session

import (
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"

	tn3270e "github.com/jmaslak/go-3270e"
	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// nextSessionID numbers connections for the log and for LU names.
var nextSessionID atomic.Uint64

// Config holds what every session shares.
type Config struct {
	// TasksDir is the task program's directory, read on every redraw.
	TasksDir string

	// Busy supplies the busy indicator's status; nil when none is
	// configured.
	Busy busy.Source

	// BusyControl sends keys to the busy indicator; nil when its control
	// port is not configured.
	BusyControl *busy.Control

	// Agenda holds the calendar; nil when no calendar is configured.
	Agenda *agenda.Cache

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
// or presses PF3.
func Handle(rawConn net.Conn, cfg Config) {
	defer rawConn.Close() //nolint:errcheck

	sessionID := nextSessionID.Add(1)
	neg, err := tn3270e.Negotiate(rawConn, fmt.Sprintf("AD%06X", sessionID&0xFFFFFF))
	if err != nil {
		log.Printf("session %d (%s): %v", sessionID, rawConn.RemoteAddr(), err)
		return
	}
	conn, devinfo := neg.Conn, neg.DevInfo
	log.Printf("session %d (%s): connected, LU %s", sessionID, rawConn.RemoteAddr(), neg.LUName)
	defer log.Printf("session %d (%s): disconnected", sessionID, rawConn.RemoteAddr())

	rows, cols := devinfo.AltDimensions()
	page := 0
	autoRefresh := true
	inCalendar := false
	var cal calendarState
	message := "" // shown on the dashboard until the next key

	for {
		now := time.Now()
		var screen go3270.Screen
		var totalPages, cursorRow, cursorCol int
		if inCalendar {
			cal.load(cfg.Agenda, cfg.AgendaRefresh, now)
			screen, cursorRow, cursorCol = buildCalendar(rows, cols, cal.view(now, autoRefresh, cfg.Agenda != nil))
		} else {
			v := gather(cfg, now)
			v.AutoRefresh, v.BusyControl, v.Message = autoRefresh, cfg.BusyControl != nil, message
			screen, page, totalPages = buildDashboard(rows, cols, v, page, neg.LUName)
			cursorRow, cursorCol = rows-1, 0
		}

		// With auto-refresh off there is no deadline, so the screen stays as
		// drawn until a key is pressed.
		deadline := time.Time{}
		if autoRefresh {
			deadline = time.Now().Add(cfg.Refresh)
		}
		_ = conn.SetReadDeadline(deadline)
		resp, err := go3270.ShowScreenOpts(screen, nil, conn, go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: devinfo.Codepage(), CursorRow: cursorRow, CursorCol: cursorCol,
		})
		if isTimeout(err) {
			continue
		}
		if err != nil {
			log.Printf("session %d (%s): %v", sessionID, rawConn.RemoteAddr(), err)
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		message = ""

		if resp.AID == go3270.AIDPF5 {
			autoRefresh = !autoRefresh
			continue
		}
		if inCalendar {
			switch resp.AID {
			case go3270.AIDPF3:
				inCalendar = false
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
			continue
		}
		switch resp.AID {
		case go3270.AIDPF3:
			return
		case go3270.AIDPF7:
			page = max(page-1, 0)
		case go3270.AIDPF8:
			page = min(page+1, totalPages-1)
		case go3270.AIDPF9:
			inCalendar = true
			cal.goTo(time.Now())
		case go3270.AIDPF1:
			message = sendBusyKey(cfg, 'b')
		case go3270.AIDPF2:
			message = sendBusyKey(cfg, 'o')
		}
		// Enter, Clear and anything else just redraw.
	}
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
	return "Sent to busy indicator at " + cfg.BusyControl.Addr + ", but its feed reported no change"
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

	Tasks    []tasks.Task
	TasksErr error
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

	// ignore-tags is re-read each time so a change to the task program's
	// configuration shows up without restarting the server.
	ignore, err := tasks.IgnoreTags()
	if err != nil {
		v.TasksErr = err
		return v
	}
	all, err := tasks.ReadDir(cfg.TasksDir)
	if err != nil {
		v.TasksErr = err
		return v
	}
	v.Tasks = tasks.Visible(all, ignore, now)
	return v
}
