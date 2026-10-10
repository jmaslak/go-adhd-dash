// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package recurring keeps each user's recurring tasks in the server's
// SQLite database (see package db), shared by every session.
//
// A recurring task comes due at a time of day on the days its schedule
// says: on chosen days of the week, every so many weeks; every so many
// days, on a fixed schedule; or so many days after it was last done. It
// is due from then until it is done, when it is given the next day it
// comes due. Days are calendar days, in the server's time zone.
package recurring

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Schedule is how a recurring task repeats.
type Schedule string

const (
	// Weekly is on the days of the week in Days, every Weeks weeks.
	Weekly Schedule = "weekly"
	// Every is every Interval days, on a fixed schedule from Start,
	// however late it is done.
	Every Schedule = "every"
	// After is Interval days after it was last done, at the time of day
	// it was done.
	After Schedule = "after"
)

// Limits on a schedule, and on a title.
const (
	MaxWeeks    = 52
	MaxInterval = 999
	MaxTitle    = 200 // runes
)

// Weekdays is a set of days of the week, a bit for each time.Weekday.
type Weekdays uint8

// Has reports whether d is in w.
func (w Weekdays) Has(d time.Weekday) bool { return w&(1<<d) != 0 }

// With is w with d in it.
func (w Weekdays) With(d time.Weekday) Weekdays { return w | 1<<d }

// Date is a calendar day, with no time zone.
type Date struct{ t time.Time } // midnight UTC

// DateOf is the day t is on, where t is.
func DateOf(t time.Time) Date {
	return NewDate(t.Year(), t.Month(), t.Day())
}

// NewDate is the day given, normalized as time.Date does.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// ParseDate reads a day written yyyy-mm-dd.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("%q is not a date, yyyy-mm-dd", s)
	}
	return Date{t}, nil
}

// String writes d as yyyy-mm-dd.
func (d Date) String() string { return d.t.Format(time.DateOnly) }

// IsZero reports whether d is the zero Date, no day.
func (d Date) IsZero() bool { return d.t.IsZero() }

// AddDays is n days after d.
func (d Date) AddDays(n int) Date { return Date{d.t.AddDate(0, 0, n)} }

// Weekday is d's day of the week.
func (d Date) Weekday() time.Weekday { return d.t.Weekday() }

// Equal reports whether d and e are the same day.
func (d Date) Equal(e Date) bool { return d.t.Equal(e.t) }

// Before reports whether d is before e.
func (d Date) Before(e Date) bool { return d.t.Before(e.t) }

// DaysSince is how many days d is after e: negative if before.
func (d Date) DaysSince(e Date) int { return int(d.t.Sub(e.t).Hours() / 24) }

// At is minutes after midnight on d, in loc.
func (d Date) At(minutes int, loc *time.Location) time.Time {
	return time.Date(d.t.Year(), d.t.Month(), d.t.Day(), 0, minutes, 0, 0, loc)
}

// later is the later of d and e.
func later(d, e Date) Date {
	if d.Before(e) {
		return e
	}
	return d
}

// monday is the Monday of d's week, weeks starting on Monday.
func (d Date) monday() Date {
	return d.AddDays(-((int(d.Weekday()) + 6) % 7))
}

// Task is one recurring task.
type Task struct {
	// ID is unique across the database and never reused.
	ID    int
	Owner int
	Title string

	Schedule Schedule
	Days     Weekdays // Weekly: the days of the week it is on
	Weeks    int      // Weekly: every this many weeks
	Interval int      // Every and After: this many days apart

	// At is the time of day it comes due, in minutes after midnight. With
	// After, once done, it is the time it was done.
	At int

	// Start is the first day it may come due; with Weekly and Every, the
	// schedule counts from it.
	Start Date

	// Due is the day it next comes due, at At. It is due from then until
	// it is done.
	Due Date

	// HideUntilDue keeps it off the dashboard until it is due, rather than
	// listing it while due soon or due later the same day.
	HideUntilDue bool
}

// DueAt is when t next comes due, in loc.
func (t Task) DueAt(loc *time.Location) time.Time { return t.Due.At(t.At, loc) }

