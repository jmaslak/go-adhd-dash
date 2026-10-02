package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTrello is two boards: Work, with lists Today (l1) and Later (l2), and
// Home, with Inbox (l3). It adds and archives cards, and can be made to fail
// or to hold fetches until released.
type fakeTrello struct {
	mu     sync.Mutex
	cards  []trelloItem
	nextID int
	fail   bool

	// hold, when set, holds a request for a board's cards until it is
	// closed, replying with the cards as they were when it arrived, having
	// sent on held.
	hold chan struct{}
	held chan struct{}

	fetches int // requests for a board's cards
}

func (f *fakeTrello) setHold(hold chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = hold
}

func (f *fakeTrello) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *fakeTrello) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func newFakeTrello() *fakeTrello {
	return &fakeTrello{cards: []trelloItem{
		{ID: "c1", Name: "second", IDList: "l1", Pos: 200},
		{ID: "c2", Name: "first", IDList: "l1", Pos: 100},
		{ID: "c3", Name: "later", IDList: "l2", Pos: 1},
		{ID: "c4", Name: "home", IDList: "l3", Pos: 1},
	}, nextID: 5}
}

func (f *fakeTrello) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	fail, hold, held := f.fail, f.hold, f.held
	f.mu.Unlock()
	if fail || q.Get("key") != "key" || q.Get("token") != "tok" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck
	switch p := r.URL.Path; {
	case r.Method == http.MethodGet && p == "/1/members/me/boards" && q.Get("lists") == "open":
		reply([]Board{
			{ID: "b1", Name: "Work", Lists: []List{{ID: "l1", Name: "Today"}, {ID: "l2", Name: "Later"}}},
			{ID: "b2", Name: "Home", Lists: []List{{ID: "l3", Name: "Inbox"}}},
		})
	case r.Method == http.MethodGet && p == "/1/members/me":
		reply(map[string]string{"id": "m1", "username": "joelle"})
	case r.Method == http.MethodDelete && p == "/1/tokens/tok":
		reply(map[string]any{})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/1/lists/") && strings.HasSuffix(p, "/cards"):
		list := strings.TrimSuffix(strings.TrimPrefix(p, "/1/lists/"), "/cards")
		if list != "l1" && list != "l2" && list != "l3" {
			http.Error(w, "The requested resource was not found.", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.fetches++
		var cards []trelloItem
		for _, c := range f.cards {
			if c.IDList == list {
				cards = append(cards, c)
			}
		}
		f.mu.Unlock()
		if hold != nil {
			if held != nil {
				held <- struct{}{}
			}
			<-hold
		}
		reply(cards)
	case r.Method == http.MethodPost && p == "/1/cards" && q.Get("pos") == "bottom":
		f.mu.Lock()
		card := trelloItem{ID: fmt.Sprint("c", f.nextID), Name: q.Get("name"), IDList: q.Get("idList"), Pos: 1000}
		f.nextID++
		f.cards = append(f.cards, card)
		f.mu.Unlock()
		reply(card)
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/1/cards/") && q.Get("closed") == "true" && q.Get("dueComplete") == "true":
		id := strings.TrimPrefix(p, "/1/cards/")
		f.mu.Lock()
		for i, c := range f.cards {
			if c.ID == id {
				f.cards = append(f.cards[:i], f.cards[i+1:]...)
				break
			}
		}
		f.mu.Unlock()
		reply(trelloItem{ID: id, Closed: true})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// testLists are the lists the test configuration names, in order.
var testLists = []Destination{
	{BoardID: "b2", Board: "Home", ListID: "l3", List: "Inbox", Tag: "home"},
	{BoardID: "b1", Board: "Work", ListID: "l2", List: "Later", Tag: "later"},
	{BoardID: "b1", Board: "Work", ListID: "l1", List: "Today", Tag: "work"},
}

// newTestCache is a cache of the fake Trello's lists, its clock at *clock.
func newTestCache(t *testing.T, f *fakeTrello, clock *time.Time) *Cache {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewCache(func() (Config, error) {
		return Config{APIKey: "key", Token: "tok", BaseURL: srv.URL, Lists: testLists}, nil
	})
	var mu sync.Mutex
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return *clock
	}
	return c
}

// titles are the snapshot's tasks, numbered.
func titles(s Snapshot) string {
	var out []string
	for _, tk := range s.Tasks {
		out = append(out, fmt.Sprintf("%d:%s[%s]", tk.Number, tk.Title, strings.Join(tk.Tags, ",")))
	}
	return strings.Join(out, " ")
}

func TestCacheFetchesInBackground(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)

	hold := make(chan struct{})
	f.setHold(hold)
	if s := c.Snapshot(); !s.Loading || len(s.Tasks) != 0 {
		t.Fatalf("first snapshot %+v, want loading", s)
	}
	close(hold)
	c.fetches.Wait()
	f.setHold(nil)

	s := c.Snapshot()
	if got := titles(s); got != "1:home[home] 2:later[later] 3:first[work] 4:second[work]" {
		t.Errorf("tasks %s", got)
	}
	if s.Loading || s.Err != nil || !s.Fetched.Equal(clock) {
		t.Errorf("snapshot %+v", s)
	}
	if n := f.fetchCount(); n != 3 {
		t.Errorf("%d card fetches, want one per list", n)
	}

	// Within the TTL, nothing is fetched.
	clock = clock.Add(cacheTTL - time.Second)
	c.Snapshot()
	c.fetches.Wait()
	if f.fetchCount() != 3 {
		t.Errorf("fetched again within the TTL")
	}

	// After it, the old tasks are used while the new are fetched.
	clock = clock.Add(time.Second)
	f.mu.Lock()
	f.cards = f.cards[1:]
	f.mu.Unlock()
	hold = make(chan struct{})
	f.setHold(hold)
	if got := titles(c.Snapshot()); !strings.Contains(got, "second") {
		t.Errorf("old tasks not used while fetching: %s", got)
	}
	close(hold)
	c.fetches.Wait()
	f.setHold(nil)
	if got := titles(c.Snapshot()); strings.Contains(got, "second") {
		t.Errorf("tasks not fetched after the TTL: %s", got)
	}
}

func TestCacheKeepsTasksWhenFetchFails(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	c.Snapshot()
	c.fetches.Wait()
	fetched := clock

	f.setFail(true)
	clock = clock.Add(cacheTTL)
	c.Snapshot()
	c.fetches.Wait()
	s := c.Snapshot()
	if s.Err == nil || !strings.Contains(s.Err.Error(), "401") || len(s.Tasks) != 4 || !s.Fetched.Equal(fetched) {
		t.Errorf("after a failed fetch: %+v", s)
	}
	if strings.Contains(s.Err.Error(), "tok") {
		t.Errorf("error shows the token: %v", s.Err)
	}

	// Tried again after cacheRetry, not before.
	f.setFail(false)
	clock = clock.Add(cacheRetry - time.Second)
	c.Snapshot()
	c.fetches.Wait()
	if c.Snapshot().Err == nil {
		t.Error("fetched again before cacheRetry")
	}
	clock = clock.Add(time.Second)
	c.Snapshot()
	c.fetches.Wait()
	if s := c.Snapshot(); s.Err != nil || !s.Fetched.Equal(clock) {
		t.Errorf("not fetched again after cacheRetry: %+v", s)
	}
}

func TestCacheArchiveAndAdd(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	c.Snapshot()
	c.fetches.Wait()
	ctx := context.Background()

	// Changed in the cache at once, while fetching again in the background.
	hold := make(chan struct{})
	f.setHold(hold)
	s := c.Snapshot()
	if err := c.Archive(ctx, s.Tasks[2]); err != nil {
		t.Fatal(err)
	}
	n, err := c.Add(ctx, "new", testLists[1])
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); n != 3 || got != "1:home[home] 2:later[later] 3:new[later] 4:second[work]" {
		t.Errorf("added as %d; tasks %s", n, got)
	}
	close(hold)
	c.fetches.Wait()
	f.setHold(nil)

	// The fetch agrees, whatever order it ran in against the changes.
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:new[later] 4:second[work]" {
		t.Errorf("after fetching: %s", got)
	}
	if s := c.Snapshot(); !s.Fetched.Equal(clock) || s.Err != nil {
		t.Errorf("snapshot %+v", s)
	}

	f.setFail(true)
	if err := c.Archive(ctx, c.Snapshot().Tasks[0]); err == nil {
		t.Error("archive with Trello failing succeeded")
	}
	if _, err := c.Add(ctx, "x", testLists[0]); err == nil {
		t.Error("add with Trello failing succeeded")
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:new[later] 4:second[work]" {
		t.Errorf("tasks changed by failures: %s", got)
	}
}

