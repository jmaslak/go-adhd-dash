// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

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

	from time.Time // as last asked for
}

func (f *fakeSource) Events(_ context.Context, from, _ time.Time) ([]Event, error) {
	f.from = from
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

	a := p.Get("alice", time.Local, source)
	if p.Get("alice", time.Local, source) != a || made != 1 {
		t.Errorf("the same key gave another cache, or made another source (%d)", made)
	}
	b := p.Get("bob", time.Local, source)
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
	p.Get("bob", time.Local, source)
	p.mu.Lock()
	_, kept := p.entries["alice"]
	p.mu.Unlock()
	if kept {
		t.Errorf("unused cache kept")
	}
	if p.Get("alice", time.Local, source) == a || made != 3 {
		t.Errorf("dropped key gave its old cache back, or made no source (%d)", made)
	}
}

func TestCacheIn(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("no zone files:", err)
	}
	src := &fakeSource{}
	NewCacheIn(src, tokyo).refresh(context.Background())
	if src.from.Location() != tokyo || src.from.Hour() != 0 || src.from.Minute() != 0 {
		t.Errorf("fetched from %v; want midnight in Tokyo", src.from)
	}

	p := NewPool(context.Background(), time.Hour)
	if p.Get("alice", tokyo, func() Source { return src }).loc != tokyo {
		t.Errorf("pool's cache is not in the zone asked for")
	}
}

func TestEventIn(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip("no zone files:", err)
	}
	// 05:00 UTC on the 27th is 23:00 on the 26th in Denver.
	timed := Event{Summary: "late", Start: at(5, 0), End: at(6, 0)}
	if got := timed.In(denver); got.Start.Location() != denver || !got.Start.Equal(timed.Start) || got.Start.Day() != 26 {
		t.Errorf("timed event moved to %v", got.Start)
	}
	// An all-day event on the 27th stays on the 27th.
	allDay := Event{Summary: "holiday", Start: base, End: base.AddDate(0, 0, 1), AllDay: true}
	got := allDay.In(denver)
	if want := time.Date(2026, 9, 27, 0, 0, 0, 0, denver); !got.Start.Equal(want) || !got.End.Equal(want.AddDate(0, 0, 1)) {
		t.Errorf("all-day event is %v to %v; want the 27th in Denver", got.Start, got.End)
	}

	// Moving the all-day event to Denver's midnight, 06:00 UTC, puts it
	// after the timed one, and the list is put back in order.
	s := Snapshot{Events: []Event{allDay, timed}, Fetched: at(4, 0)}.In(denver)
	if s.Events[0].Summary != "late" || s.Events[1].Summary != "holiday" || s.Fetched.Location() != denver {
		t.Errorf("snapshot in Denver = %+v", s)
	}
	if EventsIn(nil, denver) != nil {
		t.Errorf("no events gave some")
	}
}
