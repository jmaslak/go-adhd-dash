package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
