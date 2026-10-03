package agenda

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

type fakeSource struct {
	events []Event
	err    error
}

func (f *fakeSource) Events(context.Context, time.Time, time.Time) ([]Event, error) {
	return f.events, f.err
}

func TestRefreshSortsDedupesAndKeepsOnFailure(t *testing.T) {
	src := &fakeSource{events: []Event{
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "work", OthersDeclined: true},
		{Summary: "A", Start: at(9, 0), End: at(10, 0), Calendar: "work", OthersDeclined: true},
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "home"},
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "work", OthersDeclined: true},
	}}
	c := NewCache(src)
	c.refresh(context.Background())

	snap := c.Snapshot()
	if snap.Err != nil || len(snap.Events) != 2 || snap.Events[0].Summary != "A" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := snap.Events[1].Calendar; got != "work,home" {
		t.Errorf("merged duplicate's calendars are %q, want \"work,home\"", got)
	}
	// Marked as declined by everyone else only if every copy says so.
	if !snap.Events[0].OthersDeclined || snap.Events[1].OthersDeclined {
		t.Errorf("declined marks %v, %v; want A's alone", snap.Events[0].OthersDeclined, snap.Events[1].OthersDeclined)
	}

	src.err = errors.New("boom")
	c.refresh(context.Background())
	snap = c.Snapshot()
	if snap.Err == nil || len(snap.Events) != 2 {
		t.Fatalf("after failure, snapshot = %+v", snap)
	}
}

func TestUpcoming(t *testing.T) {
	s := Snapshot{Events: []Event{
		{Summary: "done", Start: at(8, 0), End: at(9, 0)},
		{Summary: "running", Start: at(9, 0), End: at(10, 0)},
		{Summary: "later", Start: at(11, 0), End: at(12, 0)},
	}}
	got := s.Upcoming(at(9, 30))
	if len(got) != 2 || got[0].Summary != "running" {
		t.Errorf("Upcoming = %+v", got)
	}
}

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenda.json")
	data := `[{"summary":"In","start":"2026-09-27T09:00:00Z","end":"2026-09-27T10:00:00Z","calendar":"work","others_declined":true},
	          {"summary":"Out","start":"2026-09-30T09:00:00Z","end":"2026-09-30T10:00:00Z"}]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := File{Path: path}.Events(context.Background(), base, base.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Summary != "In" || got[0].Calendar != "work" || !got[0].OthersDeclined {
		t.Errorf("File.Events = %+v", got)
	}
}

func TestPool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewPool(ctx, time.Hour)
	made := 0
	source := func() Source {
		made++
		return &fakeSource{events: []Event{{Summary: "A", Start: at(9, 0), End: at(10, 0)}}}
	}

	a := p.Get("alice", source)
	if p.Get("alice", source) != a || made != 1 {
		t.Errorf("the same key gave another cache, or made another source (%d)", made)
	}
	b := p.Get("bob", source)
	if b == a || made != 2 {
		t.Errorf("another key shared a cache, or made no source (%d)", made)
	}
	// Each fills itself.
	for deadline := time.Now().Add(5 * time.Second); a.Snapshot().Fetched.IsZero() && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if len(a.Snapshot().Events) != 1 {
		t.Errorf("cache not filled: %+v", a.Snapshot())
	}

	// One unused for three intervals is dropped when another is asked for.
	p.mu.Lock()
	p.entries["alice"].lastUsed = time.Now().Add(-4 * time.Hour)
	p.mu.Unlock()
	p.Get("bob", source)
	p.mu.Lock()
	_, kept := p.entries["alice"]
	p.mu.Unlock()
	if kept {
		t.Errorf("unused cache kept")
	}
	if p.Get("alice", source) == a || made != 3 {
		t.Errorf("dropped key gave its old cache back, or made no source (%d)", made)
	}
}
