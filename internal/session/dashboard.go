package session

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// Fixed rows at the top of the screen. Everything from firstBodyRow down to
// the message row is shared between the agenda and the task list.
const (
	titleRow     = 0
	busyRow      = 1
	firstBodyRow = 3
)

// soonThreshold is how close a meeting's start must be for it to be called
// out as coming up.
const soonThreshold = 15 * time.Minute

// line is one row's worth of fields, each placed after the previous one.
type line []go3270.Field

// buildDashboard renders the dashboard for a rows x cols screen. page selects
// which page of tasks is shown; it is clamped to the pages that exist, and
// the page actually shown is returned with the page count.
func buildDashboard(rows, cols int, v view, page int, luName string) (screen go3270.Screen, shownPage, totalPages int) {
	// The row above the help line is left blank, for symmetry with the
	// spacer between the sections.
	messageRow, helpRow := rows-2, rows-1

	// Rows available between the fixed top and the message row: two
	// section headers and a blank spacer between the sections come out of
	// it; what remains is split between agenda and task lines.
	body := messageRow - firstBodyRow
	content := body - 3

	agendaLines := agendaContent(v, content*2/5, cols)
	taskRows := max(content-len(agendaLines), 1)

	totalPages = max((len(v.Tasks)+taskRows-1)/taskRows, 1)
	shownPage = min(max(page, 0), totalPages-1)
	start := shownPage * taskRows
	end := min(start+taskRows, len(v.Tasks))
	pageTasks := v.Tasks[start:end]

	clock := v.Now.Format("Mon Jan 2 15:04")
	screen = go3270.Screen{
		{Row: titleRow, Col: 0, Intense: true, Color: go3270.White, Content: "ADHD DASHBOARD"},
		{Row: titleRow, Col: max(cols-len(clock)-2, 16), Intense: true, Color: go3270.White, Content: clock},
	}
	screen = append(screen, placeLine(busyRow, cols, busyLine(v))...)

	row := firstBodyRow
	screen = append(screen, placeLine(row, cols, agendaHeader(v))...)
	row++
	for _, l := range agendaLines {
		screen = append(screen, placeLine(row, cols, l)...)
		row++
	}
	row++ // spacer

	screen = append(screen, placeLine(row, cols, taskHeader(v, shownPage, totalPages))...)
	row++
	for _, t := range pageTasks {
		screen = append(screen, placeLine(row, cols, taskLine(t))...)
		row++
	}
	if v.TasksErr == nil && len(v.Tasks) == 0 {
		screen = append(screen, placeLine(row, cols, line{{Content: "Nothing to do. Really."}})...)
	}

	help := "PF3 Exit  PF7 Up  PF8 Down  Enter Refresh"
	if luName != "" {
		help += "   LU " + luName
	}
	screen = append(screen,
		go3270.Field{Row: helpRow, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)},
	)
	return screen, shownPage, totalPages
}

// placeLine positions a line's fields on row, one after the other. Each
// field's attribute byte takes a column of its own before its content, and
// content that would run past the right edge is cut off.
func placeLine(row, cols int, l line) []go3270.Field {
	var out []go3270.Field
	col := 0
	for _, f := range l {
		room := cols - col - 1
		if room <= 0 {
			break
		}
		f.Row, f.Col = row, col
		f.Content = truncate(f.Content, room)
		out = append(out, f)
		col += 1 + utf8.RuneCountInString(f.Content)
	}
	return out
}

// busyLine is the busy indicator's state: a reverse-video badge, then when
// the next meeting is.
func busyLine(v view) line {
	if !v.BusyEnabled {
		return line{{Content: "Busy indicator not configured (-busy-url)", Color: go3270.Blue}}
	}
	s := v.Busy
	if !s.Connected {
		msg := "Busy indicator " + s.Problem
		if !s.Updated.IsZero() {
			msg += fmt.Sprintf(" (was %s at %s)", s.Light, s.Updated.Format("15:04"))
		}
		return line{
			{Content: " ???? ", Color: go3270.Yellow, Highlighting: go3270.ReverseVideo},
			{Content: msg, Color: go3270.Yellow},
		}
	}

	var badge go3270.Field
	switch s.Light {
	case "red":
		badge = go3270.Field{Content: " BUSY ", Color: go3270.Red, Highlighting: go3270.ReverseVideo, Intense: true}
	case "green":
		badge = go3270.Field{Content: " AVAILABLE ", Color: go3270.Green, Highlighting: go3270.ReverseVideo}
	default:
		badge = go3270.Field{Content: " NOT BUSY ", Color: go3270.Turquoise, Highlighting: go3270.ReverseVideo}
	}
	return line{badge, {Content: nextMeetingText(s, v.Now)}}
}

// nextMeetingText describes the feed's minutes-to-next. The indicator only
// publishes on its own refresh interval, so the time since the last message
// is taken off to keep the countdown current in between.
func nextMeetingText(s busy.Status, now time.Time) string {
	if s.MinutesToNext == nil {
		return "No more meetings today"
	}
	minutes := *s.MinutesToNext - int(now.Sub(s.Updated)/time.Minute)
	if minutes <= 0 {
		return "Meeting now"
	}
	return "Next meeting in " + duration(time.Duration(minutes)*time.Minute)
}