// IsDue reports whether t is due at now.
func (t Task) IsDue(now time.Time) bool { return !now.Before(t.DueAt(now.Location())) }

// SameSchedule reports whether t and u come due on the same days at the
// same time.
func (t Task) SameSchedule(u Task) bool {
	return t.Schedule == u.Schedule && t.Days == u.Days && t.Weeks == u.Weeks &&
		t.Interval == u.Interval && t.At == u.At && t.Start.Equal(u.Start)
}

// Check reports what is wrong with t's title or schedule, if anything.
func (t Task) Check() error {
	switch {
	case strings.TrimSpace(t.Title) == "":
		return errors.New("a recurring task needs a title")
	case utf8.RuneCountInString(t.Title) > MaxTitle:
		return fmt.Errorf("a title can be at most %d characters", MaxTitle)
	case t.At < 0 || t.At >= 24*60:
		return errors.New("the time of day is not one")
	case t.Start.IsZero():
		return errors.New("a recurring task needs a start date")
	}
	switch t.Schedule {
	case Weekly:
		if t.Days == 0 || t.Days >= 1<<7 {
			return errors.New("a weekly task needs a day of the week")
		}
		if t.Weeks < 1 || t.Weeks > MaxWeeks {
			return fmt.Errorf("weeks apart go from 1 to %d", MaxWeeks)
		}
	case Every, After:
		if t.Interval < 1 || t.Interval > MaxInterval {
			return fmt.Errorf("days apart go from 1 to %d", MaxInterval)
		}
	default:
		return fmt.Errorf("%q is not a schedule", t.Schedule)
	}
	return nil
}

// anchor is the Monday of the week a Weekly task's weeks count from: that
// of the first day from Start on one of its days of the week.
func (t Task) anchor() Date {
	d := t.Start
	for !t.Days.Has(d.Weekday()) {
		d = d.AddDays(1)
	}
	return d.monday()
}

// First is the first day, from = from on, but none before Start, that t
// comes due by its schedule. With After, that is the later of the two.
// t must Check.
func (t Task) First(from Date) Date {
	from = later(from, t.Start)
	switch t.Schedule {
	case Every:
		late := from.DaysSince(t.Start) % t.Interval
		if late == 0 {
			return from
		}
		return from.AddDays(t.Interval - late)
	case Weekly:
		anchor := t.anchor()
		// Within Weeks weeks and a week, every day of the week comes
		// round in an on week.
		for d, n := from, 0; n < 7*(t.Weeks+1); d, n = d.AddDays(1), n+1 {
			if t.Days.Has(d.Weekday()) && (d.monday().DaysSince(anchor)/7)%t.Weeks == 0 {
				return d
			}
		}
		panic("recurring: no day found for a weekly task") // unreachable for a task that Checks
	}
	return from
}

// DoneAt is t once done at done, coming due next: for After, Interval
// days after done, at the minute of the day it was done; otherwise at its
// time on the first day by its schedule after both done's day and the day
// it was due, so that occurrences missed are skipped and one done early is
// not due again the same day.
func (t Task) DoneAt(done time.Time) Task {
	day := DateOf(done)
	if t.Schedule == After {
		t.Due, t.At = day.AddDays(t.Interval), done.Hour()*60+done.Minute()
		return t
	}
	t.Due = t.First(later(day, t.Due).AddDays(1))
	return t
}

// ErrChanged reports a task done, changed or removed since it was read.
var ErrChanged = errors.New("it was changed in another session")

// errNoOwner reports a task added for no one.
var errNoOwner = errors.New("a recurring task is a logged-in user's")

// schema makes the database's table. Days are kept yyyy-mm-dd; AUTOINCREMENT
// keeps IDs from being reused.
const schema = `
CREATE TABLE IF NOT EXISTS recurring (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	owner      INTEGER NOT NULL,
	title      TEXT    NOT NULL,
	schedule   TEXT    NOT NULL,
	days       INTEGER NOT NULL DEFAULT 0,
	weeks      INTEGER NOT NULL DEFAULT 1,
	every_days INTEGER NOT NULL DEFAULT 0,
	at_minute  INTEGER NOT NULL,
	start      TEXT    NOT NULL,
	due        TEXT    NOT NULL,
	hide_until_due INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS recurring_by_owner ON recurring (owner);
`

