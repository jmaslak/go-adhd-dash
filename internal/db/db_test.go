package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adhd-dash.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("database mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if err := Check(d); err != nil {
		t.Errorf("a new database is not sound: %v", err)
	}
	copied := filepath.Join(t.TempDir(), "copy.db")
	if err := BackupTo(d, copied); err != nil {
		t.Fatal(err)
	}
	c, err := Open(copied)
	if err != nil {
		t.Fatalf("the copy does not open: %v", err)
	}
	c.Close() //nolint:errcheck
}

func TestOpenNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adhd-dash.db")
	if err := os.WriteFile(path, []byte(`{"checklists": []}`+"\n"+string(make([]byte, 200))), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := Open(path); err == nil {
		d.Close() //nolint:errcheck
		t.Errorf("opened a file that is not a database")
	}
}

func TestMove(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "old.db"), filepath.Join(dir, "new.db")
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return "missing"
		}
		return string(data)
	}

	// Neither: nothing to do.
	if moved, err := Move(from, to); moved || err != nil {
		t.Errorf("neither: %v, %v", moved, err)
	}

	// The old, with its journals: all moved.
	write(from, "db")
	write(from+"-wal", "wal")
	write(from+"-shm", "shm")
	if moved, err := Move(from, to); !moved || err != nil {
		t.Fatalf("old: %v, %v", moved, err)
	}
	for suffix, want := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		if got := read(to + suffix); got != want {
			t.Errorf("%s%s: %q; want %q", to, suffix, got, want)
		}
		if got := read(from + suffix); got != "missing" {
			t.Errorf("%s%s left: %q", from, suffix, got)
		}
	}

	// Both: neither touched.
	write(from, "older")
	if moved, err := Move(from, to); moved || err != nil {
		t.Errorf("both: %v, %v", moved, err)
	}
	if read(from) != "older" || read(to) != "db" {
		t.Errorf("both: %q, %q", read(from), read(to))
	}
}
