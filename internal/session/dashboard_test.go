package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

var now = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func sampleView(nTasks int) view {
	minutes := 20
	v := view{
		Now:           now,
		AutoRefresh:   true,
		BusyEnabled:   true,
		Busy:          busy.Status{Connected: true, Light: "off", MinutesToNext: &minutes, Updated: now.Add(-5 * time.Minute)},
		AgendaEnabled: true,
		Agenda: agenda.Snapshot{Fetched: now, Events: []agenda.Event{
			{Summary: "Holiday", Start: now.Add(-10 * time.Hour), End: now.Add(14 * time.Hour), AllDay: true},
			{Summary: "Running", Start: now.Add(-30 * time.Minute), End: now.Add(30 * time.Minute)},
			{Summary: "Soon", Start: now.Add(15 * time.Minute), End: now.Add(45 * time.Minute)},
			{Summary: "Tomorrow " + strings.Repeat("x", 200), Start: now.Add(23 * time.Hour), End: now.Add(24 * time.Hour)},
			{Summary: "Beyond", Start: now.Add(25 * time.Hour), End: now.Add(26 * time.Hour)},
		}},
	}
	for i := range nTasks {
		v.Tasks = append(v.Tasks, tasks.Task{Number: i + 1, Title: fmt.Sprintf("task %d %s", i+1, strings.Repeat("é", 150)), Tags: []string{"work"}})
	}
	return v
}

// screenText renders fields onto a grid the way a terminal would, content
// running on into the next row as it does in the 3270 buffer, failing on any
// field that runs off the end of the screen or overlaps another.
func screenText(t *testing.T, s go3270.Screen, rows, cols int) []string {
	t.Helper()
	buf := []rune(strings.Repeat(" ", rows*cols))
	used := map[int]bool{}
	for _, f := range s {
		if f.Row < 0 || f.Row >= rows || f.Col < 0 || f.Col >= cols {
			t.Fatalf("field off screen: %+v", f)
		}
		start := f.Row*cols + f.Col
		end := start + 1 + utf8.RuneCountInString(f.Content)
		if end > len(buf) {
			t.Fatalf("field runs off the screen: row %d col %d %q", f.Row, f.Col, f.Content)
		}
		for a := start; a < end; a++ {
			if used[a] {
				t.Fatalf("field overlaps another at row %d col %d: %q", a/cols, a%cols, f.Content)
			}
			used[a] = true
		}
		copy(buf[start+1:], []rune(f.Content))
	}
	out := make([]string, rows)
	for i := range out {
		out[i] = strings.TrimRight(string(buf[i*cols:(i+1)*cols]), " ")
	}
	return out
}

func TestDashboardFitsAndPages(t *testing.T) {
	for _, size := range [][2]int{{24, 80}, {32, 80}, {27, 132}} {
		rows, cols := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", rows, cols), func(t *testing.T) {
			v := sampleView(40)
			seen := map[int]bool{}
			_, _, total := buildDashboard(rows, cols, v, 0, "AD000001")
			for page := range total {
				s, shown, _ := buildDashboard(rows, cols, v, page, "AD000001")
				if shown != page {
					t.Fatalf("page %d shown as %d", page, shown)
				}
				for _, l := range screenText(t, s, rows, cols) {
					var n int
					if _, err := fmt.Sscanf(strings.TrimSpace(l), "%d [work]", &n); err == nil {
						if seen[n] {
							t.Errorf("task %d on two pages", n)
						}
						seen[n] = true
					}
				}
			}
			if len(seen) != 40 {
				t.Errorf("saw %d of 40 tasks across %d pages", len(seen), total)
			}

			_, shown, _ := buildDashboard(rows, cols, v, 99, "")
			if shown != total-1 {
				t.Errorf("page 99 clamped to %d, want %d", shown, total-1)
			}
		})
	}
}

func TestDashboardContent(t *testing.T) {
	s, _, _ := buildDashboard(24, 80, sampleView(3), 0, "AD000001")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{
		"NOT BUSY", "Meeting now: Running",
		"Today 09:30-10:30 NOW      Running", "in 15m   Soon", "Tmrw ",
		"TASKS 3 open", "   1 [work] task 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "AGENDA (next 24 hours)") || strings.Contains(text, "Beyond") || strings.Contains(text, "Holiday") {
		t.Errorf("agenda should be labeled, stop at 24 hours and leave out all-day events:\n%s", text)
	}
}

