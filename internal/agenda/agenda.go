// Package agenda keeps today's and tomorrow's meetings, refreshed in the
// background and shared by every dashboard session of a user, so that each
// connection does not call Google on its own.
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
	"strings"
	"sync"
	"time"

	"github.com/jmaslak/go-busy-indicator/gcal"
)

// Event is one meeting.
type Event struct {
	Summary    string
	Start, End time.Time

	// AllDay marks an event given as a date rather than a time. Its End is
	// midnight following the last day.
	AllDay bool

	// Calendar is the alias of the calendar the event is on, or of each of
	// them, comma-separated, when it is on several; empty when no aliases
	// are configured.
	Calendar string
}

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

// Pool keeps a Cache for each of a set of sources, by key, refreshing each
// only while it is in use: one not asked for in three refresh intervals is
// stopped and dropped. It is safe for concurrent use.
type Pool struct {
	ctx      context.Context
	interval time.Duration

	mu      sync.Mutex
	entries map[string]*poolEntry
}

type poolEntry struct {
	cache    *Cache
	stop     context.CancelFunc
	lastUsed time.Time
}

// NewPool returns a pool whose caches refresh every interval until ctx is
// canceled.
func NewPool(ctx context.Context, interval time.Duration) *Pool {
	return &Pool{ctx: ctx, interval: interval, entries: map[string]*poolEntry{}}
}

// Get returns the cache for key, starting one over the source newSource
// makes if there is none. A key should change whenever the source would:
// the cache under the old one is then dropped once unused.
func (p *Pool) Get(key string, newSource func() Source) *Cache {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, e := range p.entries {
		if k != key && now.Sub(e.lastUsed) > 3*p.interval {
			e.stop()
			delete(p.entries, k)
		}
	}
	e, ok := p.entries[key]
	if !ok {
		ctx, stop := context.WithCancel(p.ctx)
		e = &poolEntry{cache: NewCache(newSource()), stop: stop}
		p.entries[key] = e
		go e.cache.Run(ctx, p.interval)
	}
	e.lastUsed = now
	return e.cache
}

// Fetch reads the events between from and to straight from the source, for
// views beyond the today and tomorrow the cache keeps. Nothing is cached.
func (c *Cache) Fetch(ctx context.Context, from, to time.Time) ([]Event, error) {
	events, err := c.source.Events(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return sortUnique(events), nil
}

// sortUnique orders events by start, end and title, merging duplicates,
// which are common when one meeting is on more than one watched calendar.
// A merged event keeps every calendar alias it had, in the order the
// calendars were given.
func sortUnique(events []Event) []Event {
	compare := func(a, b Event) int {
		return cmp.Or(a.Start.Compare(b.Start), a.End.Compare(b.End), cmp.Compare(a.Summary, b.Summary))
	}
	sorted := slices.Clone(events)
	slices.SortStableFunc(sorted, compare)

	var out []Event
	for _, e := range sorted {
		if n := len(out); n > 0 && compare(out[n-1], e) == 0 {
			out[n-1].Calendar = mergeAliases(out[n-1].Calendar, e.Calendar)
			continue
		}
		out = append(out, e)
	}
	return out
}

// mergeAliases adds alias to the comma-separated list, unless it is empty
// or already there.
func mergeAliases(list, alias string) string {
	switch {
	case alias == "" || slices.Contains(strings.Split(list, ","), alias):
		return list
	case list == "":
		return alias
	default:
		return list + "," + alias
	}
}

// Google reads a set of Google calendars.
type Google struct {
	Calendars []string

	// Aliases name the calendars, in the same order, for their events'
	// Calendar; nil when they have none.
	Aliases []string

	Client *gcal.Client
}

// NewGoogle returns a source for calendars, authorized by tokens. aliases
// is nil, or one name for each calendar.
func NewGoogle(tokens gcal.TokenSource, calendars, aliases []string) *Google {
	return &Google{Calendars: calendars, Aliases: aliases, Client: gcal.New(tokens)}
}

// Events reads every calendar. One calendar failing fails the whole fetch,
// so the cache keeps the last complete agenda, marked stale, rather than
// showing a partial one that looks complete.
func (g *Google) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	var all []Event
	for i, cal := range g.Calendars {
		events, err := g.Client.Events(ctx, cal, from, to, from.Location())
		if err != nil {
			return nil, err
		}
		alias := ""
		if i < len(g.Aliases) {
			alias = g.Aliases[i]
		}
		for _, e := range events {
			all = append(all, Event{Summary: e.Summary, Start: e.Start, End: e.End, AllDay: e.AllDay, Calendar: alias})
		}
	}
	return all, nil
}

// File reads events from a JSON file, for trying the dashboard without
// Google. Times are RFC 3339; calendar, optional, is the event's calendar
// alias.
//
//	[{"summary": "Standup", "start": "2026-09-27T09:00:00-06:00",
//	  "end": "2026-09-27T09:15:00-06:00", "all_day": false, "calendar": "work"}]
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
		Summary  string    `json:"summary"`
		Start    time.Time `json:"start"`
		End      time.Time `json:"end"`
		AllDay   bool      `json:"all_day"`
		Calendar string    `json:"calendar"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", f.Path, err)
	}

	var out []Event
	for _, r := range raw {
		if r.End.After(from) && r.Start.Before(to) {
			out = append(out, Event{Summary: r.Summary, Start: r.Start.In(from.Location()), End: r.End.In(from.Location()), AllDay: r.AllDay, Calendar: r.Calendar})
		}
	}
	return out, nil
}