func TestCacheDiscardsOvertakenFetch(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	c.Snapshot()
	c.fetches.Wait()

	// A fetch reads Home's cards, home among them, then home is archived
	// before the fetch ends: its result must not bring home back.
	clock = clock.Add(cacheTTL)
	hold, held := make(chan struct{}), make(chan struct{}, 1)
	f.mu.Lock()
	f.hold, f.held = hold, held
	f.mu.Unlock()
	c.Snapshot()
	<-held
	f.mu.Lock()
	f.held = nil
	f.mu.Unlock()
	if err := c.Archive(context.Background(), c.Snapshot().Tasks[0]); err != nil {
		t.Fatal(err)
	}
	close(hold)
	c.fetches.Wait()
	if got := titles(c.Snapshot()); got != "1:later[later] 2:first[work] 3:second[work]" {
		t.Errorf("after the overtaken fetch: %s", got)
	}
	if n := f.fetchCount(); n != 9 {
		t.Errorf("%d card fetches, want 9: the overtaken fetch's result thrown away and another made", n)
	}
}

func TestCacheNotConfigured(t *testing.T) {
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{Lists: testLists}, "no Trello authorization"},
		{Config{APIKey: "k", Token: "t"}, "no Trello lists are chosen"},
	} {
		cache := NewCache(func() (Config, error) { return c.cfg, nil })
		cache.now = func() time.Time { return clock }
		cache.Snapshot()
		cache.fetches.Wait()
		if s := cache.Snapshot(); s.Err == nil || !strings.Contains(s.Err.Error(), c.want) {
			t.Errorf("got %v, want %q", s.Err, c.want)
		}
	}
	var nilCache *Cache
	if s := nilCache.Snapshot(); s.Err == nil {
		t.Error("nil cache has no error")
	}
}