// addHideUntilDue adds hide_until_due to a table made before recurring
// tasks had it, none of its tasks hidden.
const addHideUntilDue = `ALTER TABLE recurring ADD COLUMN hide_until_due INTEGER NOT NULL DEFAULT 0`

// Store reads and writes the recurring tasks. One Store is shared by every
// session.
type Store struct {
	db *sql.DB
}

// New keeps the recurring tasks in db, which its opener closes, making
// their table if it is not there.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("making the recurring tasks' table: %w", err)
	}
	var hasHide bool
	if err := db.QueryRow(`SELECT count(*) > 0 FROM pragma_table_info('recurring') WHERE name = 'hide_until_due'`).Scan(&hasHide); err != nil {
		return nil, fmt.Errorf("reading the recurring tasks' table: %w", err)
	}
	if !hasHide {
		if _, err := db.Exec(addHideUntilDue); err != nil {
			return nil, fmt.Errorf("adding hide until due to the recurring tasks' table: %w", err)
		}
	}
	return &Store{db: db}, nil
}

const columns = `id, owner, title, schedule, days, weeks, every_days, at_minute, start, due, hide_until_due`

// scanner is a *sql.Row or *sql.Rows.
type scanner interface{ Scan(...any) error }

func scan(r scanner) (Task, error) {
	var t Task
	var start, due string
	if err := r.Scan(&t.ID, &t.Owner, &t.Title, &t.Schedule, &t.Days, &t.Weeks, &t.Interval, &t.At, &start, &due, &t.HideUntilDue); err != nil {
		return Task{}, err
	}
	var err error
	if t.Start, err = ParseDate(start); err != nil {
		return Task{}, fmt.Errorf("recurring task %d: %w", t.ID, err)
	}
	if t.Due, err = ParseDate(due); err != nil {
		return Task{}, fmt.Errorf("recurring task %d: %w", t.ID, err)
	}
	return t, nil
}

// List returns owner's recurring tasks, in the order they come due.
func (s *Store) List(owner int) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+columns+` FROM recurring WHERE owner = ? ORDER BY due, at_minute, id`, owner)
	if err != nil {
		return nil, fmt.Errorf("reading recurring tasks: %w", err)
	}
	var out []Task
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			rows.Close() //nolint:errcheck
			return nil, fmt.Errorf("reading recurring tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("reading recurring tasks: %w", err)
	}
	return out, nil
}

