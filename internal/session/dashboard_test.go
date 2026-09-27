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
		BusyEnabled:   true,
		Busy:          busy.Status{Connected: true, Light: "off", MinutesToNext: &minutes, Updated: now.Add(-5 * time.Minute)},
		AgendaEnabled: true,
		Agenda: agenda.Snapshot{Fetched: now, Events: []agenda.Event{
			{Summary: "Holiday", Start: now.Add(-10 * time.Hour), End: now.Add(14 * time.Hour), AllDay: true},
			{Summary: "Running", Start: now.Add(-30 * time.Minute), End: now.Add(30 * time.Minute)},
			{Summary: "Soon", Start: now.Add(15 * time.Minute), End: now.Add(45 * time.Minute)},
			{Summary: "Tomorrow " + strings.Repeat("x", 200), Start: now.Add(24 * time.Hour), End: now.Add(25 * time.Hour)},
		}},
	}
	for i := range nTasks {
		v.Tasks = append(v.Tasks, tasks.Task{Number: i + 1, Title: fmt.Sprintf("task %d %s", i+1, strings.Repeat("é", 150)), Tags: []string{"work"}})
	}
	return v
}

// screenText renders fields onto a grid the way a terminal would, failing
// on any field that runs off the screen or overlaps another.
func screenText(t *testing.T, s go3270.Screen, rows, cols int) []string {
	t.Helper()
	grid := make([][]rune, rows)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(" ", cols))
	}
	used := map[[2]int]bool{}
	for _, f := range s {
		if f.Row < 0 || f.Row >= rows || f.Col < 0 {
			t.Fatalf("field off screen: %+v", f)
		}
		end := f.Col + 1 + utf8.RuneCountInString(f.Content)
		if end > cols {
			t.Fatalf("field wraps past column %d: row %d col %d %q", cols, f.Row, f.Col, f.Content)
		}
		for c := f.Col; c < end; c++ {
			if used[[2]int{f.Row, c}] {
				t.Fatalf("field overlaps another at row %d col %d: %q", f.Row, c, f.Content)
			}
			used[[2]int{f.Row, c}] = true
		}
		copy(grid[f.Row][f.Col+1:], []rune(f.Content))
	}
	out := make([]string, rows)
	for i, r := range grid {
		out[i] = strings.TrimRight(string(r), " ")
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
		"NOT BUSY", "Next meeting in 15m", // 20 minutes reported 5 minutes ago
		"Today all day", "NOW      Running", "in 15m   Soon", "Tmrw ",
		"TASKS 3 open", "   1 [work] task 1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
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