// agendaHeader titles the agenda, noting a failed or missing calendar.
func agendaHeader(v view) line {
	l := line{{Content: "AGENDA", Color: go3270.Turquoise, Intense: true}}
	switch {
	case !v.AgendaEnabled:
	case v.Agenda.Err != nil && v.Agenda.Fetched.IsZero():
		l = append(l, go3270.Field{Content: "calendar unavailable: " + v.Agenda.Err.Error(), Color: go3270.Red})
	case v.Agenda.Err != nil:
		l = append(l, go3270.Field{
			Content: fmt.Sprintf("stale, from %s: %s", v.Agenda.Fetched.Format("15:04"), v.Agenda.Err),
			Color:   go3270.Yellow,
		})
	}
	return l
}

// agendaContent is the agenda's lines, at most limit of them (but always at
// least one): today's and tomorrow's meetings that have not ended, the
// last line summarizing any that did not fit.
func agendaContent(v view, limit, cols int) []line {
	limit = max(limit, 1)
	if !v.AgendaEnabled {
		return []line{{{Content: "No calendar configured (-calendar)", Color: go3270.Blue}}}
	}
	upcoming := v.Agenda.Upcoming(v.Now)
	if len(upcoming) == 0 {
		if v.Agenda.Fetched.IsZero() {
			return []line{{{Content: "Waiting for calendar", Color: go3270.Blue}}}
		}
		return []line{{{Content: "Nothing left on the calendar today or tomorrow"}}}
	}

	shown := upcoming
	if len(shown) > limit {
		shown = shown[:limit-1]
	}

	var out []line
	today := dayOf(v.Now)
	lastDay := time.Time{}
	nextMarked := false
	for _, e := range shown {
		day := dayOf(e.Start)
		if day.Before(today) {
			day = today // began before today; still running
		}
		dayLabel := ""
		if !day.Equal(lastDay) {
			dayLabel = dayName(day, today)
			lastDay = day
		}

		when := e.Start.Format("15:04") + "-" + e.End.Format("15:04")
		if e.AllDay {
			when = "all day"
		}

		var countdown string
		color, intense := go3270.Green, false
		switch {
		case e.AllDay:
			color = go3270.Blue
		case !e.Start.After(v.Now):
			countdown, color, intense = "NOW", go3270.Yellow, true
		case !nextMarked:
			nextMarked = true
			countdown = "in " + duration(e.Start.Sub(v.Now))
			if e.Start.Sub(v.Now) <= soonThreshold {
				color, intense = go3270.Yellow, true
			} else {
				color = go3270.White
			}
		}
		if !day.Equal(today) {
			color, intense = go3270.Blue, false
		}

		text := fmt.Sprintf("%-5s %-11s %-8s %s", dayLabel, when, countdown, e.Summary)
		if e.Summary == "" {
			text += "(No title)"
		}
		out = append(out, line{{Content: text, Color: color, Intense: intense}})
	}
	if len(shown) < len(upcoming) {
		out = append(out, line{{Content: fmt.Sprintf("      ... and %d more", len(upcoming)-len(shown)), Color: go3270.Blue}})
	}
	return out
}

// taskHeader titles the task list with its count and page.
func taskHeader(v view, page, totalPages int) line {
	l := line{{Content: "TASKS", Color: go3270.Turquoise, Intense: true}}
	if v.TasksErr != nil {
		return append(l, go3270.Field{Content: v.TasksErr.Error(), Color: go3270.Red})
	}
	info := fmt.Sprintf("%d open", len(v.Tasks))
	if totalPages > 1 {
		info += fmt.Sprintf(", page %d/%d", page+1, totalPages)
	}
	return append(l, go3270.Field{Content: info, Color: go3270.Blue})
}

// taskLine is one task: its number and tags, then its title.
func taskLine(t tasks.Task) line {
	prefix := fmt.Sprintf("%4d", t.Number)
	if len(t.Tags) > 0 {
		prefix += " [" + strings.Join(t.Tags, "] [") + "]"
	}
	return line{
		{Content: prefix, Color: go3270.Turquoise},
		{Content: t.Title, Color: go3270.Green, Intense: true},
	}
}

// dayOf is midnight at the start of t's day.
func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// dayName labels day relative to today.
func dayName(day, today time.Time) string {
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, 1)):
		return "Tmrw"
	default:
		return day.Format("Mon")
	}
}

// duration renders d in whole minutes as "45m" or "2h05".
func duration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02d", minutes/60, minutes%60)
}

// truncate shortens s to at most width runes, marking a cut with a trailing
// ">". A plain ASCII marker is used because an ellipsis has no EBCDIC
// mapping and some clients draw the substitute as a box.
func truncate(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	return string(r[:width-1]) + ">"
}
