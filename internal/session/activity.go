package session

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/racingmars/go3270"
)

// Activity tracks what every session of this server is doing, for the
// activity viewer, and terminates sessions from it.
type Activity struct {
	mu       sync.Mutex
	sessions map[uint64]*activityEntry
}

// activityEntry is one session's record: what the viewer shows, and what it
// takes to terminate it.
type activityEntry struct {
	SessionActivity
	conn       net.Conn
	terminated bool
	farewell   string // why, left on the terminal as it disconnects
}

// SessionActivity is what one session is doing.
type SessionActivity struct {
	ID        uint64
	LU        string // empty until negotiated
	User      string // who logged in; empty for a session from this machine, or before login
	Addr      string // the client's IP address
	Screen    string
	Connected time.Time
	LastKey   time.Time // when a key was last pressed, or the session connected
}

// terminateGrace is how long a terminated session has to say goodbye and
// disconnect before its connection is closed under it.
const terminateGrace = 5 * time.Second

// NewActivity returns an empty Activity.
func NewActivity() *Activity {
	return &Activity{sessions: map[uint64]*activityEntry{}}
}

// add records session id connecting on conn at now. A nil Activity records
// nothing, here and in its other methods.
func (a *Activity) add(id uint64, conn net.Conn, now time.Time) {
	if a == nil {
		return
	}
	host := conn.RemoteAddr().String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[id] = &activityEntry{
		SessionActivity: SessionActivity{ID: id, Addr: host, Screen: "Connecting", Connected: now, LastKey: now},
		conn:            conn,
	}
}

// remove forgets session id, which has disconnected.
func (a *Activity) remove(id uint64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
}

// update changes what is recorded of session id.
func (a *Activity) update(id uint64, change func(*SessionActivity)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[id]; ok {
		change(&s.SessionActivity)
	}
}

// List is every session, in the order they connected. One terminated but
// not yet gone is on the screen "Terminating".
func (a *Activity) List() []SessionActivity {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]SessionActivity, 0, len(a.sessions))
	for _, s := range a.sessions {
		sa := s.SessionActivity
		if s.terminated {
			sa.Screen = "Terminating"
		}
		out = append(out, sa)
	}
	slices.SortFunc(out, func(x, y SessionActivity) int { return cmp.Compare(x.ID, y.ID) })
	return out
}

// terminate asks session id to disconnect, reporting false if it already
// has. A session waiting for a key is woken by its read deadline passing,
// sees it is terminated, and disconnects; one busy with a key does so when
// it is done with it. One still connected after terminateGrace has its
// connection closed, which ends it at its next read or write but never in
// the middle of writing a file.
//
// It is left saying an administrator terminated it.
func (a *Activity) terminate(id uint64) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminateLocked(id, terminatedFarewell)
}

// claimLU records that session id has LU name lu, terminating every other
// session with it, each left saying farewell, and returns their IDs. Done
// under one hold of the lock, so that of two sessions claiming lu at once,
// the later one keeps it.
func (a *Activity) claimLU(id uint64, lu, farewell string) (booted []uint64) {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for other, s := range a.sessions {
		if other != id && s.LU == lu && !s.terminated && a.terminateLocked(other, farewell) {
			booted = append(booted, other)
		}
	}
	if s, ok := a.sessions[id]; ok {
		s.LU = lu
	}
	slices.Sort(booted)
	return booted
}

// terminateLocked is terminate, leaving farewell. a.mu must be held.
func (a *Activity) terminateLocked(id uint64, farewell string) bool {
	s, ok := a.sessions[id]
	if !ok {
		return false
	}
	s.terminated, s.farewell = true, farewell
	_ = s.conn.SetReadDeadline(time.Now())
	time.AfterFunc(terminateGrace, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.sessions[id] == s {
			_ = s.conn.Close()
		}
	})
	return true
}

// terminated reports whether session id has been terminated.
func (a *Activity) terminated(id uint64) bool {
	return a.farewellFor(id) != ""
}

// farewellFor is what to leave on session id's terminal as it disconnects,
// if it has been terminated, else "".
func (a *Activity) farewellFor(id uint64) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[id]; ok && s.terminated {
		return s.farewell
	}
	return ""
}

// Activity viewer layout: the heading, the column headings, then one session
// per row down to the row above the message row. Each row is a
// one-character field for marking the session, then what it is doing.
const (
	actHeaderRow = 2
	actColumnRow = 3
	actFirstRow  = 4
	actTextCol   = 2 // the session's attribute byte, after its mark field
	actSelField  = "sel:"
	actPrompt    = "Type X beside sessions to terminate, then press PF6."
)

// activityState is one session's place in the activity viewer: the page
// shown, the sessions marked for terminating, by ID, and those awaiting
// confirmation.
type activityState struct {
	page       int
	marked     map[uint64]bool
	confirming []SessionActivity

	message string
	isError bool
}

