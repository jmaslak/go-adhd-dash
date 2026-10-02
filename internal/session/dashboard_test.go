package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jmaslak/go-busy-indicator/gauth"
	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
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
		// As on a terminal, a field running off the last row wraps to the
		// first, but no further.
		start := f.Row*cols + f.Col
		end := start + 1 + utf8.RuneCountInString(f.Content)
		if end > 2*len(buf) || (end > len(buf) && end-len(buf) > start) {
			t.Fatalf("field runs off the screen: row %d col %d %q", f.Row, f.Col, f.Content)
		}
		for a := start; a < end; a++ {
			if used[a%len(buf)] {
				t.Fatalf("field overlaps another at row %d col %d: %q", a%len(buf)/cols, a%cols, f.Content)
			}
			used[a%len(buf)] = true
		}
		for i, r := range []rune(f.Content) {
			buf[(start+1+i)%len(buf)] = r
		}
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
			v.Checklists = sampleChecklists(12)
			seen := map[int]bool{}
			_, _, total, _ := buildDashboard(rows, cols, v, 0, "AD000001")
			for page := range total {
				s, shown, _, _ := buildDashboard(rows, cols, v, page, "AD000001")
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

			_, shown, _, _ := buildDashboard(rows, cols, v, 99, "")
			if shown != total-1 {
				t.Errorf("page 99 clamped to %d, want %d", shown, total-1)
			}
		})
	}
}

