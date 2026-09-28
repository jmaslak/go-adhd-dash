package session

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// Fixed rows at the top of the screen: the title, then the busy indicator's
// banner with its details on the row below. Everything from firstBodyRow
// down to the spacer above the bottom banner is shared between the agenda
// and the task list.
const (
	titleRow     = 0
	busyRow      = 1
	firstBodyRow = 4
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
	// The busy banner is repeated just above the help line, with a blank
	// spacer above it, for symmetry with the spacer between the sections.
	spacerRow, bottomBannerRow, helpRow := rows-3, rows-2, rows-1

	// Rows available between the fixed top and the bottom spacer: two
	// section headers and a blank spacer between the sections come out of
	// it; what remains is split between agenda and task lines.
	body := spacerRow - firstBodyRow
	content := body - 3

	// The first page shows the whole agenda, even if that leaves no room
	// for tasks; later pages show two fifths of it and more tasks.
	firstAgenda := agendaContent(v, max(content*2/5, min(len(agendaEvents(v)), content)), cols)
	restAgenda := agendaContent(v, content*2/5, cols)
	firstRows := max(content-len(firstAgenda), 0)
	restRows := max(content-len(restAgenda), 1)

	totalPages = 1
	if extra := len(v.Tasks) - firstRows; extra > 0 {
		totalPages += (extra + restRows - 1) / restRows
	}
	shownPage = min(max(page, 0), totalPages-1)
	agendaLines, start, taskRows := firstAgenda, 0, firstRows
	if shownPage > 0 {
		agendaLines, start, taskRows = restAgenda, firstRows+(shownPage-1)*restRows, restRows
	}
	end := min(start+taskRows, len(v.Tasks))
	pageTasks := v.Tasks[start:end]

	screen = titleFields(cols, "EXECUTIVE FUNCTION DASHBOARD", v.Now, v.AutoRefresh)
	badge, detail := busyState(v)
	if badge.Content != "" {
		screen = append(screen, banner(busyRow, cols, badge), banner(bottomBannerRow, cols, badge))
		screen = append(screen, placeLine(busyRow+1, cols, detail)...)
	} else {
		screen = append(screen, placeLine(busyRow, cols, detail)...)
	}

	row := firstBodyRow
	screen = append(screen, placeLine(row, cols, agendaHeader(v))...)
	row++
	for _, l := range agendaLines {
		screen = append(screen, placeLine(row, cols, l)...)
		row++
	}
	row++ // spacer

	screen = append(screen, placeLine(row, cols, taskHeader(v, shownPage, totalPages, len(pageTasks)))...)
	row++
	for _, t := range pageTasks {
		screen = append(screen, placeLine(row, cols, taskLine(t))...)
		row++
	}
	if v.TasksErr == nil && len(v.Tasks) == 0 {
		screen = append(screen, placeLine(row, cols, line{{Content: "Nothing to do. Really."}})...)
	}

	if v.Message != "" {
		// cols-1 keeps clear of the bottom banner's attribute byte at the
		// end of this row.
		screen = append(screen, placeLine(spacerRow, cols-1, line{{Content: v.Message, Color: go3270.Red, Intense: true}})...)
	}

	help := "PF3 Exit  PF5 Auto  PF7 Up  PF8 Down  PF9 Cal  Enter Rfrsh"
	if v.BusyControl {
		help = "PF1 Busy  PF2 Off  " + help
	}
	// The LU name is only for reference, so it is left off when it would
	// not fit.
	if lu := "   LU " + luName; luName != "" && len(help)+len(lu) <= cols-1 {
		help += lu
	}
	screen = append(screen,
		go3270.Field{Row: helpRow, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)},
	)
	return screen, shownPage, totalPages
}

