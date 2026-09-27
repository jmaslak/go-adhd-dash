// Package session drives one TN3270 client connection: telnet negotiation,
// then a dashboard that redraws itself on a timer until the client quits.
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

	// Busy follows the busy indicator; nil when none is configured.
	Busy *busy.Watcher

	// Agenda holds the calendar; nil when no calendar is configured.
	Agenda *agenda.Cache

	// Refresh is how often an idle screen is redrawn.
	Refresh time.Duration
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

	for {
		v := gather(cfg, time.Now())
		screen, shown, totalPages := buildDashboard(rows, cols, v, page, neg.LUName)
		page = shown

		_ = conn.SetReadDeadline(time.Now().Add(cfg.Refresh))
		resp, err := go3270.ShowScreenOpts(screen, nil, conn, go3270.ScreenOpts{
			AltScreen: devinfo, Codepage: devinfo.Codepage(), CursorRow: rows - 1, CursorCol: 0,
		})
		if isTimeout(err) {
			continue
		}
		if err != nil {
			log.Printf("session %d (%s): %v", sessionID, rawConn.RemoteAddr(), err)
			return
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch resp.AID {
		case go3270.AIDPF3:
			return
		case go3270.AIDPF7:
			page = max(page-1, 0)
		case go3270.AIDPF8:
			page = min(page+1, totalPages-1)
		}
		// Enter, Clear and anything else just redraw.
	}
}

// view is everything one redraw shows, gathered up front so that building
// the screen is a pure function.
type view struct {
	Now time.Time

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
