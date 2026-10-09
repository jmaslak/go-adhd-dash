package recurring

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/db"
)

// TestNewAddsHideUntilDue checks that a table made before recurring tasks
// could be hidden until due gets hide_until_due, its tasks not hidden.
func TestNewAddsHideUntilDue(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "adhd-dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	if _, err := d.Exec(`CREATE TABLE recurring (id INTEGER PRIMARY KEY AUTOINCREMENT, owner INTEGER NOT NULL, title TEXT NOT NULL,
			schedule TEXT NOT NULL, days INTEGER NOT NULL DEFAULT 0, weeks INTEGER NOT NULL DEFAULT 1,
			every_days INTEGER NOT NULL DEFAULT 0, at_minute INTEGER NOT NULL, start TEXT NOT NULL, due TEXT NOT NULL);
		INSERT INTO recurring (owner, title, schedule, every_days, at_minute, start, due) VALUES (1, 'Old', 'every', 1, 540, '2026-10-01', '2026-10-01')`); err != nil {
		t.Fatal(err)
	}
	for range 2 { // and again, with the column there
		s, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		if list, err := s.List(1); err != nil || len(list) != 1 || list[0].Title != "Old" || list[0].HideUntilDue {
			t.Errorf("the old task: %+v, %v", list, err)
		}
	}
}

// open makes recurring tasks in a new database, closed when the test ends.
func open(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "adhd-dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) //nolint:errcheck
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// day is a day in October 2026, which starts on a Thursday.
func day(n int) Date { return NewDate(2026, time.October, n) }

func days(ds ...time.Weekday) Weekdays {
	var w Weekdays
	for _, d := range ds {
		w = w.With(d)
	}
	return w
}

func TestFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		task Task
		from Date
		want Date
	}{
		{"weekly, the start's day", Task{Schedule: Weekly, Days: days(time.Thursday), Weeks: 1, Start: day(1)}, day(1), day(1)},
		{"weekly, later in the week", Task{Schedule: Weekly, Days: days(time.Saturday), Weeks: 1, Start: day(1)}, day(1), day(3)},
		{"weekly, the next week", Task{Schedule: Weekly, Days: days(time.Monday, time.Wednesday), Weeks: 1, Start: day(1)}, day(1), day(5)},
		// Every 2 weeks on Monday, from Friday the 2nd: the first Monday is
		// the 5th, and the weeks count from its.
		{"fortnightly, first", Task{Schedule: Weekly, Days: days(time.Monday), Weeks: 2, Start: day(2)}, day(2), day(5)},
		{"fortnightly, skips the off week", Task{Schedule: Weekly, Days: days(time.Monday), Weeks: 2, Start: day(2)}, day(6), day(19)},
		// Sunday ends the week that starts on Monday.
		{"fortnightly, Sunday of an on week", Task{Schedule: Weekly, Days: days(time.Monday, time.Sunday), Weeks: 2, Start: day(5)}, day(6), day(11)},
		{"fortnightly, Sunday of an off week", Task{Schedule: Weekly, Days: days(time.Monday, time.Sunday), Weeks: 2, Start: day(5)}, day(12), day(19)},
		{"every 3 days, on one", Task{Schedule: Every, Interval: 3, Start: day(1)}, day(7), day(7)},
		{"every 3 days, between", Task{Schedule: Every, Interval: 3, Start: day(1)}, day(8), day(10)},
		{"not before the start", Task{Schedule: Every, Interval: 3, Start: day(10)}, day(1), day(10)},
		{"after", Task{Schedule: After, Interval: 3, Start: day(10)}, day(12), day(12)},
	} {
		if got := tc.task.First(tc.from); !got.Equal(tc.want) {
			t.Errorf("%s: First(%v) = %v, want %v", tc.name, tc.from, got, tc.want)
		}
	}
}

func TestDoneAt(t *testing.T) {
	at := func(d Date, h int) time.Time { return d.At(h*60, time.Local) }
	for _, tc := range []struct {
		name string
		task Task
		done time.Time
		want Date
	}{
		{"weekly, on time", Task{Schedule: Weekly, Days: days(time.Monday, time.Friday), Weeks: 1, Start: day(1), Due: day(5)}, at(day(5), 10), day(9)},
		{"weekly, late, skips those missed", Task{Schedule: Weekly, Days: days(time.Monday, time.Wednesday), Weeks: 1, Start: day(1), Due: day(5)}, at(day(8), 10), day(12)},
		{"weekly, early", Task{Schedule: Weekly, Days: days(time.Monday), Weeks: 1, Start: day(1), Due: day(12)}, at(day(8), 10), day(19)},
		{"every, on time", Task{Schedule: Every, Interval: 3, Start: day(1), Due: day(4)}, at(day(4), 9), day(7)},
		{"every, late", Task{Schedule: Every, Interval: 3, Start: day(1), Due: day(4)}, at(day(8), 9), day(10)},
		{"after, late", Task{Schedule: After, Interval: 3, Start: day(1), Due: day(4)}, at(day(8), 9), day(11)},
		{"after, early", Task{Schedule: After, Interval: 3, Start: day(1), Due: day(4)}, at(day(2), 9), day(5)},
	} {
		tc.task.At = 7 * 60
		got := tc.task.DoneAt(tc.done)
		if !got.Due.Equal(tc.want) {
			t.Errorf("%s: due %v, want %v", tc.name, got.Due, tc.want)
		}
		// After takes the time it was done; the others keep their own.
		if want := 7 * 60; tc.task.Schedule == After {
			if want = 9 * 60; got.At != want {
				t.Errorf("%s: at %d, want %d", tc.name, got.At, want)
			}
		} else if got.At != want {
			t.Errorf("%s: at %d, want %d", tc.name, got.At, want)
		}
	}
	// To the minute, seconds dropped.
	got := Task{Schedule: After, Interval: 1, At: 0, Due: day(4)}.DoneAt(day(4).At(17*60+42, time.Local).Add(59 * time.Second))
	if !got.Due.Equal(day(5)) || got.At != 17*60+42 {
		t.Errorf("after, at 17:42:59: %v at %d", got.Due, got.At)
	}
}

func TestCheck(t *testing.T) {
	good := Task{Title: "Water plants", Schedule: Weekly, Days: days(time.Monday), Weeks: 1, At: 9 * 60, Start: day(1)}
	if err := good.Check(); err != nil {
		t.Fatalf("a good task: %v", err)
	}
	for name, change := range map[string]func(*Task){
		"no title":     func(t *Task) { t.Title = " " },
		"no days":      func(t *Task) { t.Days = 0 },
		"no weeks":     func(t *Task) { t.Weeks = 0 },
		"too late":     func(t *Task) { t.At = 24 * 60 },
		"no start":     func(t *Task) { t.Start = Date{} },
		"no interval":  func(t *Task) { t.Schedule = Every },
		"bad schedule": func(t *Task) { t.Schedule = "sometimes" },
	} {
		bad := good
		change(&bad)
		if bad.Check() == nil {
			t.Errorf("%s: checked", name)
		}
	}
}

func TestIsDue(t *testing.T) {
	task := Task{At: 9*60 + 30, Due: day(5)}
	if task.IsDue(day(5).At(9*60+29, time.Local)) {
		t.Error("due a minute early")
	}
	if !task.IsDue(day(5).At(9*60+30, time.Local)) {
		t.Error("not due on time")
	}
}

func TestStore(t *testing.T) {
	s := open(t)
	if got, err := s.List(1); err != nil || len(got) != 0 {
		t.Fatalf("before any added: %v, %v", got, err)
	}
	if _, err := s.Add(Task{Title: "x", Schedule: After, Interval: 1, Start: day(1)}, day(1)); err == nil {
		t.Error("added for no one")
	}

	// Started today, at a time already past: due at once.
	plants, err := s.Add(Task{Owner: 1, Title: "Water plants", Schedule: Every, Interval: 3, At: 9 * 60, Start: day(4)}, day(4))
	if err != nil {
		t.Fatal(err)
	}
	if !plants.Due.Equal(day(4)) || plants.ID == 0 {
		t.Errorf("added: %+v", plants)
	}
	if _, err := s.Add(Task{Owner: 2, Title: "Theirs", Schedule: After, Interval: 1, At: 0, Start: day(4)}, day(4)); err != nil {
		t.Fatal(err)
	}
	bins, err := s.Add(Task{Owner: 1, Title: "Bins", Schedule: Weekly, Days: days(time.Monday), Weeks: 1, At: 7 * 60, Start: day(1)}, day(4))
	if err != nil {
		t.Fatal(err)
	}
	if !bins.Due.Equal(day(5)) {
		t.Errorf("bins due %v, want the 5th", bins.Due)
	}
	list, err := s.List(1)
	if err != nil || len(list) != 2 || list[0].ID != plants.ID || list[1].ID != bins.ID {
		t.Fatalf("listed: %+v, %v", list, err)
	}
	if list[0].Title != "Water plants" || list[0].Schedule != Every || !list[0].Start.Equal(day(4)) {
		t.Errorf("read back: %+v", list[0])
	}

	// Done: due again on schedule; done again with what was read, nothing.
	done, err := s.Done(1, plants.ID, plants.Due, day(4).At(12*60, time.Local))
	if err != nil || !done.Due.Equal(day(7)) {
		t.Fatalf("done: %+v, %v", done, err)
	}
	if _, err := s.Done(1, plants.ID, plants.Due, day(4).At(12*60, time.Local)); !errors.Is(err, ErrChanged) {
		t.Errorf("done twice: %v", err)
	}
	if _, err := s.Done(2, plants.ID, done.Due, day(4).At(12*60, time.Local)); !errors.Is(err, ErrChanged) {
		t.Errorf("done by another owner: %v", err)
	}

	// Done after, it comes due again at the time it was done, as kept.
	walk, err := s.Add(Task{Owner: 1, Title: "Walk", Schedule: After, Interval: 2, At: 8 * 60, Start: day(4)}, day(4))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(1, walk.ID, walk.Due, day(4).At(18*60+5, time.Local)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(1, walk.ID); err != nil || !got.Due.Equal(day(6)) || got.At != 18*60+5 {
		t.Errorf("walk after done: %+v, %v", got, err)
	}
	if err := s.Remove(1, walk.ID); err != nil {
		t.Fatal(err)
	}

	// Updated: the title alone keeps the day due; the schedule recomputes it.
	done.Title = "Water the plants"
	got, err := s.Update(done, day(5))
	if err != nil || !got.Due.Equal(day(7)) {
		t.Errorf("renamed by update: %+v, %v", got, err)
	}
	got.Interval = 2
	got, err = s.Update(got, day(5))
	if err != nil || !got.Due.Equal(day(6)) {
		t.Errorf("rescheduled: %+v, %v", got, err)
	}
	if read, err := s.Get(1, plants.ID); err != nil || read.Title != "Water the plants" || read.Interval != 2 || read.HideUntilDue {
		t.Errorf("read after update: %+v, %v", read, err)
	}
	// Hidden until due, kept so, and the day due kept.
	got.HideUntilDue = true
	if got, err = s.Update(got, day(5)); err != nil || !got.Due.Equal(day(6)) {
		t.Errorf("hidden by update: %+v, %v", got, err)
	}
	if read, err := s.Get(1, plants.ID); err != nil || !read.HideUntilDue {
		t.Errorf("read after hiding: %+v, %v", read, err)
	}

	if err := s.Rename(1, bins.ID, "Put out the bins"); err != nil {
		t.Error(err)
	}
	if err := s.Rename(1, bins.ID, " "); err == nil {
		t.Error("renamed to nothing")
	}
	if err := s.Remove(1, bins.ID); err != nil {
		t.Error(err)
	}
	if err := s.Remove(1, bins.ID); !errors.Is(err, ErrChanged) {
		t.Errorf("removed twice: %v", err)
	}

	n, err := s.RemoveOwned(func(owner int) bool { return owner == 2 })
	if err != nil || n != 1 {
		t.Errorf("RemoveOwned: %d, %v", n, err)
	}
	if list, _ := s.List(2); len(list) != 0 {
		t.Errorf("left: %+v", list)
	}
	if list, _ := s.List(1); len(list) != 1 {
		t.Errorf("owner 1's: %+v", list)
	}
}