// activityRows is how many sessions fit on one page of the activity viewer.
func activityRows(rows int) int {
	return max(rows-2-actFirstRow, 1)
}

// activityFormat lays out one session's columns, after its mark field.
func activityFormat(self, lu, user, screen, connected, idle, addr string) string {
	return fmt.Sprintf("%1s %-8s %-10s %-18s %-9s %-5s %s", self, lu, truncate(user, 10), truncate(screen, 18), connected, idle, addr)
}

// buildActivity renders one page of sessions, each with its mark field, self
// marked *, and where the cursor goes: the first mark field. The page is
// clamped to the pages there are, and the page count returned.
func buildActivity(rows, cols int, now time.Time, sessions []SessionActivity, self uint64, a *activityState) (screen go3270.Screen, totalPages, cursorRow, cursorCol int) {
	screen = titleFields(cols, "ACTIVITY", now, false)
	shown, totalPages, start, end := pageRange(len(sessions), activityRows(rows), a.page)
	a.page = shown
	count := countText(len(sessions), "session", 0, 1) + " connected"
	if totalPages > 1 {
		count += fmt.Sprintf(", page %d/%d", shown+1, totalPages)
	}
	if n := len(a.marked); n > 0 {
		count += fmt.Sprintf(", %d marked", n)
	}
	screen = append(screen, placeLine(actHeaderRow, cols, line{
		{Content: "ACTIVITY", Color: go3270.Turquoise, Intense: true},
		{Content: count + "; * is this one", Color: go3270.Blue},
	})...)

	if end > start {
		// Aligned with a session's row: the mark in column 1, the rest from
		// column 3. The first mark field stops the underline.
		headings := "S " + activityFormat("", "LU", "User", "Screen", "Connected", "Idle", "IP address")
		screen = append(screen, go3270.Field{
			Row: actColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
		})
	}
	cursorRow, cursorCol = rows-1, 0
	for i, s := range sessions[start:end] {
		row := actFirstRow + i
		mark := ""
		if a.marked[s.ID] {
			mark = "X"
		}
		star, color, intense := "", go3270.Green, false
		if s.ID == self {
			star, color, intense = "*", go3270.White, true
		}
		connected := s.Connected.Format("15:04")
		if !dayOf(s.Connected).Equal(dayOf(now)) {
			connected = s.Connected.Format("Jan 2")
		}
		screen = append(screen,
			go3270.Field{
				Row: row, Col: 0, Write: true, Name: actSelField + strconv.FormatUint(s.ID, 10), Content: mark,
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			// Autoskip sends the cursor on to the next mark once one is
			// typed.
			go3270.Field{
				Row: row, Col: actTextCol, Color: color, Intense: intense, Autoskip: true,
				Content: truncate(activityFormat(star, s.LU, s.User, s.Screen, connected, duration(now.Sub(s.LastKey)), s.Addr), cols-actTextCol-1),
			},
		)
		if i == 0 {
			cursorRow, cursorCol = row, 1
		}
	}

	message, color := a.message, go3270.Red
	if !a.isError {
		color = go3270.Green
		if message == "" {
			message, color = actPrompt, go3270.Blue
		}
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: a.isError}})...)
	screen = append(screen, go3270.Field{
		Row: rows - 1, Col: 0, Color: go3270.Blue,
		Content: truncate("PF3=Back PF6=Terminate marked PF7=Up PF8=Down Enter=Refresh", cols-1),
	})
	return screen, totalPages, cursorRow, cursorCol
}

// buildTerminateConfirm renders the confirmation for terminating a's
// sessions awaiting it, noting when self is among them.
func buildTerminateConfirm(rows, cols int, now time.Time, self uint64, a *activityState) go3270.Screen {
	screen := titleFields(cols, "TERMINATE SESSIONS", now, false)
	question := "Terminate this session?"
	if len(a.confirming) != 1 {
		question = "Terminate these " + countText(len(a.confirming), "session", 0, 1) + "?"
	}
	screen = append(screen, placeLine(actHeaderRow, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)
	// The sessions below start at actTextCol, not at the start of a row,
	// so the underline is ended at the end of the row by a field of its
	// own; else it would run on into the next row.
	headings := activityFormat("", "LU", "User", "Screen", "Connected", "Idle", "IP address")
	screen = append(screen,
		go3270.Field{
			Row: actColumnRow, Col: actTextCol, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-actTextCol-2-len(headings), 0)),
		},
		go3270.Field{Row: actColumnRow, Col: cols - 1},
	)

	// The list runs from below the headings to above the note on this
	// session.
	limit := max(rows-3-actFirstRow, 1)
	shown := a.confirming
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := actFirstRow
	for _, s := range shown {
		star := ""
		if s.ID == self {
			star = "*"
		}
		screen = append(screen, go3270.Field{
			Row: row, Col: actTextCol, Color: go3270.Green,
			Content: truncate(activityFormat(star, s.LU, s.User, s.Screen, s.Connected.Format("15:04"), duration(now.Sub(s.LastKey)), s.Addr), cols-actTextCol-1),
		})
		row++
	}
	if len(shown) < len(a.confirming) {
		screen = append(screen, placeLineAt(row, actTextCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(a.confirming)-len(shown)), Color: go3270.Blue}})...)
	}

	if slices.ContainsFunc(a.confirming, func(s SessionActivity) bool { return s.ID == self }) {
		screen = append(screen, placeLine(rows-3, cols, line{{Content: "This session (*) is among them, and will be disconnected too.", Color: go3270.Pink, Intense: true}})...)
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to terminate them, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Terminate", cols-1)})
}

