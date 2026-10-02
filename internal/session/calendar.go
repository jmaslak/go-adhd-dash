package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
)

// Calendar screen layout. The month grid is on the left, one cell per day:
// an attribute byte, then the two-digit day number.
// Weeks start on Sunday. The selected day's events are listed to the right.
const (
	calMonthRow     = 2
	calWeekdayRow   = 3
	calFirstWeekRow = 4
	calCellWidth    = 3
	calGridWidth    = 7 * calCellWidth
	calDayCol       = calGridWidth + 3
	calDayRow       = 2

	// calClockRow is the first world clock, a blank row below the most
	// weeks a month can take, so the clocks stay put from month to month.
	calClockRow = calFirstWeekRow + 6 + 1
)

// worldClocks are the places whose time is listed under the month grid.
var worldClocks = []struct{ name, zone string }{
	{"Denver", "America/Denver"},
	{"Los Angeles", "America/Los_Angeles"},
	{"New York", "America/New_York"},
	{"Japan", "Asia/Tokyo"},
	{"Amsterdam", "Europe/Amsterdam"},
	{"UTC", "UTC"},
}

// calendarFetchTimeout bounds reading a month, which holds up the session.
const calendarFetchTimeout = 30 * time.Second

// calendarState is one session's place in the calendar screen and the month
// of events read for it.
type calendarState struct {
	// month is midnight on the first of the month shown; selected is
	// midnight on the day whose events are listed.
	month, selected time.Time

	// events are loadedMonth's, read at loaded from source; err is why the
	// last read failed.
	source              *agenda.Cache
	loadedMonth, loaded time.Time
	events              []agenda.Event
	err                 error
}

// goTo selects day and shows its month.
func (c *calendarState) goTo(day time.Time) {
	c.selected = dayOf(day)
	c.month = monthOf(day)
}

// changeMonth shows the month delta months away, selecting today if it is in
// that month and the first otherwise.
func (c *calendarState) changeMonth(delta int, now time.Time) {
	month := c.month.AddDate(0, delta, 0)
	if monthOf(now).Equal(month) {
		c.goTo(now)
	} else {
		c.goTo(month)
	}
}

// load reads the shown month's events unless they were read for that month
// from cache less than maxAge ago. A failed read of the month already loaded
// keeps its events, and is not retried until maxAge has passed. Events read
// from another cache, as a user's is replaced when they choose other
// calendars, are dropped.
func (c *calendarState) load(cache *agenda.Cache, maxAge time.Duration, now time.Time) {
	if cache != c.source {
		c.source, c.loadedMonth, c.events, c.err = cache, time.Time{}, nil, nil
	}
	if cache == nil || (c.loadedMonth.Equal(c.month) && now.Sub(c.loaded) < maxAge) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), calendarFetchTimeout)
	defer cancel()
	events, err := cache.Fetch(ctx, c.month, c.month.AddDate(0, 1, 0))
	if !c.loadedMonth.Equal(c.month) {
		c.events = nil
	}
	c.loadedMonth, c.loaded, c.err = c.month, now, err
	if err == nil {
		c.events = events
	}
}

// calendarView is everything one calendar redraw shows.
type calendarView struct {
	Now         time.Time
	AutoRefresh bool

	// AgendaEnabled is false when the user has no calendar to show.
	AgendaEnabled bool

	// Month is midnight on the first of the month shown; Selected is
	// midnight on the day whose events are listed.
	Month, Selected time.Time

	// Events are the month's; Err is why they could not be read.
	Events []agenda.Event
	Err    error
}

// view is what the state shows at now.
func (c *calendarState) view(now time.Time, autoRefresh, agendaEnabled bool) calendarView {
	return calendarView{
		Now: now, AutoRefresh: autoRefresh, AgendaEnabled: agendaEnabled,
		Month: c.month, Selected: c.selected, Events: c.events, Err: c.err,
	}
}

// buildCalendar renders the calendar screen for a rows x cols screen, and
// where the cursor goes: on the selected day. Every field is protected, so a
// timed redraw can write it over the last one without erasing the screen.
func buildCalendar(rows, cols int, v calendarView) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "CALENDAR", v.Now, v.AutoRefresh)

	heading := v.Month.Format("January 2006")
	screen = append(screen, go3270.Field{
		Row: calMonthRow, Col: max((calGridWidth-len(heading))/2, 0),
		Intense: true, Color: go3270.White, Content: heading,
	})
	screen = append(screen, go3270.Field{
		Row: calWeekdayRow, Col: 0, Color: go3270.Turquoise,
		Content: "Su Mo Tu We Th Fr Sa",
	})

	// Every cell of every week gets a field, blank outside the month, so
	// that a highlighted day's attribute stops at its own cell; a trailing
	// attribute ends Saturday's.
	today := dayOf(v.Now)
	first, days := calendarOffset(v.Month), daysIn(v.Month)
	weeks := (first + days + 6) / 7
	cursorRow, cursorCol = calFirstWeekRow, 1
	for cell := range weeks * 7 {
		row, col := calFirstWeekRow+cell/7, cell%7*calCellWidth
		f := go3270.Field{Row: row, Col: col, Content: strings.Repeat(" ", calCellWidth-1)}
		if d := cell - first + 1; d >= 1 && d <= days {
			day := v.Month.AddDate(0, 0, d-1)
			f.Content = fmt.Sprintf("%2d", d)
			f.Color = go3270.Green
			if day.Equal(today) {
				f.Color, f.Intense = go3270.White, true
			}
			if day.Equal(v.Selected) {
				f.Highlighting = go3270.ReverseVideo
				cursorRow, cursorCol = row, col+1
			}
		}
		screen = append(screen, f)
		if cell%7 == 6 {
			screen = append(screen, go3270.Field{Row: row, Col: calGridWidth})
		}
	}

	screen = append(screen, clockFields(rows, v.Now)...)
	screen = append(screen, dayFields(rows, cols, v)...)

	if v.Err != nil {
		screen = append(screen, placeLine(rows-2, cols, line{{Content: "calendar unavailable: " + agendaErrorText(v.Err), Color: go3270.Red}})...)
	}
	screen = append(screen, go3270.Field{
		Row: rows - 1, Col: 0, Color: go3270.Blue,
		Content: truncate("PF3=Back PF4=Today PF5=Auto PF7=Prev month PF8=Next month Enter=Pick day", cols-1),
	})
	return screen, cursorRow, cursorCol
}

