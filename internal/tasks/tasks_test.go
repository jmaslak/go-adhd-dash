package tasks

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "00002-none.task", "Title: Second: with colon\nCreated: 1\nTask-Id: 5\nTags: b a\n--- 2\nTitle: not a header\n")
	writeFile(t, dir, "00001-none.task", "Title: First\nCreated: 1\nNot-Before: 2026-10-01\nDisplay-Frequency: 7\n")
	writeFile(t, dir, "notes.txt", "ignored")
	if err := os.Mkdir(filepath.Join(dir, "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "done"), "00003-none.task", "Title: Closed\n")

	got, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tasks, want 2: %+v", len(got), got)
	}
	if got[0].Number != 1 || got[0].Title != "First" || got[0].DisplayFrequency != 7 || got[0].NotBefore.IsZero() {
		t.Errorf("task 1 = %+v", got[0])
	}
	if got[1].Number != 2 || got[1].Title != "Second: with colon" || got[1].ID.Int64() != 5 || len(got[1].Tags) != 2 {
		t.Errorf("task 2 = %+v", got[1])
	}
}

func TestReadDirRejectsUntitled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "00001-none.task", "Created: 1\n")
	if _, err := ReadDir(dir); err == nil {
		t.Fatal("want error for task with no title")
	}
}

func TestDisplayToday(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.Local)
	mjd := modifiedJulianDay(now)
	if mjd != 61310 {
		t.Fatalf("MJD of 2026-09-27 = %d, want 61310", mjd)
	}

	// ID + MJD divisible by the frequency shows today; one more does not.
	due := Task{DisplayFrequency: 7, ID: big.NewInt(7*1000 - mjd%7)}
	if !due.DisplayToday(now) {
		t.Error("due task not displayed")
	}
	notDue := Task{DisplayFrequency: 7, ID: new(big.Int).Add(due.ID, big.NewInt(1))}
	if notDue.DisplayToday(now) {
		t.Error("task not due was displayed")
	}
	if !(Task{DisplayFrequency: 7}).DisplayToday(now) {
		t.Error("task without an ID should always display")
	}
}

func TestVisible(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.Local)
	all := []Task{
		{Number: 1, Title: "plain"},
		{Number: 2, Title: "ignored", Tags: []string{"shopping"}},
		{Number: 3, Title: "immature", NotBefore: time.Date(2026, 9, 28, 0, 0, 0, 0, time.Local)},
		{Number: 4, Title: "matures today", NotBefore: time.Date(2026, 9, 27, 0, 0, 0, 0, time.Local)},
	}
	got := Visible(all, []string{"shopping"}, now)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 4 {
		t.Errorf("Visible = %+v", got)
	}
}
