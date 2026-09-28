package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
)

func sampleCalendar() calendarView {
	v := sampleView(0)
	return calendarView{
		Now: now, AutoRefresh: true, AgendaEnabled: true,
		Month: monthOf(now), Selected: dayOf(now), Events: v.Agenda.Events,
	}
}

func TestCalendarScreen(t *testing.T) {
	s, crow, ccol := buildCalendar(24, 80, sampleCalendar())
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"CALENDAR", "AUTO-REFRESH", "September 2026", "Su Mo Tu We Th Fr Sa",
		"Sunday, September 27, 2026 (today)",
		"all day     Holiday", "09:30-10:30 Running", "10:15-10:45 Soon",
		"PF3=Back",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Tomorrow") {
		t.Errorf("screen lists another day's event:\n%s", text)
	}

	// September 2026 starts on a Tuesday, so the 27th opens the fifth week.
	if week := rows[calFirstWeekRow+4]; week != " 27 28 29 30" {
		t.Errorf("fifth week is %q", week)
	}
	if week := rows[calFirstWeekRow]; !strings.HasPrefix(week, "        1  2  3 ") {
		t.Errorf("first week is %q", week)
	}
	if crow != calFirstWeekRow+4 || ccol != 1 {
		t.Errorf("cursor at %d,%d, want on the 27th at %d,1", crow, ccol, calFirstWeekRow+4)
	}
}

func TestCalendarDayList(t *testing.T) {
	v := sampleCalendar()
	v.Events = nil
	for i := range 40 {
		start := dayOf(now).Add(time.Duration(i) * 15 * time.Minute)
		v.Events = append(v.Events, agenda.Event{Summary: fmt.Sprint("m", i), Start: start, End: start.Add(15 * time.Minute)})
	}
	s, _, _ := buildCalendar(24, 80, v)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, "00:00-00:15 m0") || !strings.Contains(text, "... and 23 more") {
		t.Errorf("long day not summarized:\n%s", text)
	}

	v.Events = []agenda.Event{
		{Summary: "Home", Start: dayOf(now), End: dayOf(now).AddDate(0, 0, 1), AllDay: true},
		{Summary: "Home", Start: dayOf(now).Add(9 * time.Hour), End: dayOf(now).Add(10 * time.Hour)},
		{Summary: "Homework", Start: dayOf(now), End: dayOf(now).AddDate(0, 0, 1), AllDay: true},
	}
	s, _, _ = buildCalendar(24, 80, v)
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	if strings.Contains(text, "all day     Home\n") || !strings.Contains(text, "09:00-10:00 Home") || !strings.Contains(text, "all day     Homework") {
		t.Errorf("want only the all-day Home event left off:\n%s", text)
	}

	v.Events = []agenda.Event{{Summary: "Overnight", Start: dayOf(now).Add(-2 * time.Hour), End: dayOf(now).Add(26 * time.Hour)}}
	s, _, _ = buildCalendar(24, 80, v)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "00:00-24:00 Overnight") {
		t.Errorf("event spanning the day not clipped to it:\n%s", text)
	}

	v.Events, v.AgendaEnabled = nil, false
	s, _, _ = buildCalendar(24, 80, v)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "No calendar configured") {
		t.Errorf("missing calendar not reported:\n%s", text)
	}
}

func TestCalendarAliases(t *testing.T) {
	v := sampleView(0)
	for i := range v.Agenda.Events {
		v.Agenda.Events[i].Calendar = "work"
	}
	v.Agenda.Events[2].Calendar = "work,home" // Soon, on both calendars

	s, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"NOW      [work] Running", "[work,home] Soon", "Meeting now: [work] Running"} {
		if !strings.Contains(text, want) {
			t.Errorf("dashboard lacks %q:\n%s", want, text)
		}
	}

	cv := sampleCalendar()
	cv.Events = v.Agenda.Events
	s, _, _ = buildCalendar(24, 80, cv)
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"all day     [work] Holiday", "09:30-10:30 [work] Running", "10:15-10:45 [work,home] Soon"} {
		if !strings.Contains(text, want) {
			t.Errorf("calendar lacks %q:\n%s", want, text)
		}
	}
}

func TestCalendarDayAt(t *testing.T) {
	month := monthOf(now) // September 2026, starting on a Tuesday
	for _, c := range []struct {
		row, col int
		want     int // day of month, 0 for none
	}{
		{calFirstWeekRow, 6, 1}, {calFirstWeekRow, 8, 1}, {calFirstWeekRow, 9, 2},
		{calFirstWeekRow, 5, 0}, {calFirstWeekRow + 4, 0, 27}, {calFirstWeekRow + 4, 9, 30},
		{calFirstWeekRow + 4, 12, 0}, {calFirstWeekRow + 1, calGridWidth, 0}, {calWeekdayRow, 8, 0},
	} {
		day, ok := calendarDayAt(month, c.row, c.col)
		if got := map[bool]int{true: day.Day(), false: 0}[ok]; got != c.want {
			t.Errorf("row %d col %d: got day %d, want %d", c.row, c.col, got, c.want)
		}
	}

	// Wherever the selected day falls, the cursor placed on it reads back
	// as that day.
	for m := range 12 {
		v := sampleCalendar()
		v.Month = time.Date(2026, time.Month(m+1), 1, 0, 0, 0, 0, time.UTC)
		for d := range daysIn(v.Month) {
			v.Selected = v.Month.AddDate(0, 0, d)
			_, row, col := buildCalendar(24, 80, v)
			if got, ok := calendarDayAt(v.Month, row, col); !ok || !got.Equal(v.Selected) {
				t.Fatalf("%s: cursor at %d,%d reads back as %s, %v", v.Selected.Format("Jan 2"), row, col, got, ok)
			}
		}
	}
}

// countingSource hands out its events, or its error, counting the reads.
type countingSource struct {
	events []agenda.Event
	err    error
	reads  int
}

func (s *countingSource) Events(context.Context, time.Time, time.Time) ([]agenda.Event, error) {
	s.reads++
	return s.events, s.err
}

func TestCalendarLoad(t *testing.T) {
	src := &countingSource{events: []agenda.Event{{Summary: "a", Start: now, End: now.Add(time.Hour)}}}
	cache := agenda.NewCache(src)
	var c calendarState
	c.goTo(now)

	c.load(cache, 5*time.Minute, now)
	c.load(cache, 5*time.Minute, now.Add(time.Minute))
	if src.reads != 1 || len(c.events) != 1 {
		t.Fatalf("after two loads within max age: %d reads, %d events", src.reads, len(c.events))
	}

	src.err = errors.New("offline")
	c.load(cache, 5*time.Minute, now.Add(6*time.Minute))
	if src.reads != 2 || len(c.events) != 1 || c.err == nil {
		t.Fatalf("failed reload: %d reads, %d events, err %v; want the month's events kept", src.reads, len(c.events), c.err)
	}

	c.changeMonth(1, now)
	c.load(cache, 5*time.Minute, now.Add(7*time.Minute))
	if src.reads != 3 || len(c.events) != 0 {
		t.Fatalf("failed read of a new month: %d reads, %d events; want none kept from the old month", src.reads, len(c.events))
	}
	if !c.selected.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("next month selects %s, want October 1", c.selected)
	}
	c.changeMonth(-1, now)
	if !c.selected.Equal(dayOf(now)) {
		t.Errorf("returning to this month selects %s, want today", c.selected)
	}

	c.load(nil, 5*time.Minute, now)
	if src.reads != 3 {
		t.Errorf("load without a calendar read from the source")
	}
}
