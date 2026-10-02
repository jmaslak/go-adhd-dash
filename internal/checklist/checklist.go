// Package checklist keeps named checklists in a JSON file, shared by every
// session. Each checklist is one user's.
//
// Checklists and their items carry IDs, unique across the file and never
// reused while it lasts, so that a change made on a screen drawn before
// another session changed the file still lands on the right item.
package checklist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Item is one line of a checklist.
type Item struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// Checklist is a named list of items, in the order they were added.
type Checklist struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Items []Item `json:"items"`

	// Active is set for a checklist starred as in use, which is listed
	// ahead of the others.
	Active bool `json:"active,omitempty"`

	// Owner is the ID of the user whose checklist it is; zero for one made
	// before checklists were each a user's, until AssignUnowned gives it
	// to someone.
	Owner int `json:"owner,omitempty"`
}

// Owned are the checklists of lists that are owner's, in the same order.
func Owned(lists []Checklist, owner int) []Checklist {
	var out []Checklist
	for _, l := range lists {
		if l.Owner == owner {
			out = append(out, l)
		}
	}
	return out
}

// file is the JSON file's contents.
type file struct {
	Checklists []Checklist `json:"checklists"`
}

// Store reads and writes the checklist file. One Store is shared by every
// session, so that changes do not interleave.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store for the file at path, which need not exist yet.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultPath is where checklists are kept unless told otherwise:
// adhd-dash-checklists.json in the home directory.
func DefaultPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-checklists.json")
	}
	return "adhd-dash-checklists.json"
}

// Path is the file the Store keeps.
func (s *Store) Path() string { return s.path }

// Load returns every checklist, in the order they were added. A missing file
// has none.
func (s *Store) Load() ([]Checklist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// AssignUnowned gives every checklist with no owner to owner, reporting how
// many there were. With none, the file is left as it is.
func (s *Store) AssignUnowned(owner int) (int, error) {
	lists, err := s.Load()
	if err != nil || !slices.ContainsFunc(lists, func(l Checklist) bool { return l.Owner == 0 }) {
		return 0, err
	}
	n := 0
	err = s.Update(func(lists *[]Checklist, _ func() int) error {
		n = 0
		for i := range *lists {
			if (*lists)[i].Owner == 0 {
				(*lists)[i].Owner = owner
				n++
			}
		}
		return nil
	})
	return n, err
}

// RemoveOwned removes every checklist whose owner gone says is gone,
// reporting how many there were. With none, the file is left as it is.
func (s *Store) RemoveOwned(gone func(owner int) bool) (int, error) {
	lists, err := s.Load()
	if err != nil || !slices.ContainsFunc(lists, func(l Checklist) bool { return gone(l.Owner) }) {
		return 0, err
	}
	n := 0
	err = s.Update(func(lists *[]Checklist, _ func() int) error {
		before := len(*lists)
		*lists = slices.DeleteFunc(*lists, func(l Checklist) bool { return gone(l.Owner) })
		n = before - len(*lists)
		return nil
	})
	return n, err
}

// Update reads the checklists, passes them to change, and writes back what
// it leaves, unless it returns an error. NextID gives change IDs for
// anything it adds.
func (s *Store) Update(change func(lists *[]Checklist, nextID func() int) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lists, err := s.read()
	if err != nil {
		return err
	}
	last := 0
	for _, l := range lists {
		last = max(last, l.ID)
		for _, it := range l.Items {
			last = max(last, it.ID)
		}
	}
	nextID := func() int {
		last++
		return last
	}
	if err := change(&lists, nextID); err != nil {
		return err
	}
	return s.write(lists)
}

func (s *Store) read() ([]Checklist, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading checklists: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reading checklists from %s: %w", s.path, err)
	}
	return f.Checklists, nil
}

// write replaces the file by renaming a new one over it, so that a reader
// never sees it half written and a failure leaves the old one.
func (s *Store) write(lists []Checklist) error {
	if lists == nil {
		lists = []Checklist{}
	}
	data, err := json.MarshalIndent(file{Checklists: lists}, "", "  ")
	if err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".checklists-*.tmp")
	if err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing checklists: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	return nil
}