func TestDashboardContent(t *testing.T) {
	s, _, _, _ := buildDashboard(24, 80, sampleView(3), 0, "AD000001")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{
		"** NOT IN MEETING **", "Meeting now: Running",
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
	for light, want := range map[string]string{"red": "** IN MEETING **", "green": "** AVAILABLE **", "off": "** NOT IN MEETING **"} {
		v := sampleView(0)
		v.Busy.Light = light
		s, _, _, _ := buildDashboard(24, 80, v, 0, "")
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
		s, _, _, _ := buildDashboard(24, 80, v, 0, "")
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

	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
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

	s, _, total, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if strings.Contains(text, "[work]") || !strings.Contains(text, "PF8 to see them") {
		t.Errorf("first page should give every row to the agenda and point to PF8:\n%s", text)
	}
	if !strings.Contains(text, "crowd12") || !strings.Contains(text, "... and 7 more") {
		t.Errorf("first page should fill the body with the agenda:\n%s", text)
	}

	seen := 0
	for page := 1; page < total; page++ {
		s, _, _, _ := buildDashboard(24, 80, v, page, "")
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
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
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
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Next meeting in 16m: Odd", "in 16m   Odd"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
}

func TestBusyControlKeys(t *testing.T) {
	v := sampleView(0)
	s, _, _, _ := buildDashboard(24, 80, v, 0, "AD000001")
	if help := screenText(t, s, 24, 80)[23]; help != " PF3=Exit PF4=Calc PF5=Auto PF7=Up PF8=Dn PF9=Cal PF10=Tasks PF11=Chat" {
		t.Errorf("without a control port, help row is %q", help)
	}
	s, _, _, _ = buildDashboard(27, 132, v, 0, "AD000001")
	if help := screenText(t, s, 27, 132)[26]; !strings.HasSuffix(help, "Enter=Rfrsh   LU AD000001") {
		t.Errorf("at 132 columns, help row is %q; want the LU name", help)
	}

	v.BusyControl = true
	v.Message = "Could not send to busy indicator: " + strings.Repeat("x", 100)
	s, _, _, _ = buildDashboard(24, 80, v, 0, "AD000001")
	rows := screenText(t, s, 24, 80)
	if want := " PF1=Busy PF2=Off PF3=Exit PF4=Calc PF5=Auto PF7=Up PF8=Dn PF9=Cal PF10=Tasks"; rows[23] != want {
		t.Errorf("help row is %q, want %q (neither PF11, Enter nor the LU name fits)", rows[23], want)
	}
	// The message follows the command field, stopping short of the bottom
	// banner's attribute byte at the end of the row.
	if !strings.HasPrefix(rows[21], " Command ===>") || !strings.Contains(rows[21], "Could not send to busy indicator") || len(rows[21]) > 79 {
		t.Errorf("command row is %q", rows[21])
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
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
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

	// With no calendar, nothing of one is shown, and the tasks start
	// where the agenda would.
	s, _, _, _ = buildDashboard(24, 80, view{Now: now}, 0, "")
	rows := screenText(t, s, 24, 80)
	text = strings.Join(rows, "\n")
	for _, want := range []string{"not configured (-busy-url)", "All tasks completed!"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "AGENDA") || strings.Contains(strings.ToLower(text), "calendar") {
		t.Errorf("screen shows calendar information with no calendar:\n%s", text)
	}
	if !strings.HasPrefix(rows[firstBodyRow], " TASKS") {
		t.Errorf("row %d is %q; want the tasks to start there", firstBodyRow, rows[firstBodyRow])
	}

	// A refused authorization says how to fix it.
	v.Agenda.Err = fmt.Errorf("refreshing: %w", errors.Join(&gauth.RejectedError{Source: "x", Code: "invalid_grant"}))
	s, _, _, _ = buildDashboard(24, 80, v, 0, "")
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "type GOOGLE to reconnect") {
		t.Errorf("refused authorization not explained:\n%s", text)
	}
}

// sampleChecklists are n checklists, IDs 1 to n, all starred but the second,
// the first with its one item done.
func sampleChecklists(n int) []checklist.Checklist {
	var out []checklist.Checklist
	for i := range n {
		l := checklist.Checklist{ID: i + 1, Name: fmt.Sprint("list ", i+1), Items: []checklist.Item{{ID: 100 + i, Text: "x"}}}
		l.Items[0].Done = i == 0
		l.Active = i != 1
		out = append(out, l)
	}
	return out
}

func TestDashboardChecklists(t *testing.T) {
	v := sampleView(3)
	v.Checklists = sampleChecklists(3)
	s, _, _, at := buildDashboard(24, 80, v, 0, "")
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"TASKS 5 open",
		">\n    - [checklist] list 1 (1/1)\n    - [checklist] list 3 (0/1)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "[checklist]") < strings.Index(text, "task 3") {
		t.Errorf("checklists should follow the tasks:\n%s", text)
	}
	for _, unwanted := range []string{"list 2", "OPEN CHECKLISTS", "Enter on"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("screen has %q:\n%s", unwanted, text)
		}
	}
	if len(at) != 2 {
		t.Fatalf("checklist rows are %v, want two", at)
	}
	for row, id := range at {
		if want := fmt.Sprintf("list %d (", id); !strings.Contains(rows[row], want) {
			t.Errorf("row %d is %q, mapped to checklist %d", row, rows[row], id)
		}
	}
	for _, f := range s {
		if strings.HasPrefix(f.Content, "list ") && (f.Color != go3270.Green || !f.Intense) {
			t.Errorf("checklist name not colored as a task's title: %+v", f)
		}
		if f.Content == checklistTag && f.Color != go3270.Turquoise {
			t.Errorf("checklist label not colored as a task's tags: %+v", f)
		}
	}
}

func TestDashboardChecklistsPage(t *testing.T) {
	v := sampleView(40)
	v.Checklists = sampleChecklists(12) // 11 starred
	_, _, total, _ := buildDashboard(24, 80, v, 0, "")
	seen := map[int]int{}
	for page := range total {
		s, _, _, at := buildDashboard(24, 80, v, page, "")
		rows := screenText(t, s, 24, 80)
		if !strings.Contains(strings.Join(rows, "\n"), "TASKS 51 open") {
			t.Errorf("page %d: header does not count the checklists:\n%s", page+1, strings.Join(rows, "\n"))
		}
		for row, id := range at {
			seen[id]++
			if row >= dashboardCommandRow(24) || !strings.Contains(rows[row], fmt.Sprintf("list %d (", id)) {
				t.Errorf("page %d: row %d is %q, mapped to checklist %d", page+1, row, rows[row], id)
			}
		}
	}
	if len(seen) != 11 {
		t.Errorf("saw %d of 11 starred checklists across %d pages", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("checklist %d on %d pages", id, n)
		}
	}
}

func TestDashboardChecklistsError(t *testing.T) {
	v := sampleView(3)
	v.ChecklistsErr = errors.New("reading checklists: bad")
	s, _, _, at := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, "[checklist] reading checklists: bad") || !strings.Contains(text, "TASKS 3 open") || len(at) != 0 {
		t.Errorf("checklist error should be a line of the task list, not counted or opened (%v):\n%s", at, text)
	}

	// Only unstarred checklists, and no tasks: all done.
	v = sampleView(0)
	v.Checklists = sampleChecklists(2)[1:]
	s, _, _, _ = buildDashboard(24, 80, v, 0, "")
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "All tasks completed!") || strings.Contains(text, "[checklist]") {
		t.Errorf("unstarred checklist shown:\n%s", text)
	}

	// A starred checklist is still open.
	v.Checklists = sampleChecklists(1)
	s, _, _, _ = buildDashboard(24, 80, v, 0, "")
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); strings.Contains(text, "All tasks completed!") || !strings.Contains(text, "TASKS 1 open") {
		t.Errorf("starred checklist not counted as open:\n%s", text)
	}
}

