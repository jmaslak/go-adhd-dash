package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// fakeTrelloAPI is a Trello with two boards, for the token "tok1" or
// "tok2", recording the tokens revoked.
type fakeTrelloAPI struct {
	url     string
	mu      sync.Mutex
	revoked []string
}

func newFakeTrelloAPI(t *testing.T) *fakeTrelloAPI {
	t.Helper()
	f := &fakeTrelloAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("key") != "abc123" || (q.Get("token") != "tok1" && q.Get("token") != "tok2") {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		reply := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck
		switch p := r.URL.Path; {
		case p == "/1/members/me/boards":
			reply([]tasks.Board{
				{ID: "b1", Name: "Work", Lists: []tasks.List{{ID: "l1", Name: "Today"}, {ID: "l2", Name: "Later"}}},
				{ID: "b2", Name: "Home", Lists: []tasks.List{{ID: "l3", Name: "Inbox"}}},
			})
		case p == "/1/lists/l1/cards":
			reply([]map[string]any{{"id": "c1", "name": "write report", "pos": 1}})
		case p == "/1/lists/l3/cards":
			reply([]map[string]any{{"id": "c2", "name": "buy milk", "pos": 1}})
		case strings.HasPrefix(p, "/1/tokens/") && r.Method == http.MethodDelete:
			f.mu.Lock()
			f.revoked = append(f.revoked, strings.TrimPrefix(p, "/1/tokens/"))
			f.mu.Unlock()
			reply(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// trelloRig drives the Trello screens as a session does.
type trelloRig struct {
	t      *testing.T
	store  *users.Store
	tr     trelloState
	screen go3270.Screen
	rows   []string
}

func (r *trelloRig) draw() {
	r.t.Helper()
	r.screen, _, _ = r.tr.build(24, 80, now)
	r.rows = screenText(r.t, r.screen, 24, 80)
}

func (r *trelloRig) key(aid go3270.AID, typed map[string]string) (bool, string) {
	r.t.Helper()
	values := map[string]string{}
	for _, f := range r.screen {
		if f.Write {
			values[f.Name] = f.Content
		}
	}
	for k, v := range typed {
		values[k] = v
	}
	leave, said := r.tr.handle(go3270.Response{AID: aid, Values: values}, r.store, func(string, ...any) {})
	if !leave {
		r.draw()
	}
	return leave, said
}

func (r *trelloRig) text() string { return strings.Join(r.rows, "\n") }

func (r *trelloRig) link() *users.TrelloLink {
	r.t.Helper()
	list, _, err := r.store.TrelloClient()
	if err != nil {
		r.t.Fatal(err)
	}
	return list[0].Trello
}

// index is the field number of the list with id.
func (r *trelloRig) index(id string) string {
	for i, o := range r.tr.lists {
		if o.ListID == id {
			return string(rune('0' + i))
		}
	}
	r.t.Fatalf("no list %q", id)
	return ""
}

func TestTrelloLinkAndChoose(t *testing.T) {
	fake := newFakeTrelloAPI(t)
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if _, why := startTrello(store, 1, true, fake.url); !strings.Contains(why, "No Trello API key") {
		t.Errorf("no key: %q", why)
	}
	if err := store.SetTrelloClient(&users.TrelloClient{APIKey: "abc123"}); err != nil {
		t.Fatal(err)
	}
	if _, why := startTrello(store, 1, true, fake.url); !strings.Contains(why, "web site") {
		t.Errorf("no site: %q", why)
	}
	if err := store.SetSite(&users.Site{BaseURL: "https://adhd.example.com/"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Users: store, TaskPool: tasks.NewPool(), TrelloBaseURL: fake.url}
	u := &users.User{ID: 1}
	if cfg.tasksFor(u) != nil {
		t.Errorf("not linked, yet tasks")
	}

	tr, why := startTrello(store, 1, true, fake.url)
	if why != "" || tr.step != trelloConnect {
		t.Fatalf("start: %q, step %d", why, tr.step)
	}
	r := &trelloRig{t: t, store: store, tr: tr}
	r.draw()
	if !strings.Contains(r.text(), "https://adhd.example.com/trello") {
		t.Errorf("link screen:\n%s", r.text())
	}
	r.key(go3270.AIDEnter, nil)
	if !strings.Contains(r.tr.message, "Not linked yet") {
		t.Errorf("Enter too soon: %q", r.tr.message)
	}

	// Linked on the web site, Enter goes on to choosing lists.
	if err := store.ConnectTrello(1, "abc123", "tok1", "joelle_t"); err != nil {
		t.Fatal(err)
	}
	r.key(go3270.AIDEnter, nil)
	if r.tr.step != trelloChoose || len(r.tr.lists) != 3 || !strings.Contains(r.text(), "Work / Today") || !strings.Contains(r.text(), "Trello account joelle_t") {
		t.Fatalf("after linking: step %d:\n%s", r.tr.step, r.text())
	}

	// A tag with a bracket is refused.
	today, inbox := r.index("l1"), r.index("l3")
	r.key(go3270.AIDEnter, map[string]string{tSelField + today: "x", tTagField + today: "w[ork"})
	if !r.tr.isError || r.link().Lists != nil {
		t.Errorf("bad tag: %q", r.tr.message)
	}
	r.key(go3270.AIDEnter, map[string]string{tSelField + today: "x", tTagField + today: "work", tSelField + inbox: "x"})
	got := r.link().Lists
	if len(got) != 2 || got[0].ListID != "l1" || got[0].Tag != "work" || got[0].Board != "Work" || got[1].ListID != "l3" || got[1].Tag != "" {
		t.Fatalf("saved %+v, message %q", got, r.tr.message)
	}

	// The tasks are those lists' cards, tagged.
	cache := cfg.tasksFor(u)
	if cache == nil {
		t.Fatal("linked with lists, yet no tasks")
	}
	var snap tasks.Snapshot
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if snap = cache.Snapshot(); !snap.Fetched.IsZero() || snap.Err != nil {
			break
		}
	}
	if len(snap.Tasks) != 2 || snap.Tasks[0].Title != "write report" || strings.Join(snap.Tasks[0].Tags, ",") != "work" || snap.Tasks[1].Tags != nil {
		t.Errorf("tasks %+v, err %v", snap.Tasks, snap.Err)
	}

	// PF3 with changes asks first.
	if leave, _ := r.key(go3270.AIDPF3, map[string]string{tSelField + inbox: ""}); leave || !r.tr.leaveArmed {
		t.Errorf("PF3 with changes left, or did not ask")
	}
	if leave, _ := r.key(go3270.AIDPF3, nil); !leave {
		t.Errorf("second PF3 did not leave")
	}

	// A list chosen that Trello no longer has is shown as gone.
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[0].Trello.Lists = append((*list)[0].Trello.Lists, users.TrelloList{BoardID: "b9", Board: "Old", ListID: "l9", List: "Gone"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.tr, _ = startTrello(store, 1, true, fake.url)
	r.draw()
	if !strings.Contains(r.text(), "Old / Gone (gone from Trello)") {
		t.Errorf("gone list not shown:\n%s", r.text())
	}

	// Linking again keeps the lists.
	r.key(go3270.AIDPF9, nil)
	if err := store.ConnectTrello(1, "abc123", "tok2", "joelle_t"); err != nil {
		t.Fatal(err)
	}
	r.key(go3270.AIDEnter, nil)
	if l := r.link(); r.tr.step != trelloChoose || l.Token != "tok2" || len(l.Lists) != 3 {
		t.Fatalf("after linking again: step %d, %+v", r.tr.step, l)
	}

	// Unlinking asks, then forgets and revokes the token.
	r.key(go3270.AIDPF6, nil)
	if !strings.Contains(r.text(), "Unlink your Trello account?") {
		t.Fatalf("PF6:\n%s", r.text())
	}
	leave, said := r.key(go3270.AIDPF4, nil)
	if !leave || r.link() != nil || !strings.Contains(said, "Unlinked") {
		t.Errorf("PF4: leave %v, link %+v, said %q", leave, r.link(), said)
	}
	fake.mu.Lock()
	revoked := fake.revoked
	fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "tok2" {
		t.Errorf("revoked %v", revoked)
	}
	if cfg.tasksFor(u) != nil {
		t.Errorf("unlinked, yet tasks")
	}
}

func TestTrelloKeyScreen(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSite(&users.Site{BaseURL: "https://adhd.example.com/"}); err != nil {
		t.Fatal(err)
	}
	var k trelloKeyState
	var screen go3270.Screen
	var text string
	draw := func() {
		list, client, err := store.TrelloClient()
		site, _ := store.Site()
		screen, _, _ = buildTrelloKey(24, 80, now, list, client, site, err, &k)
		text = strings.Join(screenText(t, screen, 24, 80), "\n")
	}
	key := func(aid go3270.AID, typed map[string]string) bool {
		values := map[string]string{}
		for _, f := range screen {
			if f.Write {
				values[f.Name] = f.Content
			}
		}
		for n, v := range typed {
			values[n] = v
		}
		leave := k.handle(go3270.Response{AID: aid, Values: values}, store, func(string, ...any) {})
		draw()
		return leave
	}
	client := func() *users.TrelloClient {
		_, c, _ := store.TrelloClient()
		return c
	}
	draw()
	if !strings.Contains(text, "power-ups/admin") || !strings.Contains(text, "https://adhd.example.com") || !strings.Contains(text, "none set") {
		t.Errorf("no key:\n%s", text)
	}
	key(go3270.AIDEnter, map[string]string{tKeyField: "not hex!"})
	if !k.isError || client() != nil {
		t.Errorf("bad key: %q", k.message)
	}
	key(go3270.AIDEnter, map[string]string{tKeyField: "abc123"})
	if c := client(); c == nil || c.APIKey != "abc123" {
		t.Fatalf("saved %+v, %q", c, k.message)
	}

	// With a user linked, replacing it asks first.
	if err := store.ConnectTrello(1, "abc123", "tok", "x"); err != nil {
		t.Fatal(err)
	}
	key(go3270.AIDEnter, map[string]string{tKeyField: "def456"})
	if !k.confirming || !strings.Contains(text, "1 user linked through it") || client().APIKey != "abc123" {
		t.Fatalf("replacing:\n%s", text)
	}
	key(go3270.AIDPF4, nil)
	if client().APIKey != "def456" {
		t.Errorf("after PF4: %+v", client())
	}
	// Blanked, it is removed, after asking.
	key(go3270.AIDEnter, map[string]string{tKeyField: ""})
	key(go3270.AIDPF4, nil)
	if client() != nil {
		t.Errorf("not removed")
	}
	if key(go3270.AIDPF3, map[string]string{tKeyField: "abc"}) || !k.leaveArmed {
		t.Errorf("PF3 with something typed left, or did not ask")
	}
	if !key(go3270.AIDPF3, nil) {
		t.Errorf("second PF3 did not leave")
	}
}

func TestUsersRemoveWithTrello(t *testing.T) {
	fake := newFakeTrelloAPI(t)
	r := newUsersRig(t)
	r.u.trelloBaseURL = fake.url
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list, users.User{ID: nextID(), Name: "joelle", Trello: &users.TrelloLink{APIKey: "abc123", Token: "tok1"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.draw()
	r.key(go3270.AIDEnter, map[string]string{"usel:2": "D"})
	if !strings.Contains(strings.Join(r.rows, "\n"), "joelle with their Trello link") {
		t.Errorf("confirmation:\n%s", strings.Join(r.rows, "\n"))
	}
	r.key(go3270.AIDPF4, nil)
	fake.mu.Lock()
	revoked := fake.revoked
	fake.mu.Unlock()
	if len(r.list()) != 1 || len(revoked) != 1 || revoked[0] != "tok1" {
		t.Errorf("users %d, revoked %v", len(r.list()), revoked)
	}
}
