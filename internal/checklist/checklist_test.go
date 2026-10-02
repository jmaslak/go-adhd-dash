package checklist

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checklists.json")
	s := NewStore(path)

	if lists, err := s.Load(); err != nil || len(lists) != 0 {
		t.Fatalf("missing file: %v, %v; want no checklists", lists, err)
	}

	err := s.Update(func(lists *[]Checklist, nextID func() int) error {
		*lists = append(*lists, Checklist{ID: nextID(), Name: "Morning", Items: []Item{
			{ID: nextID(), Text: "Coffee", Done: true},
			{ID: nextID(), Text: "Pills"},
		}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lists, err := s.Load()
	if err != nil || len(lists) != 1 || lists[0].ID != 1 || len(lists[0].Items) != 2 ||
		lists[0].Items[1].ID != 3 || !lists[0].Items[0].Done {
		t.Fatalf("after adding: %+v, %v", lists, err)
	}

	// IDs carry on from the highest in the file as read, not reusing one
	// removed by the same change.
	err = s.Update(func(lists *[]Checklist, nextID func() int) error {
		(*lists)[0].Items = (*lists)[0].Items[:1]
		*lists = append(*lists, Checklist{ID: nextID(), Name: "Evening"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lists, _ := s.Load(); len(lists) != 2 || lists[1].ID != 4 {
		t.Errorf("second checklist got ID %d; want 4", lists[1].ID)
	}

	// A change that fails writes nothing.
	boom := errors.New("boom")
	if err := s.Update(func(lists *[]Checklist, _ func() int) error {
		*lists = nil
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("Update returned %v, want the change's error", err)
	}
	if lists, _ := s.Load(); len(lists) != 2 {
		t.Errorf("failed change was written: %+v", lists)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("directory holds %d files, want only the checklist file", len(entries))
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Errorf("Load of a corrupt file succeeded")
	}
	if err := s.Update(func(*[]Checklist, func() int) error { return nil }); err == nil {
		t.Errorf("Update over a corrupt file succeeded, which would have replaced it")
	}
}

func TestAssignUnowned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checklists.json")
	// As written before checklists had owners.
	if err := os.WriteFile(path, []byte(`{"checklists": [{"id": 1, "name": "Old", "items": []},
		{"id": 2, "name": "Older", "items": []}, {"id": 3, "name": "Mine", "items": [], "owner": 7}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
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

	// With none left unowned, the file is left alone.
	before, _ := os.Stat(path)
	if n, err := s.AssignUnowned(9); err != nil || n != 0 {
		t.Errorf("again: %d, %v; want none", n, err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file rewritten with nothing to assign")
	}
	// A missing file has none.
	if n, err := NewStore(filepath.Join(t.TempDir(), "none.json")).AssignUnowned(1); err != nil || n != 0 {
		t.Errorf("missing file: %d, %v", n, err)
	}
}

func TestRemoveOwned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checklists.json")
	if err := os.WriteFile(path, []byte(`{"checklists": [{"id": 1, "name": "a", "items": [], "owner": 1},
		{"id": 2, "name": "b", "items": [], "owner": 2}, {"id": 3, "name": "c", "items": [], "owner": 2},
		{"id": 4, "name": "d", "items": []}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	n, err := s.RemoveOwned(func(owner int) bool { return owner == 2 })
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2", n, err)
	}
	lists, _ := s.Load()
	if len(lists) != 2 || lists[0].Name != "a" || lists[1].Name != "d" {
		t.Errorf("left %+v", lists)
	}
	// With none to remove, the file is left alone.
	before, _ := os.Stat(path)
	if n, err := s.RemoveOwned(func(owner int) bool { return owner == 2 }); err != nil || n != 0 {
		t.Errorf("again: %d, %v", n, err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file rewritten with nothing to remove")
	}
}