func TestDashboardBusyBanner(t *testing.T) {
	for light, want := range map[string]string{"red": "** BUSY **", "green": "** AVAILABLE **", "off": "** NOT BUSY **"} {
		v := sampleView(0)
		v.Busy.Light = light
		s, _, _ := buildDashboard(24, 80, v, 0, "")
		text := screenText(t, s, 24, 80)
		if got := strings.TrimSpace(text[busyRow]); got != want {
			t.Errorf("%s: banner row is %q, want %q", light, text[busyRow], want)
		}
		if lead := len(text[busyRow]) - len(want); lead != (80-len(want))/2 {
			t.Errorf("%s: banner text starts at column %d, not centered", light, lead)
		}
		if text[22] != text[busyRow] {
			t.Errorf("%s: bottom banner row is %q, want it to match the top", light, text[22])
		}
		if !strings.HasPrefix(text[busyRow+1], " Meeting now: Running") {
			t.Errorf("%s: details row is %q", light, text[busyRow+1])
		}
		var banners []int
		for _, f := range s {
			if strings.TrimSpace(f.Content) == want {
				if f.Col != 79 || len(f.Content) != 80 {
					t.Errorf("%s: banner field does not span the row: %+v", light, f)
				}
				banners = append(banners, f.Row+1)
			}
		}
		if fmt.Sprint(banners) != fmt.Sprint([]int{busyRow, 22}) {
			t.Errorf("%s: banners on rows %v, want %d and 22", light, banners, busyRow)
		}
	}
}

func TestNextMeetingText(t *testing.T) {
	at := func(minutes int) *int { return &minutes }
	tomorrow := dayOf(now).AddDate(0, 0, 1)
	for _, c := range []struct {
		name    string
		minutes *int // the feed's, reported five minutes ago
		events  []agenda.Event
		agenda  bool
		want    string
	}{
		{"in progress", at(20), nil, true, "Meeting now: Running"},
		{"next", at(20), []agenda.Event{
			{Summary: "Soon", Start: now.Add(15 * time.Minute), End: now.Add(45 * time.Minute)},
		}, true, "Next meeting in 15m: Soon"},
		{
			// The feed counts to tomorrow's all-day event at midnight; the
			// next meeting is the one the next morning.
			"feed counting to an all-day event", at(14*60 + 5), []agenda.Event{
				{Summary: "Home", Start: tomorrow, End: tomorrow.AddDate(0, 0, 1), AllDay: true},
				{Summary: "OOO", Start: tomorrow.Add(8 * time.Hour), End: tomorrow.Add(9 * time.Hour)},
				{Summary: "Standup", Start: tomorrow.Add(9 * time.Hour), End: tomorrow.Add(10 * time.Hour)},
			}, true, "Next meeting in 23h00: Standup",
		},
		{"calendar empty", at(20), []agenda.Event{}, true, "No more meetings today or tomorrow"},
		{"no calendar", at(20), nil, false, "Next meeting in 15m"},
		{"no calendar, in progress", at(-25), nil, false, "Meeting now"},
		{"no calendar, none left", nil, nil, false, "No more meetings today"},
	} {
		v := sampleView(0)
		if c.events != nil {
			v.Agenda.Events = c.events
		}
		v.AgendaEnabled = c.agenda
		v.Busy.MinutesToNext = c.minutes
		if got := nextMeetingText(v); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// A calendar not yet read falls back to the feed.
	v := sampleView(0)
	v.Agenda = agenda.Snapshot{}
	if got := nextMeetingText(v); got != "Next meeting in 15m" {
		t.Errorf("calendar not yet read: got %q", got)
	}
}

func TestAutoRefreshMarker(t *testing.T) {
	for _, on := range []bool{true, false} {
		v := sampleView(0)
		v.AutoRefresh = on
		s, _, _ := buildDashboard(24, 80, v, 0, "")
		title := screenText(t, s, 24, 80)[titleRow]
		if got := strings.Contains(title, "AUTO-REFRESH"); got != on {
			t.Errorf("auto-refresh %v: title row is %q", on, title)
		}
		if !strings.HasSuffix(title, "Sun Sep 27 10:00:00") {
			t.Errorf("auto-refresh %v: clock missing from title row %q", on, title)
		}
	}
}

func TestAgendaShowsNextDay(t *testing.T) {
	v := sampleView(5)
	v.Agenda.Events = nil
	for i := range 10 {
		start := now.Add(time.Duration(i*2+1) * time.Hour)
		v.Agenda.Events = append(v.Agenda.Events, agenda.Event{Summary: fmt.Sprint("meeting", i), Start: start, End: start.Add(time.Hour)})
	}
	later := now.Add(30 * time.Hour)
	v.Agenda.Events = append(v.Agenda.Events, agenda.Event{Summary: "later", Start: later, End: later.Add(time.Hour)})

	s, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for i := range 10 {
		if want := fmt.Sprint("meeting", i); !strings.Contains(text, want) {
			t.Errorf("screen lacks %q, within 24 hours:\n%s", want, text)
		}
	}
	if strings.Contains(text, "later") {
		t.Errorf("meeting after 24 hours shown:\n%s", text)
	}
	if !strings.Contains(text, "   1 [work] task 1") {
		t.Errorf("task list squeezed out though there is room:\n%s", text)
	}
}

func TestFullAgendaPushesTasksToNextPage(t *testing.T) {
	v := sampleView(5)
	v.Agenda.Events = nil
	for i := range 20 {
		start := now.Add(time.Duration(i) * time.Hour)
		v.Agenda.Events = append(v.Agenda.Events, agenda.Event{Summary: fmt.Sprint("crowd", i), Start: start, End: start.Add(time.Hour)})
	}

	s, _, total := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if strings.Contains(text, "[work]") || !strings.Contains(text, "PF8 to see them") {
		t.Errorf("first page should give every row to the agenda and point to PF8:\n%s", text)
	}
	if !strings.Contains(text, "crowd12") || !strings.Contains(text, "... and 7 more") {
		t.Errorf("first page should fill the body with the agenda:\n%s", text)
	}

	seen := 0
	for page := 1; page < total; page++ {
		s, _, _ := buildDashboard(24, 80, v, page, "")
		text := strings.Join(screenText(t, s, 24, 80), "\n")
		seen += strings.Count(text, "[work]")
		if strings.Contains(text, "crowd12") {
			t.Errorf("page %d shows the whole agenda:\n%s", page+1, text)
		}
	}
	if seen != 5 {
		t.Errorf("saw %d of 5 tasks on pages after the first", seen)
	}
}

func TestIsMeeting(t *testing.T) {
	for title, want := range map[string]bool{
		"Standup": true, "Zoooom call": true, "Cooordination": true,
		"Out of office": false, "OOO": false, "Joelle OOO - dentist": false,
		"ooo": false, "Out of the office": false, "OUT OF OFFICE until Tuesday": false,
	} {
		e := agenda.Event{Summary: title, Start: now, End: now.Add(time.Hour)}
		if got := isMeeting(e); got != want {
			t.Errorf("%q: isMeeting %v, want %v", title, got, want)
		}
	}
	if isMeeting(agenda.Event{Summary: "Holiday", Start: now, End: now.Add(24 * time.Hour), AllDay: true}) {
		t.Errorf("all-day event counted as a meeting")
	}

	v := sampleView(0)
	v.Agenda.Events = append(v.Agenda.Events,
		agenda.Event{Summary: "OOO", Start: now.Add(-time.Hour), End: now.Add(3 * time.Hour)},
		agenda.Event{Summary: "Out of office", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)},
	)
	s, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if strings.Contains(text, "OOO") || strings.Contains(text, "Out of office") {
		t.Errorf("out-of-office event shown:\n%s", text)
	}
	if got := meetingsNow(v); fmt.Sprint(got) != "[Running]" {
		t.Errorf("meetings under way are %v, want only Running", got)
	}
}

