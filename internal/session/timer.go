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
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The timer (the TIMER command): asked how long, or until when (a time of
// day, as an alarm), it counts down in large digits filling the screen,
// redrawn each second, then flashes DONE until Enter is pressed.
const (
	timerField      = "timer"
	timerUntilField = "until"
	timerLabel      = "How long ===>"
	timerUntilLabel = "Or until ===>" // as long as timerLabel, so the fields line up
	timerRow        = 4
	timerUntilRow   = timerRow + 5
	timerWidth      = 12
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

	// typed and typedUntil are how long and until when, as typed, drawn
	// again when they will not do; onUntil puts the cursor on until when,
	// which is what the message is about.
	typed, typedUntil string
	onUntil           bool

	// until is set for a timer to a time of day, rather than for how long.
	until bool
	total time.Duration
	end   time.Time

	message string
	isError bool
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
	left := t.end.Sub(now)
	return max((left + time.Second - 1).Truncate(time.Second), 0)
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
			explanations []string
		}{
			{timerRow, timerLabel, timerField, t.typed, []string{
				"hh:mm, hours and minutes: 1:30 is an hour and a half, 0:05 five minutes.",
				"Or hours, minutes and seconds: 2h3m20s, 45m, 90s, 1h30m.",
			}},
			{timerUntilRow, timerUntilLabel, timerUntilField, t.typedUntil, []string{
				"A 24-hour time, hh:mm or hh:mm:ss: 17:45, 07:30:15 (or 1745).",
				"A time already past today is tomorrow's.",
			}},
		} {
			screen = append(screen,
				go3270.Field{Row: f.row, Col: 0, Color: go3270.Turquoise, Content: f.label},
				go3270.Field{
					Row: f.row, Col: fieldCol, Write: true, Name: f.name, Content: cutRunes(f.typed, timerWidth),
					Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
				},
				go3270.Field{Row: f.row, Col: fieldCol + 1 + timerWidth},
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
		color := go3270.Green
		switch {
		case left <= 10*time.Second:
			color = go3270.Red
		case left <= time.Minute:
			color = go3270.Yellow
		}
		screen = append(screen, bigTextFields(1, rows-4, cols, clockText(left, t.total), color, false)...)
		info := line{
			{Content: "Ends at " + t.end.Format("15:04:05"), Color: go3270.Blue},
			{Content: "of " + clockText(t.total, t.total), Color: go3270.Blue},
		}
		if t.until {
			info = line{{Content: "Until " + t.end.Format("15:04:05"), Color: go3270.Blue}}
			if !dayOf(t.end).Equal(dayOf(now)) {
				info = append(info, go3270.Field{Content: "tomorrow", Color: go3270.Blue})
			}
		}
		screen = append(screen, placeLine(rows-3, cols, info)...)
		screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Stop the timer", cols-1)})
		return screen, rows - 1, 0
	}

	// Done: every other second, the screen in red with DONE in it; or for
	// a user who avoids flashing, DONE in red, steadily.
	on := now.Unix()%2 == 0 && !prefs.NoFlash
	screen = append(screen, bigTextFields(1, rows-2, cols, "DONE", go3270.Red, on)...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.White, Intense: true, Content: truncate("Time is up. Press Enter.", cols-1)})
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
// to the dashboard. Asking, Enter starts the timer typed, and PF3 goes
// back; counting down, PF3 stops it; done, Enter (or PF3) goes back. A
// 3270 sends nothing for a key typed where there is no input field, only
// for Enter and the like, so Enter is what DONE asks for.
func (t *timerState) handle(resp go3270.Response, now time.Time, logf func(string, ...any)) (back bool) {
	t.message, t.isError = "", false
	if t.step == timerRunning && !now.Before(t.end) {
		t.step = timerDone
	}
	switch t.step {
	case timerAsk:
		switch resp.AID {
		case go3270.AIDPF3:
			return true
		case go3270.AIDEnter:
		default:
			return false
		}
		t.typed, t.typedUntil, t.onUntil = resp.Values[timerField], resp.Values[timerUntilField], false
		fail := func(onUntil bool, err error) bool {
			msg := err.Error()
			t.message, t.isError, t.onUntil = strings.ToUpper(msg[:1])+msg[1:]+".", true, onUntil
			return false
		}
		long, until := strings.TrimSpace(t.typed), strings.TrimSpace(t.typedUntil)
		switch {
		case long != "" && until != "":
			return fail(false, errors.New("type how long, or until when, not both"))
		case until != "":
			end, err := parseUntil(until, now)
			if err != nil {
				return fail(true, err)
			}
			t.step, t.until, t.total, t.end = timerRunning, true, end.Sub(now), end
			logf("started a timer until %s, in %v", end.Format("15:04:05"), end.Sub(now).Round(time.Second))
			return false
		}
		d, err := parseTimer(long)
		if err != nil {
			return fail(false, err)
		}
		t.step, t.total, t.end = timerRunning, d, now.Add(d)
		logf("started a timer for %v", d)
		return false
	case timerRunning:
		if resp.AID == go3270.AIDPF3 {
			logf("stopped a timer with %v left", t.remaining(now))
			return true
		}
		return false
	}
	return resp.AID == go3270.AIDEnter || resp.AID == go3270.AIDPF3
}