// Get returns owner's recurring task id.
func (s *Store) Get(owner, id int) (Task, error) {
	t, err := scan(s.db.QueryRow(`SELECT `+columns+` FROM recurring WHERE owner = ? AND id = ?`, owner, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Task{}, ErrChanged
	case err != nil:
		return Task{}, fmt.Errorf("reading the recurring task: %w", err)
	}
	return t, nil
}

// Add adds t, which must Check, as its owner's, first due on the first
// day by its schedule from today on, returning it with its ID and due day.
func (s *Store) Add(t Task, today Date) (Task, error) {
	if t.Owner <= 0 {
		return Task{}, errNoOwner
	}
	if err := t.Check(); err != nil {
		return Task{}, err
	}
	t.Due = t.First(today)
	res, err := s.db.Exec(`INSERT INTO recurring (owner, title, schedule, days, weeks, every_days, at_minute, start, due, hide_until_due)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Owner, t.Title, t.Schedule, t.Days, t.Weeks, t.Interval, t.At, t.Start.String(), t.Due.String(), t.HideUntilDue)
	if err != nil {
		return Task{}, fmt.Errorf("adding the recurring task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Task{}, fmt.Errorf("adding the recurring task: %w", err)
	}
	t.ID = int(id)
	return t, nil
}

// Update saves t, which must Check, over the task of its owner with its
// ID, returning it as saved. With its schedule unchanged, it comes due
// when it would have; changed, on the first day by the new one from today
// on.
func (s *Store) Update(t Task, today Date) (Task, error) {
	if err := t.Check(); err != nil {
		return Task{}, err
	}
	err := s.inTx(func(tx *sql.Tx) error {
		old, err := scan(tx.QueryRow(`SELECT `+columns+` FROM recurring WHERE owner = ? AND id = ?`, t.Owner, t.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrChanged
		} else if err != nil {
			return err
		}
		t.Due = old.Due
		if !t.SameSchedule(old) {
			t.Due = t.First(today)
		}
		_, err = tx.Exec(`UPDATE recurring SET title = ?, schedule = ?, days = ?, weeks = ?, every_days = ?, at_minute = ?, start = ?, due = ?, hide_until_due = ?
			WHERE owner = ? AND id = ?`,
			t.Title, t.Schedule, t.Days, t.Weeks, t.Interval, t.At, t.Start.String(), t.Due.String(), t.HideUntilDue, t.Owner, t.ID)
		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("saving the recurring task: %w", err)
	}
	return t, nil
}

// Rename gives owner's task id the title.
func (s *Store) Rename(owner, id int, title string) error {
	if err := (Task{Title: title}).checkTitle(); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE recurring SET title = ? WHERE owner = ? AND id = ?`, title, owner, id)
	if err != nil {
		return fmt.Errorf("renaming the recurring task: %w", err)
	}
	return changedOne(res)
}

// checkTitle is Check, for the title alone.
func (t Task) checkTitle() error {
	t.Schedule, t.Interval, t.Start = After, 1, NewDate(2000, 1, 1)
	return t.Check()
}

// Done marks owner's task id done at at, giving it when it next comes due
// (see Task.DoneAt), and returns it so. due is the day it was due when
// read: if it has been done or rescheduled since, nothing is changed, and
// the error is ErrChanged, so that it is not done twice for once.
func (s *Store) Done(owner, id int, due Date, at time.Time) (Task, error) {
	var t Task
	err := s.inTx(func(tx *sql.Tx) error {
		var err error
		t, err = scan(tx.QueryRow(`SELECT `+columns+` FROM recurring WHERE owner = ? AND id = ?`, owner, id))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrChanged
		case err != nil:
			return err
		case !t.Due.Equal(due):
			return ErrChanged
		}
		t = t.DoneAt(at)
		_, err = tx.Exec(`UPDATE recurring SET due = ?, at_minute = ? WHERE id = ?`, t.Due.String(), t.At, id)
		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("marking the recurring task done: %w", err)
	}
	return t, nil
}

// Remove removes owner's task id.
func (s *Store) Remove(owner, id int) error {
	res, err := s.db.Exec(`DELETE FROM recurring WHERE owner = ? AND id = ?`, owner, id)
	if err != nil {
		return fmt.Errorf("deleting the recurring task: %w", err)
	}
	return changedOne(res)
}

// changedOne reports ErrChanged unless res changed a row.
func changedOne(res sql.Result) error {
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrChanged
	}
	return nil
}

// RemoveOwned removes every recurring task whose owner gone says is gone,
// reporting how many there were.
func (s *Store) RemoveOwned(gone func(owner int) bool) (int, error) {
	n := 0
	err := s.inTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT DISTINCT owner FROM recurring`)
		if err != nil {
			return err
		}
		var owners []int
		for rows.Next() {
			var owner int
			if err := rows.Scan(&owner); err != nil {
				rows.Close() //nolint:errcheck
				return err
			}
			if gone(owner) {
				owners = append(owners, owner)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, owner := range owners {
			res, err := tx.Exec(`DELETE FROM recurring WHERE owner = ?`, owner)
			if err != nil {
				return err
			}
			removed, err := res.RowsAffected()
			if err != nil {
				return err
			}
			n += int(removed)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("removing recurring tasks: %w", err)
	}
	return n, nil
}

// inTx runs f in a transaction, committed if f returns nil.
func (s *Store) inTx(f func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}