func TestNextMeetingMatchesAgenda(t *testing.T) {
	// The meeting is 15m40s away. The feed, counting whole minutes, said 20
	// five minutes ago; the agenda rounds to 16m, and the busy line must too.
	v := sampleView(0)
	start := now.Add(15*time.Minute + 40*time.Second)
	v.Agenda.Events = []agenda.Event{{Summary: "Odd", Start: start, End: start.Add(time.Hour)}}
	s, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Next meeting in 16m: Odd", "in 16m   Odd"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
}

func TestBusyControlKeys(t *testing.T) {
	v := sampleView(0)
	s, _, _ := buildDashboard(24, 80, v, 0, "AD000001")
	if help := screenText(t, s, 24, 80)[23]; strings.Contains(help, "PF1 ") || !strings.Contains(help, "LU AD000001") {
		t.Errorf("without a control port, help row is %q; want no busy keys, and the LU name", help)
	}

	v.BusyControl = true
	v.Message = "Could not send to busy indicator: " + strings.Repeat("x", 100)
	s, _, _ = buildDashboard(24, 80, v, 0, "AD000001")
	rows := screenText(t, s, 24, 80)
	if want := " PF1 Busy  PF2 Off  PF3 Exit  PF5 Auto  PF7 Up  PF8 Down  PF9 Cal  Enter Rfrsh"; rows[23] != want {
		t.Errorf("help row is %q, want %q (the LU name does not fit)", rows[23], want)
	}
	if !strings.HasPrefix(rows[21], " Could not send to busy indicator") {
		t.Errorf("message row is %q", rows[21])
	}
}

func TestDashboardDegraded(t *testing.T) {
	v := view{
		Now:           now,
		BusyEnabled:   true,
		Busy:          busy.Status{Err: errors.New("connection refused"), Problem: "not reachable at ws://x/feed"},
		AgendaEnabled: true,
		Agenda:        agenda.Snapshot{Err: errors.New("no Google credentials")},
		TasksErr:      errors.New("reading task directory: no such file"),
	}
	s, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{
		"Busy indicator not reachable at ws://x/feed",
		"calendar unavailable: no Google credentials",
		"TASKS reading task directory",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}

	s, _, _ = buildDashboard(24, 80, view{Now: now}, 0, "")
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"not configured (-busy-url)", "No calendar configured", "Nothing to do"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
}
