package checklist

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// open is a new database in a directory of its own, closed when the test
// ends.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "checklists.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck
	return s
}

// count is how many rows table has.
func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStore(t *testing.T) {
	s := open(t)
	if lists, err := s.Load(); err != nil || len(lists) != 0 {
		t.Fatalf("new database: %v, %v; want no checklists", lists, err)
	}
	if fi, err := os.Stat(s.Path()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("database mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}

	err := s.Update(func(lists *[]Checklist, nextID func() int) error {
		*lists = append(*lists, Checklist{ID: nextID(), Name: "Morning", Owner: 3, Active: true, Items: []Item{
			{ID: nextID(), Text: "Coffee", Done: true},
			{ID: nextID(), Text: "Pills"},
		}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lists, err := s.Load()
	if err != nil || len(lists) != 1 || lists[0].ID != 1 || lists[0].Owner != 3 || !lists[0].Active || len(lists[0].Items) != 2 ||
		lists[0].Items[1].ID != 3 || lists[0].Items[1].Text != "Pills" || !lists[0].Items[0].Done {
		t.Fatalf("after adding: %+v, %v", lists, err)
	}

	// An ID once given is not given again, even when what had it, the
	// highest, is gone.
	err = s.Update(func(lists *[]Checklist, nextID func() int) error {
		(*lists)[0].Items = (*lists)[0].Items[:1]
		*lists = append(*lists, Checklist{ID: nextID(), Name: "Evening"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lists, _ := s.Load(); len(lists) != 2 || lists[1].ID != 4 || len(lists[0].Items) != 1 {
		t.Errorf("after removing item 3: %+v; want a second checklist with ID 4", lists)
	}
	if err := s.Update(func(lists *[]Checklist, nextID func() int) error {
		*lists = (*lists)[:1]
		*lists = append(*lists, Checklist{ID: nextID(), Name: "Night"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lists, _ := s.Load(); len(lists) != 2 || lists[1].ID != 5 {
		t.Errorf("after removing checklist 4: %+v; want the new one with ID 5", lists)
	}

	// Moves: the checklists swapped, an item moved from one to the other.
	if err := s.Update(func(lists *[]Checklist, nextID func() int) error {
		l := *lists
		l[1].Items = append(l[1].Items, l[0].Items[0], Item{ID: nextID(), Text: "Teeth"})
		l[0].Items = nil
		l[0], l[1] = l[1], l[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lists, _ = s.Load()
	if len(lists) != 2 || lists[0].Name != "Night" || len(lists[0].Items) != 2 || lists[0].Items[0].Text != "Coffee" ||
		lists[0].Items[1].Text != "Teeth" || lists[1].Name != "Morning" || len(lists[1].Items) != 0 {
		t.Errorf("after moving: %+v", lists)
	}

	// A checklist removed takes its items with it.
	if err := s.Update(func(lists *[]Checklist, _ func() int) error {
		*lists = (*lists)[1:]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "items"); n != 0 {
		t.Errorf("%d items left after their checklist was removed", n)
	}

	// A change that fails writes nothing; nor does one with an ID used
	// twice.
	boom := errors.New("boom")
	if err := s.Update(func(lists *[]Checklist, _ func() int) error {
		*lists = nil
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("Update returned %v, want the change's error", err)
	}
	if err := s.Update(func(lists *[]Checklist, _ func() int) error {
		*lists = nil
		*lists = append(*lists, Checklist{ID: 9, Name: "a"}, Checklist{ID: 9, Name: "b"})
		return nil
	}); !errors.Is(err, ErrBadID) {
		t.Errorf("an ID used twice: %v", err)
	}
	if lists, _ := s.Load(); len(lists) != 1 || lists[0].Name != "Morning" {
		t.Errorf("a failed change was written: %+v", lists)
	}

	// What Load returns is a copy.
	lists, _ = s.Load()
	lists[0].Name = "changed"
	if again, _ := s.Load(); again[0].Name != "Morning" {
		t.Errorf("changing what Load returned changed the database")
	}

	// It is all there when opened again.
	path := s.Path()
	s.Close() //nolint:errcheck
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	if lists, _ := s.Load(); len(lists) != 1 || lists[0].Name != "Morning" {
		t.Errorf("opened again: %+v", lists)
	}
	if err := s.Update(func(lists *[]Checklist, nextID func() int) error {
		*lists = append(*lists, Checklist{ID: nextID(), Name: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lists, _ := s.Load(); lists[1].ID != 7 {
		t.Errorf("opened again, the next ID is %d; want 7", lists[1].ID)
	}
}

func TestOpenNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checklists.db")
	if err := os.WriteFile(path, []byte(`{"checklists": []}`+"\n"+string(make([]byte, 200))), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close() //nolint:errcheck
		t.Errorf("opened a file that is not a database")
	}
}

// seed adds checklists to s, with the names and owners given.
func seed(t *testing.T, s *Store, lists ...Checklist) {
	t.Helper()
	if err := s.Update(func(l *[]Checklist, nextID func() int) error {
		for _, c := range lists {
			c.ID = nextID()
			c.Items = []Item{{ID: nextID(), Text: "an item"}}
			*l = append(*l, c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAssignUnowned(t *testing.T) {
	s := open(t)
	seed(t, s, Checklist{Name: "Old"}, Checklist{Name: "Older"}, Checklist{Name: "Mine", Owner: 7})
	n, err := s.AssignUnowned(5)
	if err != nil || n != 2 {
		t.Fatalf("assigned %d, %v; want 2", n, err)
	}
	lists, _ := s.Load()
	if lists[0].Owner != 5 || lists[1].Owner != 5 || lists[2].Owner != 7 {
		t.Errorf("owners %d, %d, %d; want 5, 5, 7", lists[0].Owner, lists[1].Owner, lists[2].Owner)
	}
	if got := Owned(lists, 5); len(got) != 2 || got[0].Name != "Old" || got[1].Name != "Older" {
		t.Errorf("owned by 5: %+v", got)
	}
	if n, err := s.AssignUnowned(9); err != nil || n != 0 {
		t.Errorf("again: %d, %v; want none", n, err)
	}
}

func TestRemoveOwned(t *testing.T) {
	s := open(t)
	seed(t, s, Checklist{Name: "a", Owner: 1}, Checklist{Name: "b", Owner: 2}, Checklist{Name: "c", Owner: 2}, Checklist{Name: "d"})
	n, err := s.RemoveOwned(func(owner int) bool { return owner == 2 })
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2", n, err)
	}
	lists, _ := s.Load()
	if len(lists) != 2 || lists[0].Name != "a" || lists[1].Name != "d" {
		t.Errorf("left %+v", lists)
	}
	if n := count(t, s, "items"); n != 2 {
		t.Errorf("%d items left; want the 2 of the checklists left", n)
	}
	if n, err := s.RemoveOwned(func(owner int) bool { return owner == 2 }); err != nil || n != 0 {
		t.Errorf("again: %d, %v", n, err)
	}
}

func TestImportJSON(t *testing.T) {
	s := open(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "checklists.json")

	// No file, nothing to import.
	if n, err := s.ImportJSON(path); err != nil || n != 0 {
		t.Errorf("no file: %d, %v", n, err)
	}

	// As written before checklists had owners, and after.
	old := `{"checklists": [
		{"id": 1, "name": "Old", "items": [{"id": 4, "text": "one", "done": true}, {"id": 2, "text": "two"}]},
		{"id": 9, "name": "Mine", "items": [], "owner": 7, "active": true}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ImportJSON(path); err != nil || n != 2 {
		t.Fatalf("imported %d, %v; want 2", n, err)
	}
	lists, _ := s.Load()
	if len(lists) != 2 || lists[0].ID != 1 || lists[0].Owner != 0 || len(lists[0].Items) != 2 || lists[0].Items[0].ID != 4 ||
		!lists[0].Items[0].Done || lists[0].Items[1].Text != "two" || lists[1].ID != 9 || lists[1].Owner != 7 || !lists[1].Active {
		t.Errorf("imported %+v", lists)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the JSON file is still there: %v", err)
	}
	if _, err := os.Stat(path + ".imported"); err != nil {
		t.Errorf("the JSON file was not renamed: %v", err)
	}
	// IDs carry on past those imported.
	seed(t, s, Checklist{Name: "new"})
	if lists, _ := s.Load(); lists[2].ID != 10 {
		t.Errorf("after importing, the next ID is %d; want 10", lists[2].ID)
	}

	// Imported again, it clashes, and nothing is imported or renamed.
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportJSON(path); !errors.Is(err, ErrBadID) {
		t.Errorf("importing twice: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the clashing file was renamed: %v", err)
	}
	if lists, _ := s.Load(); len(lists) != 3 {
		t.Errorf("after the clash: %+v", lists)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportJSON(path); err == nil {
		t.Errorf("imported a corrupt file")
	}
}