// clockFields lists the world clocks' times at now under the month grid, as
// many as fit above the message row. A zone that cannot be loaded shows
// "--:--".
func clockFields(rows int, now time.Time) []go3270.Field {
	var out []go3270.Field
	for i, c := range worldClocks {
		row := calClockRow + i
		if row > rows-3 {
			break
		}
		t := "--:--"
		if loc, err := time.LoadLocation(c.zone); err == nil {
			t = now.In(loc).Format("15:04")
		}
		out = append(out, placeLineAt(row, 0, calGridWidth+1, line{
			{Content: fmt.Sprintf("%-11s", c.name), Color: go3270.Turquoise},
			{Content: t, Color: go3270.White},
		})...)
	}
	return out
}

// dayFields lists the selected day's events right of the grid, down to the
// row above the message row, the last line summarizing any that did not fit.
func dayFields(rows, cols int, v calendarView) []go3270.Field {
	heading := v.Selected.Format("Monday, January 2, 2006")
	if v.Selected.Equal(dayOf(v.Now)) {
		heading += " (today)"
	}
	out := placeLineAt(calDayRow, calDayCol, cols, line{{Content: heading, Intense: true, Color: go3270.White}})

	var lines []line
	switch {
	case !v.AgendaEnabled:
		lines = []line{
			{{Content: "No Google calendar connected.", Color: go3270.Blue}},
			{{Content: "Type GOOGLE on the dashboard to connect one.", Color: go3270.Blue}},
		}
	default:
		for _, e := range v.Events {
			if overlapsDay(e, v.Selected) && !isHomeDay(e) {
				lines = append(lines, dayEventLine(e, v.Selected, v.Now))
			}
		}
		if len(lines) == 0 && v.Err == nil {
			lines = []line{{{Content: "Nothing on the calendar"}}}
		}
	}

	limit := max(rows-3-(calDayRow+1), 1)
	if len(lines) > limit {
		more := len(lines) - (limit - 1)
		lines = append(lines[:limit-1], line{{Content: fmt.Sprintf("... and %d more", more), Color: go3270.Blue}})
	}
	for i, l := range lines {
		out = append(out, placeLineAt(calDayRow+1+i, calDayCol, cols, l)...)
	}
	return out
}

// dayEventLine is one of day's events: its times, clipped to the day, and
// its title, colored by whether it is over, under way or still to come.
func dayEventLine(e agenda.Event, day, now time.Time) line {
	when := "all day"
	if !e.AllDay {
		start, end := e.Start, e.End
		if start.Before(day) {
			start = day
		}
		endText := end.Format("15:04")
		if next := day.AddDate(0, 0, 1); !end.Before(next) {
			endText = "24:00"
		}
		when = start.Format("15:04") + "-" + endText
	}

	color, intense := go3270.Green, false
	switch {
	case e.AllDay:
		color = go3270.Blue
	case !e.End.After(now):
		color = go3270.Blue
	case !e.Start.After(now):
		color, intense = go3270.Yellow, true
	}
	return line{{Content: fmt.Sprintf("%-11s %s", when, meetingName(e)), Color: color, Intense: intense}}
}

// isHomeDay reports whether e is an all-day event titled "Home", as Google
// Calendar's working-location entries for working from home are. Matched by
// title, since the calendar client does not pass on the event type.
func isHomeDay(e agenda.Event) bool {
	return e.AllDay && strings.EqualFold(strings.TrimSpace(e.Summary), "Home")
}

// calendarDayAt is the day in month whose cell is at row, col, as reported
// for the cursor, and whether there is one.
func calendarDayAt(month time.Time, row, col int) (time.Time, bool) {
	week := row - calFirstWeekRow
	if week < 0 || col < 0 || col >= calGridWidth {
		return time.Time{}, false
	}
	d := week*7 + col/calCellWidth - calendarOffset(month) + 1
	if d < 1 || d > daysIn(month) {
		return time.Time{}, false
	}
	return month.AddDate(0, 0, d-1), true
}

// overlapsDay reports whether e takes up any of day.
func overlapsDay(e agenda.Event, day time.Time) bool {
	return e.Start.Before(day.AddDate(0, 0, 1)) && e.End.After(day)
}

// calendarOffset is the grid cell of month's first day: its weekday, since
// weeks start on Sunday.
func calendarOffset(month time.Time) int {
	return int(month.Weekday())
}

// daysIn is the number of days in month.
func daysIn(month time.Time) int {
	return month.AddDate(0, 1, -1).Day()
}

// monthOf is midnight at the start of the first of t's month.
func monthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
