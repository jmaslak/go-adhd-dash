// Package checklist keeps named checklists in a SQLite database, shared by
// every session. Each checklist is one user's.
//
// Checklists and their items carry IDs, unique across the database and
// never reused, so that a change made on a screen drawn before another
// session changed the checklists still lands on the right item.
package checklist

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"

	_ "modernc.org/sqlite" // the "sqlite" driver
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

// schema makes the database's tables. Checklists and items are kept in
// the order of their position, and an item goes with its checklist.
// last_id is the highest ID ever given, so that none is used twice.
const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS checklists (
	id       INTEGER PRIMARY KEY,
	owner    INTEGER NOT NULL DEFAULT 0,
	name     TEXT    NOT NULL,
	active   INTEGER NOT NULL DEFAULT 0,
	position INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS items (
	id        INTEGER PRIMARY KEY,
	checklist INTEGER NOT NULL REFERENCES checklists (id) ON DELETE CASCADE,
	text      TEXT    NOT NULL,
	done      INTEGER NOT NULL DEFAULT 0,
	position  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS items_by_checklist ON items (checklist, position);
INSERT OR IGNORE INTO meta (key, value) VALUES ('last_id', 0);
`

// Store reads and writes the checklist database. One Store is shared by
// every session; writes are made one at a time, each in a transaction.
type Store struct {
	path string
	db   *sql.DB
	mu   sync.Mutex // held by Update
}

// Open opens the database at path, making it (readable only by its owner)
// if it does not exist.
func Open(path string) (*Store, error) {
	// Made here, so that SQLite does not make it readable by anyone; its
	// journal files take the same permissions.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening checklists: %w", err)
	}
	f.Close() //nolint:errcheck // only made

	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Set("_txlock", "immediate") // a write waits for another to finish, not fails
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("opening checklists in %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("opening checklists in %s: %w", path, err)
	}
	return &Store{path: path, db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DefaultPath is where checklists are kept unless told otherwise:
// adhd-dash-checklists.db in the home directory.
func DefaultPath() string {
	return inHome("adhd-dash-checklists.db")
}

// DefaultJSONPath is where checklists were kept, as JSON, before they were
// kept in a database: adhd-dash-checklists.json in the home directory.
func DefaultJSONPath() string {
	return inHome("adhd-dash-checklists.json")
}

func inHome(name string) string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, name)
	}
	return name
}

// Path is the database the Store keeps.
func (s *Store) Path() string { return s.path }

// Load returns every checklist, in order.
func (s *Store) Load() ([]Checklist, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("reading checklists: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // only read
	return load(tx)
}

// load reads every checklist and its items, in order.
func load(tx *sql.Tx) ([]Checklist, error) {
	rows, err := tx.Query(`SELECT id, owner, name, active FROM checklists ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("reading checklists: %w", err)
	}
	var lists []Checklist
	at := map[int]int{} // index in lists, by ID
	for rows.Next() {
		var l Checklist
		if err := rows.Scan(&l.ID, &l.Owner, &l.Name, &l.Active); err != nil {
			rows.Close() //nolint:errcheck
			return nil, fmt.Errorf("reading checklists: %w", err)
		}
		at[l.ID] = len(lists)
		lists = append(lists, l)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("reading checklists: %w", err)
	}

	rows, err = tx.Query(`SELECT id, checklist, text, done FROM items ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("reading checklist items: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var it Item
		var list int
		if err := rows.Scan(&it.ID, &list, &it.Text, &it.Done); err != nil {
			return nil, fmt.Errorf("reading checklist items: %w", err)
		}
		if i, ok := at[list]; ok {
			lists[i].Items = append(lists[i].Items, it)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading checklist items: %w", err)
	}
	return lists, nil
}

// AssignUnowned gives every checklist with no owner to owner, reporting how
// many there were.
func (s *Store) AssignUnowned(owner int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE checklists SET owner = ? WHERE owner = 0`, owner)
	if err != nil {
		return 0, fmt.Errorf("assigning checklists: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RemoveOwned removes every checklist whose owner gone says is gone, with
// its items, reporting how many there were.
func (s *Store) RemoveOwned(gone func(owner int) bool) (int, error) {
	n := 0
	err := s.Update(func(lists *[]Checklist, _ func() int) error {
		before := len(*lists)
		*lists = slices.DeleteFunc(*lists, func(l Checklist) bool { return gone(l.Owner) })
		n = before - len(*lists)
		return nil
	})
	return n, err
}

// Update reads the checklists, passes them to change, and writes back what
// it leaves, unless it returns an error: rows added, changed, moved or
// removed, all in one transaction. NextID gives change IDs for anything it
// adds.
func (s *Store) Update(change func(lists *[]Checklist, nextID func() int) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	before, err := load(tx)
	if err != nil {
		return err
	}
	var last int
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'last_id'`).Scan(&last); err != nil {
		return fmt.Errorf("reading checklists: %w", err)
	}
	last = max(last, highestID(before))
	lists := clone(before)
	nextID := func() int {
		last++
		return last
	}
	if err := change(&lists, nextID); err != nil {
		return err
	}
	last = max(last, highestID(lists)) // any given other than by nextID
	if err := write(tx, before, lists); err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	if _, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'last_id'`, last); err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("writing checklists: %w", err)
	}
	return nil
}

// highestID is the highest ID of lists and their items.
func highestID(lists []Checklist) int {
	last := 0
	for _, l := range lists {
		last = max(last, l.ID)
		for _, it := range l.Items {
			last = max(last, it.ID)
		}
	}
	return last
}

// ErrBadID reports a checklist or item, written by Update, with an ID that
// is not positive or that another has.
var ErrBadID = errors.New("an ID not positive, or used twice")

// clone copies lists, items and all, so that a change to the copy leaves
// lists as they were.
func clone(lists []Checklist) []Checklist {
	out := slices.Clone(lists)
	for i := range out {
		out[i].Items = slices.Clone(out[i].Items)
	}
	return out
}

// write makes the database's checklists, as before, into after: each
// checklist or item new, changed or moved is written, and each gone
// removed.
func write(tx *sql.Tx, before, after []Checklist) error {
	type place struct {
		list Checklist
		pos  int
	}
	type itemPlace struct {
		item      Item
		list, pos int
	}
	oldLists, oldItems := map[int]place{}, map[int]itemPlace{}
	for i, l := range before {
		oldLists[l.ID] = place{l, i}
		for j, it := range l.Items {
			oldItems[it.ID] = itemPlace{it, l.ID, j}
		}
	}

	keptLists, keptItems := map[int]bool{}, map[int]bool{}
	for i, l := range after {
		if l.ID <= 0 || keptLists[l.ID] {
			return fmt.Errorf("checklist %q, ID %d: %w", l.Name, l.ID, ErrBadID)
		}
		keptLists[l.ID] = true
		if old, ok := oldLists[l.ID]; !ok || old.pos != i || old.list.Owner != l.Owner || old.list.Name != l.Name || old.list.Active != l.Active {
			_, err := tx.Exec(`INSERT INTO checklists (id, owner, name, active, position) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET owner = excluded.owner, name = excluded.name, active = excluded.active, position = excluded.position`,
				l.ID, l.Owner, l.Name, l.Active, i)
			if err != nil {
				return err
			}
		}
	}
	for id := range oldLists {
		if !keptLists[id] {
			// Its items go with it.
			if _, err := tx.Exec(`DELETE FROM checklists WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}

	for _, l := range after {
		for j, it := range l.Items {
			if it.ID <= 0 || keptItems[it.ID] {
				return fmt.Errorf("item %q, ID %d: %w", it.Text, it.ID, ErrBadID)
			}
			keptItems[it.ID] = true
			if old, ok := oldItems[it.ID]; ok && old.list == l.ID && old.pos == j && old.item == it {
				continue
			}
			_, err := tx.Exec(`INSERT INTO items (id, checklist, text, done, position) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET checklist = excluded.checklist, text = excluded.text, done = excluded.done, position = excluded.position`,
				it.ID, l.ID, it.Text, it.Done, j)
			if err != nil {
				return err
			}
		}
	}
	for id, old := range oldItems {
		if !keptItems[id] && keptLists[old.list] {
			if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ImportJSON adds the checklists in the JSON file at path, as they were
// kept before there was a database, keeping their IDs, then renames the
// file with .imported after its name so that it is not imported again.
// It reports how many checklists there were; with no file, none. A
// checklist whose ID is already in the database is refused, and nothing
// imported.
func (s *Store) ImportJSON(path string) (int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("importing checklists: %w", err)
	}
	var f struct {
		Checklists []Checklist `json:"checklists"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("importing checklists from %s: %w", path, err)
	}
	err = s.Update(func(lists *[]Checklist, _ func() int) error {
		*lists = append(*lists, f.Checklists...)
		return nil
	})
	if errors.Is(err, ErrBadID) {
		return 0, fmt.Errorf("importing checklists from %s: some may already be in %s: %w", path, s.path, err)
	} else if err != nil {
		return 0, fmt.Errorf("importing checklists from %s: %w", path, err)
	}
	if err := os.Rename(path, path+".imported"); err != nil {
		return 0, fmt.Errorf("imported checklists from %s, but could not rename it: %w", path, err)
	}
	return len(f.Checklists), nil
}

// BackupTo writes a copy of the database, as it is at one moment, to path,
// which must not exist. It is safe while others read and write it.
func (s *Store) BackupTo(path string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backing up checklists to %s: %w", path, err)
	}
	return nil
}

// Check reports whether the database is sound, and its checklists can be
// read.
func (s *Store) Check() error {
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("checking checklists in %s: %w", s.path, err)
	}
	if result != "ok" {
		return fmt.Errorf("checklists in %s are damaged: %s", s.path, result)
	}
	_, err := s.Load()
	return err
}
