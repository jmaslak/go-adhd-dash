// Package tasks reads a user's open tasks from Trello: the cards on the
// lists they chose, each list's cards tagged as they said. Nothing is kept
// on disk; the cards are cached in memory and fetched again in the
// background.
package tasks

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Destination is a Trello list tasks are read from and added to, with the
// tag its tasks are given. The IDs find it; the names are for showing.
type Destination struct {
	BoardID, Board string
	ListID, List   string
	Tag            string
}

// String names d as "board / list".
func (d Destination) String() string { return d.Board + " / " + d.List }

// tags are the tags d's tasks are given: its tag, if it has one.
func (d Destination) tags() []string {
	if d.Tag == "" {
		return nil
	}
	return []string{d.Tag}
}

// Config is what a cache reads: the Trello credentials, and the lists its
// tasks come from, in order.
type Config struct {
	APIKey, Token string

	// BaseURL is the Trello API's address; "" for DefaultBaseURL.
	BaseURL string

	Lists []Destination
}

// Task is one open task: a card on one of the configured Trello lists.
type Task struct {
	// Number is the task's place in the list of tasks, from 1. It changes
	// as tasks are added and archived; CardID does not.
	Number int
	Title  string
	Tags   []string

	CardID string
	Dest   Destination

	// Pos is the card's position on its list, as Trello orders them.
	Pos float64
}

// renumber numbers ts in order, from 1.
func renumber(ts []Task) {
	for i := range ts {
		ts[i].Number = i + 1
	}
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
	Tasks []Task

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
	next       time.Time // when to fetch again
	refreshing bool

	// gen counts the local changes. A fetch begun before one may not
	// include it, so its result is thrown away and another begun.
	gen int

	fetches sync.WaitGroup // for tests to wait on
}

