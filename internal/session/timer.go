package session

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/bigtext"
	"github.com/jmaslak/go-adhd-dash/internal/timers"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The timer (the TIMER command): asked how long, or until when (a time of
// day, as an alarm), and what for, it counts down in large digits filling the screen,
// redrawn each second, then flashes DONE until Enter is pressed. Each user
// has one timer, kept in the database, which all their sessions show:
// started, stopped or dismissed in one, it is in all.
const (
	timerField      = "timer"
	timerUntilField = "until"
	timerNoteField  = "note"
	timerLabel      = "How long ===>"
	timerUntilLabel = "Or until ===>" // as long as timerLabel, so the fields line up
	timerNoteLabel  = "What for ===>"
	timerRow        = 4
	timerUntilRow   = timerRow + 5
	timerNoteRow    = timerUntilRow + 5
	timerWidth      = 12
	timerNoteWidth  = 40
	timerNoteless   = "Timer"                     // the note of a timer not said to be for anything
	timerMax        = 100*time.Hour - time.Second // 99:59:59, the most the digits show
	timerPrompt     = "Type how long, or until when, then press Enter."
)

// timerStep is where a timer is.
type timerStep int

const (
	timerAsk     timerStep = iota // asking how long
	timerRunning                  // counting down
	timerDone                     // flashing DONE
)

// timerState is one session's timer.
type timerState struct {
	step timerStep

	// typed, typedUntil and typedNote are how long, until when and what
	// for, as typed, drawn again when they will not do; onUntil puts the
	// cursor on until when, which is what the message is about.
	typed, typedUntil, typedNote string
	onUntil                      bool

	// until is set for a timer to a time of day, rather than for how long.
	until bool
	total time.Duration
	end   time.Time
	note  string // what it is for

	message string
	isError bool

	// syncErr is the last error reading the timer kept, logged once.
	syncErr string
}

// timerStoppedElsewhere is said on the asking screen when the timer shown
// is stopped, or dismissed once done, in another of the user's sessions.
const timerStoppedElsewhere = "The timer was stopped in another of your sessions."

// kept is the timer t shows, as the store keeps it.
func (t *timerState) kept() timers.Timer {
	return timers.Timer{Until: t.until, Total: t.total, End: t.end, Note: t.note}
}

// noteOf is the note typed for a timer, spaces run together, cut to
// timerNoteWidth; timerNoteless for none.
func noteOf(typed string) string {
	note := cutRunes(strings.Join(strings.Fields(typed), " "), timerNoteWidth)
	if note == "" {
		return timerNoteless
	}
	return note
}

// shownNote is what t is for, as shown: timerNoteless for nothing.
func (t *timerState) shownNote() string {
	if t.note == "" {
		return timerNoteless
	}
	return t.note
}

// sync makes t, at now, show owner's timer as store keeps it: the one
// started in any of their sessions, or, with none, the asking screen. It
// reports whether that moved t to another step, which is drawn afresh. With
// no store, t is the session's own and left as it is; with the store not
// read, as it was, the error logged once.
func (t *timerState) sync(store *timers.Store, owner int, now time.Time, logf func(string, ...any)) (changed bool) {
	if store == nil {
		return false
	}
	kept, ok, err := store.Get(owner)
	if err != nil {
		if err.Error() != t.syncErr {
			logf("%v", err)
		}
		t.syncErr = err.Error()
		if t.step == timerAsk {
			t.message, t.isError = "Could not read your timer: "+err.Error()+".", true
		}
		return false
	}
	t.syncErr = ""
	before := t.step
	switch {
	case ok:
		if t.step == timerAsk {
			t.typed, t.typedUntil, t.typedNote, t.onUntil, t.message, t.isError = "", "", "", false, "", false
		}
		t.step, t.until, t.total, t.end, t.note = timerRunning, kept.Until, kept.Total, kept.End, kept.Note
		if !now.Before(t.end) {
			t.step = timerDone
		}
	case t.step != timerAsk:
		*t = timerState{message: timerStoppedElsewhere}
	}
	return t.step != before
}

var (
	// timerClock is hh:mm, hours and minutes.
	timerClock = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	// timerUnits is a number of hours, minutes and seconds, each optional,
	// in that order, as 2h3m20s.
	timerUnits = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`)
	// untilClock is a 24-hour time of day: hh:mm or hh:mm:ss, or hhmm.
	untilClock = regexp.MustCompile(`^(?:(\d{1,2}):(\d{2})(?::(\d{2}))?|(\d{2})(\d{2}))$`)
)

// parseUntil reads the time of day a timer is to run until, after now: a
// 24-hour time, hh:mm or hh:mm:ss (or hhmm), spaces ignored. A time not
// after now today is tomorrow.
func parseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.Join(strings.Fields(s), "")
	m := untilClock.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("%q is not a 24-hour time, hh:mm or hh:mm:ss", s)
	}
	hText, mText, sText := m[1], m[2], m[3]
	if hText == "" {
		hText, mText = m[4], m[5]
	}
	h, _ := strconv.Atoi(hText)
	mins, _ := strconv.Atoi(mText)
	secs, _ := strconv.Atoi(sText) // 0 when not given
	if h > 23 || mins > 59 || secs > 59 {
		return time.Time{}, fmt.Errorf("%q is not a time of day: hours go up to 23, minutes and seconds to 59", s)
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), h, mins, secs, 0, now.Location())
	if !at.After(now) {
		at = time.Date(now.Year(), now.Month(), now.Day()+1, h, mins, secs, 0, now.Location())
	}
	return at, nil
}

// parseTimer reads how long a timer is to run: hh:mm (hours and
// minutes), or hours, minutes and seconds, as 2h3m20s, 45m or 90s, in any
// case, spaces ignored. It must be more than nothing, and no more than
// 99:59:59.
func parseTimer(s string) (time.Duration, error) {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	var d time.Duration
	switch m := timerClock.FindStringSubmatch(s); {
	case s == "":
		return 0, errors.New("type how long, or until when")
	case m != nil:
		h, _ := strconv.Atoi(m[1])
		mins, _ := strconv.Atoi(m[2])
		if mins > 59 {
			return 0, fmt.Errorf("%q: minutes go up to 59", s)
		}
		d = time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute
	default:
		m := timerUnits.FindStringSubmatch(s)
		if m == nil {
			return 0, fmt.Errorf("%q is not hh:mm, nor hours, minutes and seconds as 2h3m20s", s)
		}
		for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
			if m[i+1] == "" {
				continue
			}
			n, err := strconv.Atoi(m[i+1])
			if err != nil || n > int(timerMax/unit) {
				return 0, fmt.Errorf("%q is longer than 99:59:59", s)
			}
			d += time.Duration(n) * unit
		}
	}
	switch {
	case d <= 0:
		return 0, errors.New("a timer must run for more than no time")
	case d > timerMax:
		return 0, fmt.Errorf("%q is longer than 99:59:59", s)
	}
	return d, nil
}

// remaining is how long the timer has left at now, in whole seconds,
// rounded up, so that it shows 0:00 only once done.
func (t *timerState) remaining(now time.Time) time.Duration {
	return timeLeft(t.end, now)
}

// timeLeft is how long a timer ending at end has left at now, in whole
// seconds, rounded up.
func timeLeft(end, now time.Time) time.Duration {
	return max((end.Sub(now) + time.Second - 1).Truncate(time.Second), 0)
}

// endText is when a timer ends, as seen at now: the time of day; on the
// next day, with "tomorrow" after it; later, with the day of the week
// before it, which is never ambiguous, as a timer runs under 100 hours.
func endText(end, now time.Time) string {
	end = end.In(now.Location())
	clock := end.Format("15:04:05")
	switch {
	case endsLater(end, now):
		return end.Format("Mon") + " " + clock
	case !dayOf(end).Equal(dayOf(now)):
		return clock + " tomorrow"
	}
	return clock
}

// endsLater reports whether end is after tomorrow, seen at now.
func endsLater(end, now time.Time) bool {
	return dayOf(end.In(now.Location())).After(dayOf(now).AddDate(0, 0, 1))
}

// endsAt is the timer screen's "Ends at" for a timer ending at end, seen
// at now, as endText tells it, but for a day later than tomorrow, which
// goes first: "Ends Thu at 01:00:00".
func endsAt(end, now time.Time) string {
	if endsLater(end, now) {
		end = end.In(now.Location())
		return "Ends " + end.Format("Mon") + " at " + end.Format("15:04:05")
	}
	return "Ends at " + endText(end, now)
}

// countdownColor is the color of a countdown with left to go: green, then
// yellow in the last minute and red in the last ten seconds.
func countdownColor(left time.Duration) go3270.Color {
	switch {
	case left <= 10*time.Second:
		return go3270.Red
	case left <= time.Minute:
		return go3270.Yellow
	}
	return go3270.Green
}

// deadline is when a timer screen drawn at now is next redrawn: each
// second, as the clock is, and once more as the timer runs out, so that
// DONE shows on time; zero for none.
func (t *timerState) deadline(now time.Time) time.Time {
	next := nextRedraw(now, time.Second)
	if t.step == timerRunning && t.end.After(now) && t.end.Before(next) {
		next = t.end.Add(5 * time.Millisecond)
	}
	return next
}

// clockText is d as the countdown shows it: h:mm:ss, or mm:ss when the
// timer is shorter than an hour.
func clockText(d, total time.Duration) string {
	s := int(d / time.Second)
	if total >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// buildTimer renders the timer, and where the cursor goes, for a user
// whose preferences are prefs. A running timer whose time is up becomes
// done.
func buildTimer(rows, cols int, now time.Time, t *timerState, prefs users.Preferences) (screen go3270.Screen, cursorRow, cursorCol int) {
	if t.step == timerRunning && !now.Before(t.end) {
		t.step = timerDone
	}
	screen = titleFields(cols, "TIMER", now)
	switch t.step {
	case timerAsk:
		fieldCol := len(timerLabel) + 1
		for _, f := range []struct {
			row          int
			label, name  string
			typed        string
			width        int
			explanations []string
		}{
			{timerRow, timerLabel, timerField, t.typed, timerWidth, []string{
				"hh:mm, hours and minutes: 1:30 is an hour and a half, 0:05 five minutes.",
				"Or hours, minutes and seconds: 2h3m20s, 45m, 90s, 1h30m.",
			}},
			{timerUntilRow, timerUntilLabel, timerUntilField, t.typedUntil, timerWidth, []string{
				"A 24-hour time, hh:mm or hh:mm:ss: 17:45, 07:30:15 (or 1745).",
				"A time already past today is tomorrow's.",
			}},
			{timerNoteRow, timerNoteLabel, timerNoteField, t.typedNote, timerNoteWidth, []string{
				"What the timer is for, shown as it runs: laundry, call Sam back.",
				`Left blank, it is "` + timerNoteless + `".`,
			}},
		} {
			screen = append(screen,
				go3270.Field{Row: f.row, Col: 0, Color: go3270.Turquoise, Content: f.label},
				go3270.Field{
					Row: f.row, Col: fieldCol, Write: true, Name: f.name, Content: cutRunes(f.typed, f.width),
					Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
				},
				go3270.Field{Row: f.row, Col: fieldCol + 1 + f.width},
			)
			for i, text := range f.explanations {
				screen = append(screen, placeLine(f.row+2+i, cols, line{{Content: text, Color: go3270.Green}})...)
			}
		}
		screen = appendMessageRows(screen, rows, cols, t.message, t.isError, timerPrompt, "PF3=Back Enter=Start")
		if t.onUntil {
			return screen, timerUntilRow, fieldCol + 1
		}
		return screen, timerRow, fieldCol + 1

	case timerRunning:
		left := t.remaining(now)
		screen = append(screen, bigTextFields(1, rows-4, cols, clockText(left, t.total), countdownColor(left), false)...)
		info := line{
			{Content: t.shownNote() + ":", Color: go3270.White, Intense: true},
			{Content: endsAt(t.end, now), Color: go3270.Blue},
			{Content: "for " + clockText(t.total, t.total), Color: go3270.Blue},
		}
		if t.until {
			info = append(info[:1], go3270.Field{Content: "Until " + endText(t.end, now), Color: go3270.Blue})
		}
		screen = append(screen, placeLine(rows-3, cols, info)...)
		screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back, leaving it running PF6=Stop the timer", cols-1)})
		return screen, rows - 1, 0
	}

	// Done: every other second, the screen in red with DONE in it; or for
	// a user who avoids flashing, DONE in red, steadily.
	on := now.Unix()%2 == 0 && !prefs.NoFlash
	screen = append(screen, bigTextFields(1, rows-2, cols, "DONE", go3270.Red, on)...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.White, Intense: true, Content: truncate(t.shownNote()+": time is up. Enter=Dismiss PF3=Back", cols-1)})
	return screen, rows - 1, 0
}

// bigTextFields draws text in large characters centered in rows first to
// last: each row of them a field in color, the rest of the rows blank. With
// reverse set, every one of those rows is drawn in reverse video, filling
// them with color.
func bigTextFields(first, last, cols int, text string, color go3270.Color, reverse bool) go3270.Screen {
	big := bigtext.Rows(text)
	left := max((cols-1-bigtext.TextWidth(text))/2, 0)
	top := max(first+(last-first+1-bigtext.Height)/2, first)
	highlight := go3270.DefaultHighlight
	if reverse {
		highlight = go3270.ReverseVideo
	}
	var out go3270.Screen
	for row := first; row <= last; row++ {
		content := ""
		if i := row - top; i >= 0 && i < bigtext.Height {
			content = strings.Repeat(" ", left) + big[i]
		}
		if reverse {
			content += strings.Repeat(" ", max(cols-1-len(content), 0))
		}
		out = append(out, go3270.Field{Row: row, Col: 0, Color: color, Intense: true, Highlighting: highlight, Content: truncate(content, cols-1)})
	}
	return out
}

// handle acts on a key on the timer, at now, returning whether to go back
// to the dashboard, and what to say there. Asking, Enter starts the timer
// typed, and PF3 goes back; counting down, PF3 goes back, leaving it
// running, and PF6 stops it; done, PF3 goes back, leaving it done, and
// Enter (or PF6) dismisses it and goes back. A 3270 sends nothing for a
// key typed where there is no input field, only for Enter and the like, so
// Enter is what DONE asks for. With a store, the timer started, stopped or
// dismissed is owner's, there, for all their sessions.
func (t *timerState) handle(resp go3270.Response, now time.Time, store *timers.Store, owner int, logf func(string, ...any)) (back bool, said string) {
	t.message, t.isError = "", false
	if t.step == timerRunning && !now.Before(t.end) {
		t.step = timerDone
	}
	switch t.step {
	case timerAsk:
		switch resp.AID {
		case go3270.AIDPF3:
			return true, ""
		case go3270.AIDEnter:
		default:
			return false, ""
		}
		t.typed, t.typedUntil, t.typedNote, t.onUntil = resp.Values[timerField], resp.Values[timerUntilField], resp.Values[timerNoteField], false
		note := noteOf(t.typedNote)
		fail := func(onUntil bool, err error) bool {
			msg := err.Error()
			t.message, t.isError, t.onUntil = strings.ToUpper(msg[:1])+msg[1:]+".", true, onUntil
			return false
		}
		start := func(kept timers.Timer) error {
			if store == nil {
				return nil
			}
			return store.Start(owner, kept)
		}
		long, until := strings.TrimSpace(t.typed), strings.TrimSpace(t.typedUntil)
		switch {
		case long != "" && until != "":
			return fail(false, errors.New("type how long, or until when, not both")), ""
		case until != "":
			end, err := parseUntil(until, now)
			if err != nil {
				return fail(true, err), ""
			}
			if err := start(timers.Timer{Until: true, Total: end.Sub(now), End: end, Note: note}); err != nil {
				return fail(true, err), ""
			}
			t.step, t.until, t.total, t.end, t.note = timerRunning, true, end.Sub(now), end, note
			logf("started a timer %q until %s, in %v", note, end.Format("15:04:05"), end.Sub(now).Round(time.Second))
			return false, ""
		}
		d, err := parseTimer(long)
		if err != nil {
			return fail(false, err), ""
		}
		if err := start(timers.Timer{Total: d, End: now.Add(d), Note: note}); err != nil {
			return fail(false, err), ""
		}
		t.step, t.until, t.total, t.end, t.note = timerRunning, false, d, now.Add(d), note
		logf("started a timer %q for %v", note, d)
		return false, ""
	case timerRunning:
		switch resp.AID {
		case go3270.AIDPF3:
			return true, ""
		case go3270.AIDPF6:
		default:
			return false, ""
		}
		logf("stopped the timer %q with %v left", t.shownNote(), t.remaining(now))
	default:
		switch resp.AID {
		case go3270.AIDPF3:
			return true, ""
		case go3270.AIDEnter, go3270.AIDPF6:
		default:
			return false, ""
		}
	}
	// Stopped, or dismissed once done.
	if store != nil {
		if err := store.Clear(owner, t.kept()); err != nil {
			logf("%v", err)
			return true, "Could not stop your timer: " + err.Error() + "."
		}
	}
	return true, ""
}
