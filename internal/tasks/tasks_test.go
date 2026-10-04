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

// fakeTrello is three boards: Work, with lists Today (l1) and Later (l2),
// Home, with Inbox (l3), and Projects, with Someday (l4), whose cards are
// not read as tasks. It adds, moves and archives cards, and can be made to
// fail or to hold fetches until released.
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
			{ID: "b3", Name: "Projects", Lists: []List{{ID: "l4", Name: "Someday"}}},
		})
	case r.Method == http.MethodGet && p == "/1/members/me":
		reply(map[string]string{"id": "m1", "username": "joelle"})
	case r.Method == http.MethodDelete && p == "/1/tokens/tok":
		reply(map[string]any{})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/1/lists/") && strings.HasSuffix(p, "/cards"):
		list := strings.TrimSuffix(strings.TrimPrefix(p, "/1/lists/"), "/cards")
		if list != "l1" && list != "l2" && list != "l3" && list != "l4" {
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
	case r.Method == http.MethodGet && p == "/1/cards/c1" && q.Get("checklists") == "all" && q.Get("actions") == "commentCard":
		_, _ = w.Write([]byte(`{"id": "c1", "name": "second", "desc": "Bring the forms.\nAnd a pen.",
			"due": "2026-10-05T17:00:00.000Z", "dueComplete": false,
			"labels": [{"name": "urgent", "color": "red"}, {"name": "", "color": "blue"}],
			"checklists": [
				{"name": "Later", "pos": 200, "checkItems": [{"name": "file", "state": "incomplete", "pos": 1}]},
				{"name": "First", "pos": 100, "checkItems": [
					{"name": "sign", "state": "incomplete", "pos": 20}, {"name": "print", "state": "complete", "pos": 10}]}],
			"actions": [
				{"type": "commentCard", "date": "2026-10-04T15:00:00.000Z", "data": {"text": "Done soon?"}, "memberCreator": {"fullName": "Joelle M", "username": "joelle"}},
				{"type": "commentCard", "date": "2026-10-03T09:00:00.000Z", "data": {"text": "Started"}, "memberCreator": {"fullName": "", "username": "bob"}}]}`))
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/1/cards/") && q.Get("idList") != "" && q.Get("pos") == "bottom":
		id, list := strings.TrimPrefix(p, "/1/cards/"), q.Get("idList")
		if board := map[string]string{"l1": "b1", "l2": "b1", "l3": "b2", "l4": "b3"}[list]; board == "" || board != q.Get("idBoard") {
			http.Error(w, "invalid value for idList", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, c := range f.cards {
			if c.ID == id {
				f.cards[i].IDList, f.cards[i].Pos = list, 5000
				reply(trelloItem{ID: id, IDList: list})
				return
			}
		}
		http.Error(w, "The requested resource was not found.", http.StatusNotFound)
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/1/cards/") && q.Get("idList") == "" && (q.Get("name") != "" || q.Get("pos") != ""):
		id := strings.TrimPrefix(p, "/1/cards/")
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, c := range f.cards {
			if c.ID != id {
				continue
			}
			if name := q.Get("name"); name != "" {
				f.cards[i].Name = name
			}
			switch pos := q.Get("pos"); pos {
			case "":
			case "top", "bottom":
				edge := 0.0
				for _, o := range f.cards {
					if o.IDList == c.IDList && (pos == "top" && o.Pos < edge || pos == "bottom" && o.Pos > edge || edge == 0) {
						edge = o.Pos
					}
				}
				if pos == "top" {
					f.cards[i].Pos = edge / 2
				} else {
					f.cards[i].Pos = edge + 1000
				}
			default:
				fmt.Sscan(pos, &f.cards[i].Pos) //nolint:errcheck
			}
			reply(f.cards[i])
			return
		}
		http.Error(w, "The requested resource was not found.", http.StatusNotFound)
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

func TestCacheMove(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	c.Snapshot()
	c.fetches.Wait()
	ctx := context.Background()
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:first[work] 4:second[work]" {
		t.Fatalf("before: %s", got)
	}

	// Every list on every board is offered, the ones not read as tasks too.
	boards, err := c.Boards(ctx)
	if err != nil || len(boards) != 3 || boards[2].Lists[0].ID != "l4" {
		t.Fatalf("boards %+v, %v", boards, err)
	}

	// To a list read as tasks, on another board: it goes after that list's
	// tasks, with its tag.
	s := c.Snapshot()
	if err := c.Move(ctx, s.Tasks[2], testLists[0]); err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:first[home] 3:later[later] 4:second[work]" {
		t.Errorf("moved to Inbox: %s", got)
	}
	// To one that is not: it leaves.
	someday := Destination{BoardID: "b3", Board: "Projects", ListID: "l4", List: "Someday"}
	if err := c.Move(ctx, c.Snapshot().Tasks[3], someday); err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:first[home] 3:later[later]" {
		t.Errorf("moved to Someday: %s", got)
	}
	// Trello agrees.
	c.fetches.Wait()
	clock = clock.Add(time.Hour)
	c.Snapshot()
	c.fetches.Wait()
	if got := titles(c.Snapshot()); got != "1:home[home] 2:first[home] 3:later[later]" {
		t.Errorf("after fetching: %s", got)
	}
	f.mu.Lock()
	onSomeday := 0
	for _, card := range f.cards {
		if card.IDList == "l4" {
			onSomeday++
		}
	}
	f.mu.Unlock()
	if onSomeday != 1 {
		t.Errorf("%d cards on Someday, want 1", onSomeday)
	}

	// Its cards can be read, though they are not tasks.
	cards, err := c.Cards(ctx, someday)
	if err != nil || len(cards) != 1 || cards[0].Title != "second" || cards[0].Number != 1 || len(cards[0].Tags) != 0 {
		t.Errorf("Someday's cards %+v, %v", cards, err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:first[home] 3:later[later]" {
		t.Errorf("reading Someday changed the tasks: %s", got)
	}

	// A failure changes nothing.
	f.setFail(true)
	if _, err := c.Cards(ctx, someday); err == nil {
		t.Error("reading cards with Trello failing succeeded")
	}
	if err := c.Move(ctx, c.Snapshot().Tasks[0], someday); err == nil {
		t.Error("move with Trello failing succeeded")
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:first[home] 3:later[later]" {
		t.Errorf("tasks changed by a failure: %s", got)
	}
}

func TestCacheRenameAndReposition(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	c.Snapshot()
	c.fetches.Wait()
	ctx := context.Background()
	s := c.Snapshot()
	if got := titles(s); got != "1:home[home] 2:later[later] 3:first[work] 4:second[work]" {
		t.Fatalf("before: %s", got)
	}
	if s.Tasks[2].Pos != 100 || s.Tasks[3].Pos != 200 {
		t.Errorf("positions %v, %v; want Trello's, 100 and 200", s.Tasks[2].Pos, s.Tasks[3].Pos)
	}

	if err := c.Rename(ctx, s.Tasks[2], "the first"); err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:the first[work] 4:second[work]" {
		t.Errorf("renamed: %s", got)
	}

	// second to the top of Today: before the first, in the cache at once.
	if err := c.Reposition(ctx, c.Snapshot().Tasks[3], "top"); err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:second[work] 4:the first[work]" {
		t.Errorf("repositioned: %s", got)
	}
	// And to a number.
	if err := c.Reposition(ctx, c.Snapshot().Tasks[2], "150"); err != nil {
		t.Fatal(err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:the first[work] 4:second[work]" {
		t.Errorf("repositioned to 150: %s", got)
	}
	// Trello agrees.
	c.fetches.Wait()
	clock = clock.Add(time.Hour)
	c.Snapshot()
	c.fetches.Wait()
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:the first[work] 4:second[work]" {
		t.Errorf("after fetching: %s", got)
	}

	// Added to a list not read as tasks, the cache is left alone.
	someday := Destination{BoardID: "b3", Board: "Projects", ListID: "l4", List: "Someday"}
	if n, err := c.Add(ctx, "idea", someday); err != nil || n != 0 {
		t.Errorf("added to Someday: %d, %v; want 0", n, err)
	}
	if got := titles(c.Snapshot()); got != "1:home[home] 2:later[later] 3:the first[work] 4:second[work]" {
		t.Errorf("adding to Someday changed the tasks: %s", got)
	}
	if cards, _ := c.Cards(ctx, someday); len(cards) != 1 || cards[0].Title != "idea" {
		t.Errorf("Someday's cards %+v", cards)
	}

	f.setFail(true)
	if err := c.Rename(ctx, c.Snapshot().Tasks[0], "x"); err == nil {
		t.Error("rename with Trello failing succeeded")
	}
	if err := c.Reposition(ctx, c.Snapshot().Tasks[0], "top"); err == nil {
		t.Error("reposition with Trello failing succeeded")
	}
}