// NewCache returns a cache of the tasks config gives, which it calls at
// each fetch and change. Nothing is fetched until the first Snapshot.
func NewCache(config func() (Config, error)) *Cache {
	return &Cache{loadConfig: config, now: time.Now}
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
	cfg, err := c.loadConfig()
	if err == nil {
		var client *trelloClient
		client, err = newTrelloClient(cfg)
		switch {
		case err != nil:
		case len(cfg.Lists) == 0:
			err = errors.New("no Trello lists are chosen")
		default:
			ts, err = fetchTasks(ctx, client, cfg.Lists)
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
		c.snap = Snapshot{Tasks: ts, Fetched: now}
		c.next = now.Add(cacheTTL)
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

// Destinations are the Trello lists chosen, in order.
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

// Boards are the open boards the Trello token can see, each with its open
// lists: where a task can be moved to.
func (c *Cache) Boards(ctx context.Context) ([]Board, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	return Boards(ctx, cfg)
}

// Cards reads the open cards on d's list, which need not be one tasks are
// read from, as tasks numbered from 1, given d's tag. Nothing is cached.
func (c *Cache) Cards(ctx context.Context, d Destination) ([]Task, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	client, err := newTrelloClient(cfg)
	if err != nil {
		return nil, err
	}
	return fetchTasks(ctx, client, []Destination{d})
}

// Details reads a card's details, to look at. Nothing is cached.
func (c *Cache) Details(ctx context.Context, cardID string) (Details, error) {
	client, err := c.client()
	if err != nil {
		return Details{}, err
	}
	return client.cardDetails(ctx, cardID)
}

// Move moves t's card to the bottom of d's list, which need not be one its
// tasks are read from. In the cache, t goes after the other tasks of d's
// list if it is one of those, with its tag, and otherwise leaves.
func (c *Cache) Move(ctx context.Context, t Task, d Destination) error {
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	client, err := newTrelloClient(cfg)
	if err != nil {
		return err
	}
	if err := client.moveCard(ctx, t.CardID, d.BoardID, d.ListID); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.Tasks = slices.DeleteFunc(c.snap.Tasks, func(x Task) bool { return x.CardID == t.CardID })
	order := slices.IndexFunc(cfg.Lists, func(l Destination) bool { return l.ListID == d.ListID })
	if order >= 0 {
		shown := cfg.Lists[order]
		t.Dest, t.Tags = shown, shown.tags()
		c.snap.Tasks = slices.Insert(c.snap.Tasks, c.after(cfg.Lists, order), t)
	}
	c.changed()
	return nil
}

// after is where a task added to the bottom of lists[order] goes among the
// cached tasks: after the last of that list's, or of a list before it.
// c.mu must be held.
func (c *Cache) after(lists []Destination, order int) int {
	at := 0
	for i, t := range c.snap.Tasks {
		if slices.Index(lists, t.Dest) <= order {
			at = i + 1
		}
	}
	return at
}

// Add adds a task titled title as a card at the bottom of d's list,
// returning its number. If d is one of the lists tasks are read from, the
// task joins the cache at once, after the others on its list; if not, the
// number is 0.
func (c *Cache) Add(ctx context.Context, title string, d Destination) (int, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return 0, err
	}
	client, err := newTrelloClient(cfg)
	if err != nil {
		return 0, err
	}
	cardID, err := client.createCard(ctx, d.ListID, title)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	order := slices.IndexFunc(cfg.Lists, func(l Destination) bool { return l.ListID == d.ListID })
	if order < 0 {
		c.changed()
		return 0, nil
	}
	at := c.after(cfg.Lists, order)
	c.snap.Tasks = slices.Insert(c.snap.Tasks, at, Task{Title: title, Tags: cfg.Lists[order].tags(), CardID: cardID, Dest: cfg.Lists[order]})
	c.changed()
	return at + 1, nil
}

// Rename renames t's card title. In the cache, t takes the title at once.
func (c *Cache) Rename(ctx context.Context, t Task, title string) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	if err := client.renameCard(ctx, t.CardID, title); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snap.Tasks {
		if c.snap.Tasks[i].CardID == t.CardID {
			c.snap.Tasks[i].Title = title
		}
	}
	c.changed()
	return nil
}

// Reposition moves t's card within its list to pos: "top", "bottom", or a
// position as Trello numbers them. In the cache, t takes its new place
// among its list's tasks at once.
func (c *Cache) Reposition(ctx context.Context, t Task, pos string) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	at, err := client.positionCard(ctx, t.CardID, pos)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, _ := c.loadConfig()
	for i := range c.snap.Tasks {
		if c.snap.Tasks[i].CardID == t.CardID {
			c.snap.Tasks[i].Pos = at
		}
	}
	order := func(t Task) int {
		return slices.IndexFunc(cfg.Lists, func(l Destination) bool { return l.ListID == t.Dest.ListID })
	}
	slices.SortStableFunc(c.snap.Tasks, func(a, b Task) int {
		if oa, ob := order(a), order(b); oa != ob {
			return oa - ob
		}
		return cmp.Compare(a.Pos, b.Pos)
	})
	c.changed()
	return nil
}

// client is a Trello client for the configuration as it is now.
func (c *Cache) client() (*trelloClient, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	return newTrelloClient(cfg)
}

// Pool keeps a Cache for each of a set of task sources, by key. A key should
// change whenever the source would: the cache under the old one is dropped
// once unused for an hour. It is safe for concurrent use.
type Pool struct {
	mu      sync.Mutex
	entries map[string]*poolEntry
}

type poolEntry struct {
	cache    *Cache
	lastUsed time.Time
}

// poolIdle is how long a cache unused stays in a Pool.
const poolIdle = time.Hour

// NewPool returns an empty pool.
func NewPool() *Pool {
	return &Pool{entries: map[string]*poolEntry{}}
}

// Get returns the cache for key, making one over config if there is none.
func (p *Pool) Get(key string, config func() (Config, error)) *Cache {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, e := range p.entries {
		if k != key && now.Sub(e.lastUsed) > poolIdle {
			delete(p.entries, k)
		}
	}
	e, ok := p.entries[key]
	if !ok {
		e = &poolEntry{cache: NewCache(config)}
		p.entries[key] = e
	}
	e.lastUsed = now
	return e.cache
}
