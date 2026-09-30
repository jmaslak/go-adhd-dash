// Package tasks reads the open tasks from Trello: the cards on the lists the
// task program's configuration (~/.task.yaml) names for trello-sync, each
// list's cards tagged as that configuration says. Nothing is kept on disk;
// the cards are cached in memory and fetched again in the background.
package tasks

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Task is one open task: a card on one of the configured Trello lists.
type Task struct {
	// Number is the task's place in the list of tasks, from 1. It changes
	// as tasks are added and archived; CardID does not.
	Number int
	Title  string
	Tags   []string

	CardID string
	Dest   Destination
}

// renumber numbers ts in order, from 1.
func renumber(ts []Task) {
	for i := range ts {
		ts[i].Number = i + 1
	}
}

// Visible returns the tasks the dashboard shows: those not carrying an
// ignored tag.
func Visible(all []Task, ignoreTags []string) []Task {
	var out []Task
	for _, t := range all {
		if !slices.ContainsFunc(t.Tags, func(tag string) bool { return slices.Contains(ignoreTags, tag) }) {
			out = append(out, t)
		}
	}
	return out
}

// How long fetched tasks are used before they are fetched again, and how
// long after a failed fetch it is tried again.
const (
	cacheTTL   = 15 * time.Minute
	cacheRetry = time.Minute
	// fetchTimeout bounds one fetch of every list.
	fetchTimeout = time.Minute
)

// Snapshot is the tasks as last fetched, with any local changes since.
type Snapshot struct {
	Tasks      []Task
	IgnoreTags []string

	// Fetched is when the tasks were last fetched, zero if never.
	Fetched time.Time

	// Err is why the last fetch failed, nil if it worked. The tasks from
	// the fetch before are kept.
	Err error

	// Loading is set while the first fetch is under way, with nothing yet
	// to show.
	Loading bool
}

// Cache holds the tasks, fetching them from Trello in the background when
// those it has are older than cacheTTL, or have been changed by adding or
// archiving one; the tasks it has are used meanwhile. It is safe for
// concurrent use.
type Cache struct {
	loadConfig func() (Config, error)
	now        func() time.Time

	mu         sync.Mutex
	snap       Snapshot
	listIDs    map[Destination]string // as of the last fetch
	next       time.Time              // when to fetch again
	refreshing bool

	// gen counts the local changes. A fetch begun before one may not
	// include it, so its result is thrown away and another begun.
	gen int

	fetches sync.WaitGroup // for tests to wait on
}

// NewCache returns a cache of the tasks configured in the task program's
// configuration, which is read at each fetch, so a change shows up without
// restarting. Nothing is fetched until the first Snapshot.
func NewCache() *Cache {
	return &Cache{loadConfig: LoadConfig, now: time.Now}
}

// Snapshot returns the tasks, beginning a fetch in the background if they
// are due one. A nil Cache has no tasks.
func (c *Cache) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{Err: errors.New("no task source")}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeFetch()
	s := c.snap
	s.Tasks = slices.Clone(s.Tasks)
	s.Loading = c.refreshing && s.Fetched.IsZero()
	return s
}

// maybeFetch begins a fetch if one is due and none is under way. c.mu must
// be held.
func (c *Cache) maybeFetch() {
	if c.refreshing || c.now().Before(c.next) {
		return
	}
	c.refreshing = true
	c.fetches.Add(1)
	go c.fetch(c.gen)
}

// fetch reads every configured list from Trello into the cache, unless a
// local change since gen has overtaken it.
func (c *Cache) fetch(gen int) {
	defer c.fetches.Done()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	var ts []Task
	var ids map[Destination]string
	cfg, err := c.loadConfig()
	if err == nil {
		var client *trelloClient
		client, err = newTrelloClient(cfg)
		switch {
		case err != nil:
		case len(cfg.Lists) == 0:
			err = errors.New("no Trello lists are configured (trello: tasks: in ~/.task.yaml)")
		default:
			ts, ids, err = fetchTasks(ctx, client, cfg.Lists)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing = false
	now := c.now()
	switch {
	case gen != c.gen:
		c.next = time.Time{}
		c.maybeFetch()
	case err != nil:
		c.snap.Err, c.next = err, now.Add(cacheRetry)
	default:
		c.snap = Snapshot{Tasks: ts, IgnoreTags: cfg.IgnoreTags, Fetched: now}
		c.listIDs, c.next = ids, now.Add(cacheTTL)
	}
}

// changed records a local change to the tasks and begins fetching them
// again, to catch up with Trello. c.mu must be held.
func (c *Cache) changed() {
	renumber(c.snap.Tasks)
	c.gen++
	c.next = time.Time{}
	c.maybeFetch()
}

// Destinations are the configured Trello lists, by board then list.
func (c *Cache) Destinations() ([]Destination, error) {
	cfg, err := c.loadConfig()
	return cfg.Lists, err
}

// Archive archives t: its card's due date is marked complete and the card
// archived. The task leaves the cache at once.
func (c *Cache) Archive(ctx context.Context, t Task) error {
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	client, err := newTrelloClient(cfg)
	if err != nil {
		return err
	}
	if err := client.closeCard(ctx, t.CardID); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Tasks = slices.DeleteFunc(c.snap.Tasks, func(x Task) bool { return x.CardID == t.CardID })
	c.changed()
	return nil
}

// Add adds a task titled title as a card at the bottom of d's list,
// returning its number. The task joins the cache at once, after the others
// on its list.
func (c *Cache) Add(ctx context.Context, title string, d Destination) (int, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return 0, err
	}
	client, err := newTrelloClient(cfg)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	listID := c.listIDs[d]
	c.mu.Unlock()
	if listID == "" {
		if listID, err = client.listID(ctx, d); err != nil {
			return 0, err
		}
	}
	cardID, err := client.createCard(ctx, listID, title)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// After the last task of d's list, or of a list before it.
	order := slices.Index(cfg.Lists, d)
	at := 0
	for i, t := range c.snap.Tasks {
		if slices.Index(cfg.Lists, t.Dest) <= order {
			at = i + 1
		}
	}
	c.snap.Tasks = slices.Insert(c.snap.Tasks, at, Task{Title: title, Tags: []string{d.Tag}, CardID: cardID, Dest: d})
	c.changed()
	return at + 1, nil
}