// titleFields is the title row: title on the left, the clock on the right,
// and while auto-refresh is on a marker left of the clock, when there is room
// for it after the title.
func titleFields(cols int, title string, now time.Time, autoRefresh bool) go3270.Screen {
	clock := now.Format("Mon Jan 2 15:04:05")
	clockCol := max(cols-len(clock)-2, len(title)+2)
	screen := go3270.Screen{
		{Row: titleRow, Col: 0, Intense: true, Color: go3270.White, Content: title},
		{Row: titleRow, Col: clockCol, Intense: true, Color: go3270.White, Content: clock},
	}
	const autoMarker = "AUTO-REFRESH"
	if autoCol := clockCol - len(autoMarker) - 2; autoRefresh && autoCol > len(title) {
		screen = append(screen, go3270.Field{Row: titleRow, Col: autoCol, Color: go3270.Turquoise, Content: autoMarker})
	}
	return screen
}

// placeLine positions a line's fields on row, one after the other. Each
// field's attribute byte takes a column of its own before its content, and
// content that would run past the right edge is cut off.
func placeLine(row, cols int, l line) []go3270.Field {
	return placeLineAt(row, 0, cols, l)
}

// placeLineAt is placeLine starting at col rather than the left edge.
func placeLineAt(row, col, cols int, l line) []go3270.Field {
	var out []go3270.Field
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

// busyState is the busy indicator's state: a badge naming it, shown as a
// banner, and a detail line to go under it. The badge is empty when there is
// no indicator, and the detail line then says so.
func busyState(v view) (badge go3270.Field, detail line) {
	if !v.BusyEnabled {
		return go3270.Field{}, line{{Content: "Busy indicator not configured (-busy-url)", Color: go3270.Blue}}
	}
	s := v.Busy
	if !s.Connected {
		msg := "Busy indicator " + s.Problem
		if !s.Updated.IsZero() {
			msg += fmt.Sprintf(" (was %s at %s)", s.Light, s.Updated.Format("15:04"))
		}
		return go3270.Field{Content: "????", Color: go3270.Yellow, Highlighting: go3270.ReverseVideo},
			line{{Content: msg, Color: go3270.Yellow}}
	}

	switch s.Light {
	case "red":
		badge = go3270.Field{Content: "** BUSY **", Color: go3270.Red, Highlighting: go3270.ReverseVideo}
	case "green":
		badge = go3270.Field{Content: "** AVAILABLE **", Color: go3270.Green, Highlighting: go3270.ReverseVideo}
	default:
		badge = go3270.Field{Content: "** NOT BUSY **", Color: go3270.Green}
	}
	return badge, line{{Content: nextMeetingText(v)}}
}

// banner fills all of row with f's content centered. Its attribute byte goes
// in the last column of the row above, so that reverse video reaches column 0
// too; the field placed at the start of the row below ends it, so that row
// must have one.
func banner(row, cols int, f go3270.Field) go3270.Field {
	text := truncate(f.Content, cols)
	pad := cols - utf8.RuneCountInString(text)
	f.Row, f.Col = row-1, cols-1
	f.Content = strings.Repeat(" ", pad/2) + text + strings.Repeat(" ", pad-pad/2)
	return f
}

// nextMeetingText describes the meeting under way or the next one. With a
// calendar it is read from there, by the same rules as the agenda, so the two
// agree: the indicator's feed counts to its next calendar entry of any kind,
// all-day ones included. Without one it is the feed's minutes-to-next, less
// the time since the feed's last message to keep it current in between.
func nextMeetingText(v view) string {
	if v.AgendaEnabled && !v.Agenda.Fetched.IsZero() {
		if names := meetingsNow(v); len(names) > 0 {
			return "Meeting now: " + strings.Join(names, ", ")
		}
		for _, e := range v.Agenda.Upcoming(v.Now) {
			if isMeeting(e) && e.Start.After(v.Now) {
				return "Next meeting in " + duration(e.Start.Sub(v.Now)) + ": " + meetingName(e)
			}
		}
		return "No more meetings today or tomorrow"
	}

	s := v.Busy
	if s.MinutesToNext == nil {
		return "No more meetings today"
	}
	until := s.Updated.Add(time.Duration(*s.MinutesToNext) * time.Minute).Sub(v.Now)
	if until <= 0 {
		return "Meeting now"
	}
	return "Next meeting in " + duration(until)
}

// meetingsNow names the calendar's timed meetings under way.
func meetingsNow(v view) []string {
	if !v.AgendaEnabled {
		return nil
	}
	var names []string
	for _, e := range v.Agenda.Upcoming(v.Now) {
		if isMeeting(e) && !e.Start.After(v.Now) {
			names = append(names, meetingName(e))
		}
	}
	return names
}

// outOfOfficeTitle matches the titles of out-of-office events. The calendar
// client does not pass on Google's event type, so the title is all there is
// to go on.
var outOfOfficeTitle = regexp.MustCompile(`(?i)out of (the )?office|\booo\b`)

// isMeeting reports whether e belongs on the dashboard: all-day and
// out-of-office events are not meetings.
func isMeeting(e agenda.Event) bool {
	return !e.AllDay && !outOfOfficeTitle.MatchString(e.Summary)
}

// meetingName is e's title, or a stand-in when it has none, after its
// calendar alias in brackets when it has one.
func meetingName(e agenda.Event) string {
	name := e.Summary
	if name == "" {
		name = "(No title)"
	}
	if e.Calendar != "" {
		name = "[" + e.Calendar + "] " + name
	}
	return name
}

// agendaHeader titles the agenda, noting a failed or missing calendar.
func agendaHeader(v view) line {
	l := line{{Content: "AGENDA (next 24 hours)", Color: go3270.Turquoise, Intense: true}}
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

// agendaWindow is how far ahead the agenda looks.
const agendaWindow = 24 * time.Hour

// agendaEvents are the meetings under way or starting within agendaWindow.
func agendaEvents(v view) []agenda.Event {
	var out []agenda.Event
	for _, e := range v.Agenda.Upcoming(v.Now) {
		if isMeeting(e) && e.Start.Before(v.Now.Add(agendaWindow)) {
			out = append(out, e)
		}
	}
	return out
}

// agendaContent is the agenda's lines, at most limit of them (but always at
// least one): the agendaEvents, the last line summarizing any that did not
// fit.
func agendaContent(v view, limit, cols int) []line {
	limit = max(limit, 1)
	if !v.AgendaEnabled {
		return []line{{{Content: "No calendar configured (-calendar)", Color: go3270.Blue}}}
	}
	upcoming := agendaEvents(v)
	if len(upcoming) == 0 {
		if v.Agenda.Fetched.IsZero() {
			return []line{{{Content: "Waiting for calendar", Color: go3270.Blue}}}
		}
		return []line{{{Content: "Nothing on the calendar in the next 24 hours"}}}
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

		var countdown string
		color, intense := go3270.Green, false
		switch {
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

		text := fmt.Sprintf("%-5s %-11s %-8s %s", dayLabel, when, countdown, meetingName(e))
		out = append(out, line{{Content: text, Color: color, Intense: intense}})
	}
	if len(shown) < len(upcoming) {
		out = append(out, line{{Content: fmt.Sprintf("      ... and %d more", len(upcoming)-len(shown)), Color: go3270.Blue}})
	}
	return out
}

// taskHeader titles the task list with its count and page, pointing to the
// next page when the agenda has left this one no room for tasks.
func taskHeader(v view, page, totalPages, onPage int) line {
	l := line{{Content: "TASKS", Color: go3270.Turquoise, Intense: true}}
	if v.TasksErr != nil {
		return append(l, go3270.Field{Content: v.TasksErr.Error(), Color: go3270.Red})
	}
	info := fmt.Sprintf("%d open", len(v.Tasks))
	if totalPages > 1 {
		info += fmt.Sprintf(", page %d/%d", page+1, totalPages)
	}
	if onPage == 0 && len(v.Tasks) > 0 {
		info += ", PF8 to see them"
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
