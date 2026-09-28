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
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "work"},
		{Summary: "A", Start: at(9, 0), End: at(10, 0), Calendar: "work"},
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "home"},
		{Summary: "B", Start: at(10, 0), End: at(11, 0), Calendar: "work"},
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
	data := `[{"summary":"In","start":"2026-09-27T09:00:00Z","end":"2026-09-27T10:00:00Z","calendar":"work"},
	          {"summary":"Out","start":"2026-09-30T09:00:00Z","end":"2026-09-30T10:00:00Z"}]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := File{Path: path}.Events(context.Background(), base, base.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Summary != "In" || got[0].Calendar != "work" {
		t.Errorf("File.Events = %+v", got)
	}
}
