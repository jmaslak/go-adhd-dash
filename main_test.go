package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmaslak/go-adhd-dash/internal/backup"
	"github.com/jmaslak/go-adhd-dash/internal/db"
)

func TestMoveLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adhd-dash-users.json")
	old := filepath.Join(dir, ".adhd-dash-users.json")

	// Neither: nothing to do.
	if said, err := moveLegacy(path); err != nil || said != "" {
		t.Errorf("neither: %q, %v", said, err)
	}

	// Only the old: moved.
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	said, err := moveLegacy(path)
	if err != nil || !strings.Contains(said, "moved") {
		t.Fatalf("only the old: %q, %v", said, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "old" {
		t.Errorf("new file %q, %v", data, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the old file is still there")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want it kept", info.Mode().Perm())
	}

	// Both: neither touched.
	if err := os.WriteFile(old, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if said, err := moveLegacy(path); err != nil || !strings.Contains(said, "both") {
		t.Errorf("both: %q, %v", said, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Errorf("the new file was overwritten: %q", data)
	}
	if data, _ := os.ReadFile(old); string(data) != "older" {
		t.Errorf("the old file was changed: %q", data)
	}
}

func TestMoveOldDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, old := db.DefaultPath(), db.OldDefaultPath()

	// None: nothing to do.
	if said, err := moveOldDB(path); err != nil || said != "" {
		t.Errorf("none: %q, %v", said, err)
	}

	// The old, while an older server holds its lock: refused, untouched.
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := backup.Lock(backup.LockPath(old))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moveOldDB(path); err == nil || !strings.Contains(err.Error(), "stop it") {
		t.Errorf("in use: %v", err)
	}
	unlock()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("moved while in use")
	}

	// The old alone: moved, and its lock file gone.
	if said, err := moveOldDB(path); err != nil || !strings.Contains(said, "moved") {
		t.Fatalf("old: %q, %v", said, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "old" {
		t.Errorf("new database %q, %v", data, err)
	}
	for _, p := range []string{old, backup.LockPath(old)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", p)
		}
	}

	// Both: neither touched.
	if err := os.WriteFile(old, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if said, err := moveOldDB(path); err != nil || !strings.Contains(said, "both") {
		t.Errorf("both: %q, %v", said, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Errorf("the new database was overwritten: %q", data)
	}
}