func TestFetchTasksErrors(t *testing.T) {
	srv := httptest.NewServer(newFakeTrello())
	defer srv.Close()
	client, err := newTrelloClient(Config{APIKey: "key", Token: "tok", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	gone := Destination{BoardID: "b1", Board: "Work", ListID: "lx", List: "Gone", Tag: "x"}
	if _, err := fetchTasks(context.Background(), client, []Destination{gone}); err == nil || !strings.Contains(err.Error(), "the list Work / Gone") || !strings.Contains(err.Error(), "404") {
		t.Errorf("a list gone: %v", err)
	}
}

func TestTrelloHelpers(t *testing.T) {
	srv := httptest.NewServer(newFakeTrello())
	defer srv.Close()
	cfg := Config{APIKey: "key", Token: "tok", BaseURL: srv.URL}
	ctx := context.Background()

	boards, err := Boards(ctx, cfg)
	if err != nil || len(boards) != 2 || boards[0].Name != "Work" || len(boards[0].Lists) != 2 || boards[1].Lists[0].ID != "l3" {
		t.Errorf("boards %+v, %v", boards, err)
	}
	if name, err := Member(ctx, cfg); err != nil || name != "joelle" {
		t.Errorf("member %q, %v", name, err)
	}
	if err := Revoke(ctx, cfg); err != nil {
		t.Errorf("revoke: %v", err)
	}
	bad := cfg
	bad.Token = "wrong"
	if _, err := Member(ctx, bad); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("bad token: %v; want an error, without the token in it", err)
	}
	if err := Revoke(ctx, bad); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("revoking a bad token: %v; want an error, without the token in it", err)
	}
	if _, err := Boards(ctx, Config{}); err == nil {
		t.Errorf("boards with no authorization")
	}

	u, err := url.Parse(AuthorizeURL("key", "exec-3270", "https://adhd.example.com/trello/callback"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"key": "key", "name": "exec-3270", "scope": "read,write", "expiration": "never",
		"response_type": "token", "callback_method": "fragment", "return_url": "https://adhd.example.com/trello/callback",
	} {
		if q.Get(k) != want {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestPool(t *testing.T) {
	p := NewPool()
	made := 0
	config := func() (Config, error) { made++; return Config{}, nil }
	a := p.Get("a", config)
	if p.Get("a", config) != a || p.Get("b", config) == a {
		t.Errorf("caches not kept by key")
	}
	p.mu.Lock()
	p.entries["a"].lastUsed = time.Now().Add(-poolIdle - time.Minute)
	p.mu.Unlock()
	p.Get("b", config)
	if p.Get("a", config) == a {
		t.Errorf("idle cache kept")
	}
}
