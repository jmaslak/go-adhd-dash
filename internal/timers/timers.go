// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package timers keeps each user's timer in the server's SQLite database
// (see package db), shared by every session, so that all of a user's sessions show
// the same timer.
//
// A user has at most one timer, kept from when it starts until it is
// stopped, or, once it has run out, until DONE is acknowledged. With no
// user (owner zero), there is none.
package timers

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Timer is a user's timer.
type Timer struct {
	// Until is set for a timer to a time of day, rather than for how long.
	Until bool
	Total time.Duration
	End   time.Time

	// Note is what the timer is for, as the user put it.
	Note string
}

// schema makes the database's table: one row for each user with a timer,
// its end in Unix nanoseconds.
const schema = `
CREATE TABLE IF NOT EXISTS timers (
	owner    INTEGER PRIMARY KEY,
	until    INTEGER NOT NULL,
	total_ns INTEGER NOT NULL,
	end_ns   INTEGER NOT NULL,
	note     TEXT    NOT NULL DEFAULT 'Timer'
);
`

// addNote adds the note to a table made before timers had one, its
// timers' notes "Timer".
const addNote = `ALTER TABLE timers ADD COLUMN note TEXT NOT NULL DEFAULT 'Timer'`

// Store reads and writes the timers. One Store is shared by every session.
type Store struct {
	db *sql.DB
}

// New keeps the timers in db, which its opener closes, making their table
// if it is not there.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("making the timers' table: %w", err)
	}
	var hasNote bool
	if err := db.QueryRow(`SELECT count(*) > 0 FROM pragma_table_info('timers') WHERE name = 'note'`).Scan(&hasNote); err != nil {
		return nil, fmt.Errorf("reading the timers' table: %w", err)
	}
	if !hasNote {
		if _, err := db.Exec(addNote); err != nil {
			return nil, fmt.Errorf("adding notes to the timers' table: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// errNoOwner reports a timer started for no one: owner zero, as with no
// user logged in, has none.
var errNoOwner = errors.New("a timer is a logged-in user's")

// Get returns owner's timer, and whether they have one.
func (s *Store) Get(owner int) (Timer, bool, error) {
	if owner <= 0 {
		return Timer{}, false, nil
	}
	var t Timer
	var total, end int64
	err := s.db.QueryRow(`SELECT until, total_ns, end_ns, note FROM timers WHERE owner = ?`, owner).Scan(&t.Until, &total, &end, &t.Note)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Timer{}, false, nil
	case err != nil:
		return Timer{}, false, fmt.Errorf("reading the timer: %w", err)
	}
	t.Total, t.End = time.Duration(total), time.Unix(0, end)
	return t, true, nil
}

// Start makes t owner's timer, in place of any they had.
func (s *Store) Start(owner int, t Timer) error {
	if owner <= 0 {
		return errNoOwner
	}
	_, err := s.db.Exec(`INSERT INTO timers (owner, until, total_ns, end_ns, note) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (owner) DO UPDATE SET until = excluded.until, total_ns = excluded.total_ns, end_ns = excluded.end_ns, note = excluded.note`,
		owner, t.Until, int64(t.Total), t.End.UnixNano(), t.Note)
	if err != nil {
		return fmt.Errorf("starting the timer: %w", err)
	}
	return nil
}

// Clear removes owner's timer if it is t, so that a session stopping the
// timer it shows leaves alone one started since in another session.
func (s *Store) Clear(owner int, t Timer) error {
	_, err := s.db.Exec(`DELETE FROM timers WHERE owner = ? AND until = ? AND total_ns = ? AND end_ns = ?`,
		owner, t.Until, int64(t.Total), t.End.UnixNano())
	if err != nil {
		return fmt.Errorf("stopping the timer: %w", err)
	}
	return nil
}

// RemoveOwned removes every timer whose owner gone says is gone, reporting
// how many there were.
func (s *Store) RemoveOwned(gone func(owner int) bool) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("removing timers: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	rows, err := tx.Query(`SELECT owner FROM timers`)
	if err != nil {
		return 0, fmt.Errorf("removing timers: %w", err)
	}
	var owners []int
	for rows.Next() {
		var owner int
		if err := rows.Scan(&owner); err != nil {
			rows.Close() //nolint:errcheck
			return 0, fmt.Errorf("removing timers: %w", err)
		}
		if gone(owner) {
			owners = append(owners, owner)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("removing timers: %w", err)
	}
	for _, owner := range owners {
		if _, err := tx.Exec(`DELETE FROM timers WHERE owner = ?`, owner); err != nil {
			return 0, fmt.Errorf("removing timers: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("removing timers: %w", err)
	}
	return len(owners), nil
}