func TestCacheDetails(t *testing.T) {
	f := newFakeTrello()
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newTestCache(t, f, &clock)
	d, err := c.Details(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "second" || d.Notes != "Bring the forms.\nAnd a pen." || !d.Due.Equal(time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC)) || d.DueComplete {
		t.Errorf("details %+v", d)
	}
	if fmt.Sprint(d.Labels) != "[urgent blue]" {
		t.Errorf("labels %v; want named, or by color", d.Labels)
	}
	// Checklists and their items in Trello's order.
	if len(d.Checklists) != 2 || d.Checklists[0].Name != "First" || fmt.Sprint(d.Checklists[0].Items) != "[{print true} {sign false}]" || d.Checklists[1].Name != "Later" {
		t.Errorf("checklists %+v", d.Checklists)
	}
	if len(d.Comments) != 2 || d.Comments[0].Author != "Joelle M" || d.Comments[0].Text != "Done soon?" || d.Comments[1].Author != "bob" {
		t.Errorf("comments %+v", d.Comments)
	}
	if _, err := c.Details(context.Background(), "nope"); err == nil {
		t.Error("details of a card not there")
	}
	f.setFail(true)
	if _, err := c.Details(context.Background(), "c1"); err == nil {
		t.Error("details with Trello failing")
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
	if err != nil || len(boards) != 3 || boards[0].Name != "Work" || len(boards[0].Lists) != 2 || boards[1].Lists[0].ID != "l3" {
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
