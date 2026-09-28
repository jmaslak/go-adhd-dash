package tasks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jmaslak/go-task/config"
	"github.com/jmaslak/go-task/task"
)

// fakeTrello serves card updates, recording the cards closed and failing
// those in fail.
type fakeTrello struct {
	fail string

	mu     sync.Mutex
	closed []string
}

func (f *fakeTrello) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	card, ok := strings.CutPrefix(r.URL.Path, "/1/cards/")
	q := r.URL.Query()
	if !ok || r.Method != http.MethodPut || q.Get("closed") != "true" || q.Get("dueComplete") != "true" || q.Get("token") != "tok" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if card == f.fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.closed = append(f.closed, card)
	f.mu.Unlock()
	w.Write([]byte(`{"id":"` + card + `","closed":true}`)) //nolint:errcheck
}

// taskDir makes a task directory with the task program's own store, one task
// per title; a title of the form "name@card" mirrors that Trello card.
func taskDir(t *testing.T, titles ...string) string {
	t.Helper()
	dir := t.TempDir()
	store := task.NewStore(dir)
	for _, title := range titles {
		number, err := store.NextNumber()
		if err != nil {
			t.Fatal(err)
		}
		name, card, _ := strings.Cut(title, "@")
		tk := task.New(number, name)
		tk.TrelloID = card
		if err := store.Save(tk); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func titlesIn(t *testing.T, dir string) []string {
	t.Helper()
	ts, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, tk := range ts {
		if tk.Number != i+1 {
			t.Errorf("task %q numbered %d, want %d", tk.Title, tk.Number, i+1)
		}
		out = append(out, tk.Title)
	}
	return out
}

func newTestArchiver(dir string, trelloURL string) *Archiver {
	a := NewArchiver(dir)
	a.loadConfig = func() (*config.Config, error) {
		cfg := &config.Config{}
		if trelloURL != "" {
			cfg.Trello = config.Trello{APIKey: "key", Token: "tok", BaseURL: trelloURL}
		}
		return cfg, nil
	}
	return a
}

func TestArchive(t *testing.T) {
	trelloSrv := &fakeTrello{fail: "bad"}
	srv := httptest.NewServer(trelloSrv)
	defer srv.Close()

	dir := taskDir(t, "one", "two@c2", "three", "four@bad")
	a := newTestArchiver(dir, srv.URL)
	ts, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ts[1].TrelloID != "c2" {
		t.Fatalf("Trello-ID not read: %+v", ts[1])
	}
	if err := a.Check(ts); err != nil {
		t.Fatalf("Check: %v", err)
	}

	ctx := context.Background()
	if err := a.Archive(ctx, ts[1]); err != nil {
		t.Fatalf("archiving the Trello task: %v", err)
	}
	if len(trelloSrv.closed) != 1 || trelloSrv.closed[0] != "c2" {
		t.Errorf("cards closed: %v, want c2", trelloSrv.closed)
	}
	if got := strings.Join(titlesIn(t, dir), ","); got != "one,three,four" {
		t.Errorf("tasks left: %s", got)
	}
	done, _ := os.ReadDir(filepath.Join(dir, "done"))
	if len(done) != 1 {
		t.Errorf("done/ holds %d files, want 1", len(done))
	}

	// ts[2] was numbered 3 when read and is 2 now: found by ID all the same.
	if err := a.Archive(ctx, ts[2]); err != nil {
		t.Fatalf("archiving a renumbered task: %v", err)
	}
	if got := strings.Join(titlesIn(t, dir), ","); got != "one,four" {
		t.Errorf("tasks left: %s", got)
	}

	if err := a.Archive(ctx, ts[3]); err == nil || !strings.Contains(err.Error(), "Trello") {
		t.Errorf("Trello failing: got %v", err)
	}
	if got := strings.Join(titlesIn(t, dir), ","); got != "one,four" {
		t.Errorf("task archived though its Trello card was not closed: %s", got)
	}

	if err := a.Archive(ctx, ts[1]); err == nil || !strings.Contains(err.Error(), "no longer open") {
		t.Errorf("archiving a closed task: got %v", err)
	}
}

func TestArchiveCheck(t *testing.T) {
	dir := taskDir(t, "plain", "mirrored@c1")
	ts, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := newTestArchiver(dir, "")
	if err := a.Check(ts[:1]); err != nil {
		t.Errorf("plain task without Trello credentials: %v", err)
	}
	if err := a.Check(ts); err == nil || !strings.Contains(err.Error(), "Trello") {
		t.Errorf("Trello task without credentials: got %v", err)
	}
	noID := ts[0]
	noID.ID = nil
	if err := a.Check([]Task{noID}); err == nil || !strings.Contains(err.Error(), "no task ID") {
		t.Errorf("task without an ID: got %v", err)
	}
}