// handleList acts on a key pressed on the activity viewer, returning
// whether to leave for the admin menu or go to the confirmation. The marks
// typed are kept whatever the key, except PF3; marks on sessions gone are
// dropped.
func (a *activityState) handleList(resp go3270.Response, sessions []SessionActivity, totalPages int) (leave, confirm bool) {
	a.message, a.isError = "", false
	if resp.AID == go3270.AIDPF3 {
		return true, false
	}
	if a.marked == nil {
		a.marked = map[uint64]bool{}
	}
	present := map[uint64]bool{}
	for _, s := range sessions {
		present[s.ID] = true
		v, ok := resp.Values[actSelField+strconv.FormatUint(s.ID, 10)]
		if !ok {
			continue
		}
		switch strings.ToUpper(v) {
		case "":
			delete(a.marked, s.ID)
		case "X":
			a.marked[s.ID] = true
		default:
			a.message, a.isError = fmt.Sprintf("Type X beside a session to terminate it, not %q.", v), true
			return false, false
		}
	}
	for id := range a.marked {
		if !present[id] {
			delete(a.marked, id)
		}
	}

	switch resp.AID {
	case go3270.AIDPF7:
		a.page = max(a.page-1, 0)
	case go3270.AIDPF8:
		a.page = min(a.page+1, totalPages-1)
	case go3270.AIDPF6:
		a.confirming = nil
		for _, s := range sessions {
			if a.marked[s.ID] {
				a.confirming = append(a.confirming, s)
			}
		}
		if len(a.confirming) == 0 {
			a.message, a.isError = "Nothing is marked. "+actPrompt, true
			return false, false
		}
		return false, true
	}
	return false, false
}

// handleConfirm acts on a key pressed on the confirmation, returning
// whether to go back to the viewer. PF4 terminates the sessions; PF3 goes
// back with the marks kept; any other key leaves the confirmation up.
func (a *activityState) handleConfirm(resp go3270.Response, activity *Activity, logf func(string, ...any)) (back bool) {
	switch resp.AID {
	case go3270.AIDPF3:
		a.confirming = nil
		return true
	case go3270.AIDPF4:
	default:
		return false
	}
	done, gone := 0, 0
	for _, s := range a.confirming {
		delete(a.marked, s.ID)
		if !activity.terminate(s.ID) {
			gone++
			continue
		}
		logf("terminated session %d, LU %s, from %s", s.ID, s.LU, s.Addr)
		done++
	}
	a.message = "Terminated " + countText(done, "session", 0, 1) + "."
	if gone > 0 {
		a.message += fmt.Sprintf(" %d had already disconnected.", gone)
	}
	a.isError, a.confirming = false, nil
	return true
}

// terminatedFarewell is left on a terminal whose session was terminated
// from the activity viewer.
const terminatedFarewell = "This session was terminated by an administrator. Goodbye."

// screenName names the screen a session in mode shows, for the activity
// viewer.
func screenName(mode int, cl *checklistState, calc *calcState) string {
	switch mode {
	case modeCalendar:
		return "Calendar"
	case modeTasks:
		return "Task list"
	case modeConfirm:
		return "Archive tasks"
	case modeCalc:
		if calc.dbm {
			return "dBm calculator"
		}
		return "Calculator"
	case modeChecklist:
		switch {
		case cl.open == 0:
			return "Checklists"
		case cl.openName != "":
			return "Checklist: " + cl.openName
		}
		return "Checklist"
	case modeHelp:
		return "Help"
	case modeAdmin:
		return "Admin menu"
	case modeShutdownConfirm:
		return "Shut down"
	case modeAddTask:
		return "Add task"
	case modeActivity:
		return "Activity viewer"
	case modeTerminateConfirm:
		return "Terminate sessions"
	case modeChat:
		return "Chat"
	case modeClearChatConfirm:
		return "Clear chat"
	case modeUsers:
		return "Users"
	case modeLogin:
		return "Login"
	}
	return "Dashboard"
}
