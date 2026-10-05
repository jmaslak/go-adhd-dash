// Package db opens the server's SQLite database, shared by every session,
// which keeps the checklists and each user's timer, each package making
// its own tables in it.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // the "sqlite" driver
)

// Open opens the database at path, making it (readable only by its owner)
// if it does not exist, and refusing a file that is not a database.
func Open(path string) (*sql.DB, error) {
	// Made here, so that SQLite does not make it readable by anyone; its
	// journal files take the same permissions.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the database: %w", err)
	}
	f.Close() //nolint:errcheck // only made

	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Set("_txlock", "immediate") // a write waits for another to finish, not fails
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("opening the database %s: %w", path, err)
	}
	// sql.Open connects only when first used: used now, so that a file
	// that is not a database is refused here.
	if _, err := db.Exec(`SELECT count(*) FROM sqlite_schema`); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("opening the database %s: %w", path, err)
	}
	return db, nil
}

// DefaultPath is where the database is kept unless told otherwise:
// adhd-dash.db in the home directory.
func DefaultPath() string {
	return inHome("adhd-dash.db")
}

// OldDefaultPath is where the database was kept unless told otherwise
// while it held only checklists: adhd-dash-checklists.db in the home
// directory.
func OldDefaultPath() string {
	return inHome("adhd-dash-checklists.db")
}

func inHome(name string) string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, name)
	}
	return name
}

// Move renames the database at from, with its journal files, to to,
// unless there is already one at to or none at from, reporting whether it
// did. The database must not be in use.
func Move(from, to string) (bool, error) {
	if _, err := os.Lstat(to); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("moving the database to %s: %w", to, err)
	}
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("moving the database from %s: %w", from, err)
	}
	// The journals first, so that the database is never at to without
	// them; a journal at to with no database is ignored, and replaced.
	for _, suffix := range []string{"-wal", "-shm"} {
		err := os.Rename(from+suffix, to+suffix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("moving the database from %s to %s: %w", from, to, err)
		}
	}
	if err := os.Rename(from, to); err != nil {
		return false, fmt.Errorf("moving the database from %s to %s: %w", from, to, err)
	}
	return true, nil
}

// BackupTo writes a copy of db, as it is at one moment, to path, which
// must not exist. It is safe while others read and write it.
func BackupTo(db *sql.DB, path string) error {
	if _, err := db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backing up the database to %s: %w", path, err)
	}
	return nil
}

// Check reports whether db is sound.
func Check(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("checking the database: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("the database is damaged: %s", result)
	}
	return nil
}
