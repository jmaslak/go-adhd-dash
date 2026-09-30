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
