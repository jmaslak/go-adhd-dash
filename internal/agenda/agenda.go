// Package agenda keeps today's and tomorrow's meetings, refreshed in the
// background and shared by every dashboard session so that each connection
// does not call Google on its own.
package agenda

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/jmaslak/go-busy-indicator/gauth"
	"github.com/jmaslak/go-busy-indicator/gcal"
)

// Event is one meeting.
type Event = gcal.Event

// Source produces the events between two times.
type Source interface {
	Events(ctx context.Context, from, to time.Time) ([]Event, error)
}

// Snapshot is the agenda as last fetched.
type Snapshot struct {
	// Events are chronological and free of duplicates. After a failed
	// fetch they are the ones from the last successful one.
	Events []Event

	// Fetched is when Events were read; zero before the first success.
	Fetched time.Time

	// Err is why the most recent fetch failed, nil if it succeeded.
	Err error
}

// Upcoming returns the events that have not yet ended at now.
func (s Snapshot) Upcoming(now time.Time) []Event {
	var out []Event
	for _, e := range s.Events {
		if e.End.After(now) {
			out = append(out, e)
		}
	}
	return out
}

// Cache holds the latest Snapshot. It is safe for concurrent use.
type Cache struct {
	source Source

	mu   sync.Mutex
	snap Snapshot
}

// NewCache returns a cache over source. Call Run to fill it.
func NewCache(source Source) *Cache {
	return &Cache{source: source, snap: Snapshot{Err: errors.New("not yet fetched")}}
}

// Snapshot returns the latest agenda.
func (c *Cache) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

// Run refreshes the cache every interval until ctx is canceled.
func (c *Cache) Run(ctx context.Context, interval time.Duration) {
	for {
		c.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// refresh fetches from the start of today to the end of tomorrow. A failure
// keeps the events already known: a calendar that briefly cannot be read
// should not blank the agenda.
func (c *Cache) refresh(ctx context.Context) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 0, 2)

	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	events, err := c.source.Events(fetchCtx, from, to)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.snap.Err == nil || c.snap.Err.Error() != err.Error() {
			log.Printf("agenda: %v", err)
		}
		c.snap.Err = err
		return
	}
	c.snap = Snapshot{Events: sortUnique(events), Fetched: now}
}

// sortUnique orders events by start, end and title, dropping duplicates,
// which are common when one meeting is on more than one watched calendar.
func sortUnique(events []Event) []Event {
	compare := func(a, b Event) int {
		return cmp.Or(a.Start.Compare(b.Start), a.End.Compare(b.End), cmp.Compare(a.Summary, b.Summary))
	}
	sorted := slices.Clone(events)
	slices.SortStableFunc(sorted, compare)
	return slices.CompactFunc(sorted, func(a, b Event) bool { return compare(a, b) == 0 })
}

// Google reads a set of Google calendars.
type Google struct {
	Calendars []string
	Client    *gcal.Client
}

// NewGoogle returns a source for calendars, authorized by the gcalcli or
// busy-indicator credential files. Credentials are read on first use.
func NewGoogle(calendars []string) *Google {
	return &Google{Calendars: calendars, Client: gcal.New(gauth.NewTokenSource())}
}

// Events reads every calendar. One calendar failing fails the whole fetch,
// so the cache keeps the last complete agenda, marked stale, rather than
// showing a partial one that looks complete.
func (g *Google) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	var all []Event
	for _, cal := range g.Calendars {
		events, err := g.Client.Events(ctx, cal, from, to, from.Location())
		if err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	return all, nil
}

// File reads events from a JSON file, for trying the dashboard without
// Google. Times are RFC 3339.
//
//	[{"summary": "Standup", "start": "2026-09-27T09:00:00-06:00",
//	  "end": "2026-09-27T09:15:00-06:00", "all_day": false}]
type File struct {
	Path string
}

// Events re-reads the file and returns the events overlapping from..to.
func (f File) Events(_ context.Context, from, to time.Time) ([]Event, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Summary string    `json:"summary"`
		Start   time.Time `json:"start"`
		End     time.Time `json:"end"`
		AllDay  bool      `json:"all_day"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", f.Path, err)
	}

	var out []Event
	for _, r := range raw {
		if r.End.After(from) && r.Start.Before(to) {
			out = append(out, Event{Summary: r.Summary, Start: r.Start.In(from.Location()), End: r.End.In(from.Location()), AllDay: r.AllDay})
		}
	}
	return out, nil
}