func TestDashboardTaskFetchStatus(t *testing.T) {
	v := view{Now: now, TasksLoading: true}
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "TASKS fetching from Trello") || strings.Contains(text, "All tasks completed!") {
		t.Errorf("first fetch under way:\n%s", text)
	}

	v = sampleView(2)
	v.TasksFetched, v.TasksErr = now.Add(-20*time.Minute), errors.New("trello: timeout")
	s, _, _, _ = buildDashboard(24, 80, v, 0, "")
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "TASKS 2 open stale, from 09:40: trello: timeout") || !strings.Contains(text, "   2 [work] task 2") {
		t.Errorf("stale tasks should still be listed, noted as stale:\n%s", text)
	}
}

func TestTintTitle(t *testing.T) {
	// A screen with nothing at the start of row 1: a field is added to end
	// the title there, and the help line is cut short of the last column.
	screen, _, _ := buildAdmin(24, 80, now, "", false, 1)
	screen = append(screen, go3270.Field{Row: 23, Col: 40, Content: strings.Repeat("x", 39)})
	plain := screenText(t, screen, 24, 80)
	tinted := tintTitle(screen, 24, 80, go3270.Red)
	rows := screenText(t, tinted, 24, 80)
	if rows[0] != plain[0] {
		t.Errorf("title row %q, want %q", rows[0], plain[0])
	}
	if want := plain[23][:78] + ">"; rows[23] != want {
		t.Errorf("last row %q, want %q", rows[23], want)
	}
	title := tinted[len(tinted)-1]
	if title.Row != 23 || title.Col != 79 || len(title.Content) != 80 || title.Color != go3270.Red || title.Highlighting != go3270.ReverseVideo {
		t.Errorf("title field %+v", title)
	}
	ended := false
	for _, f := range tinted {
		if f.Row == 0 && f.Col < 79 {
			t.Errorf("title field left behind: %+v", f)
		}
		ended = ended || (f.Row == 1 && f.Col == 0)
	}
	if !ended {
		t.Error("nothing ends the title at the start of row 1")
	}

	// The dashboard's busy banner, the same color, has its attribute byte
	// in row 0's last column: it becomes part of the title's field, which
	// then runs over both rows.
	v := sampleView(3)
	v.Busy.Light = "red"
	screen, _, _, _ = buildDashboard(24, 80, v, 0, "")
	tinted = tintTitle(screen, 24, 80, go3270.Red)
	rows = screenText(t, tinted, 24, 80)
	if title := tinted[len(tinted)-1]; len(title.Content) != 160 || !strings.HasPrefix(rows[0], " EXECUTIVE FUNCTION DASHBOARD") {
		t.Errorf("dashboard title %q, row %q", title.Content, rows[0])
	}
	if !strings.Contains(rows[1], "IN MEETING") {
		t.Errorf("banner row %q", rows[1])
	}
	for _, f := range tinted {
		if f.Row == 0 && f.Col == 79 {
			t.Errorf("banner field left in row 0: %+v", f)
		}
	}
	// A timed redraw of it still covers the whole screen.
	filled := fillScreen(tinted, 24, 80)
	screenText(t, filled, 24, 80)

	// A banner another color, as when the state changed between reading
	// it for the banner and for the title, is left as it is.
	tinted = tintTitle(screen, 24, 80, go3270.Green)
	if title := tinted[len(tinted)-1]; len(title.Content) != 79 {
		t.Errorf("title over a banner another color is %d long", len(title.Content))
	}
}

func TestHeaderColor(t *testing.T) {
	for _, c := range []struct {
		status busy.Status
		color  go3270.Color
		ok     bool
	}{
		{busy.Status{Connected: true, Light: "red"}, go3270.Red, true},
		{busy.Status{Connected: true, Light: "green"}, go3270.Green, true},
		{busy.Status{Connected: true, Light: "off"}, 0, false},
		{busy.Status{Connected: false, Light: "red"}, 0, false},
	} {
		if color, ok := headerColor(staticBusy(c.status)); color != c.color || ok != c.ok {
			t.Errorf("%+v: %v %v", c.status, color, ok)
		}
	}
	if _, ok := headerColor(nil); ok {
		t.Error("no indicator tinted")
	}
}

// staticBusy is a busy indicator always in one state.
type staticBusy busy.Status

func (s staticBusy) Status() busy.Status { return busy.Status(s) }
