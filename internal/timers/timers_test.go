package timers

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/db"
)

// TestNewAddsNote checks that a table made before timers had notes gets
// them, its timers' "Timer".
func TestNewAddsNote(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "adhd-dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	if _, err := d.Exec(`CREATE TABLE timers (owner INTEGER PRIMARY KEY, until INTEGER NOT NULL, total_ns INTEGER NOT NULL, end_ns INTEGER NOT NULL);
		INSERT INTO timers VALUES (1, 0, 60000000000, 0)`); err != nil {
		t.Fatal(err)
	}
	for range 2 { // and again, with the note there
		s, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok, err := s.Get(1); err != nil || !ok || got.Note != "Timer" {
			t.Errorf("the old timer: %+v, %v, %v", got, ok, err)
		}
	}
}

// open makes timers in a new database, closed when the test ends.
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

func TestStore(t *testing.T) {
	s := open(t)
	if _, ok, err := s.Get(1); ok || err != nil {
		t.Fatalf("a timer before any started: %v, %v", ok, err)
	}
	end := time.Date(2026, 10, 4, 9, 30, 0, 123, time.Local)
	first := Timer{Total: 30 * time.Minute, End: end, Note: "Laundry"}
	if err := s.Start(1, first); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(1)
	if err != nil || !ok || got.Until || got.Total != first.Total || !got.End.Equal(end) || got.Note != "Laundry" {
		t.Fatalf("got %+v, %v, %v; want %+v", got, ok, err, first)
	}
	if _, ok, _ := s.Get(2); ok {
		t.Errorf("another user's timer is user 1's")
	}

	// Started again, it replaces the first, which can no longer be cleared.
	second := Timer{Until: true, Total: time.Hour, End: end.Add(30 * time.Minute)}
	if err := s.Start(1, second); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(1, first); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Get(1); !ok || !got.Until || !got.End.Equal(second.End) {
		t.Fatalf("clearing the replaced timer left %+v, %v", got, ok)
	}
	if err := s.Clear(1, second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(1); ok {
		t.Errorf("not cleared")
	}

	// No one has a timer.
	if err := s.Start(0, first); err == nil {
		t.Errorf("started a timer for no one")
	}
	if _, ok, err := s.Get(0); ok || err != nil {
		t.Errorf("no one has a timer: %v, %v", ok, err)
	}
}

func TestRemoveOwned(t *testing.T) {
	s := open(t)
	for owner := 1; owner <= 3; owner++ {
		if err := s.Start(owner, Timer{Total: time.Minute, End: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RemoveOwned(func(owner int) bool { return owner != 2 })
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2", n, err)
	}
	for owner, want := range map[int]bool{1: false, 2: true, 3: false} {
		if _, ok, _ := s.Get(owner); ok != want {
			t.Errorf("user %d has a timer: %v", owner, ok)
		}
	}
}
