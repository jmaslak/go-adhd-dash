// Package tasks reads the open tasks kept by the task program
// (github.com/jmaslak/go-task) and picks out the ones its default "task list"
// shows.
//
// Only the headers that decide what is listed are read. Task files are
// replaced by an atomic rename, so reading them without the task program's
// directory lock never sees a half-written file; at worst a listing is one
// change behind.
package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Task is one open task, as much of it as the dashboard shows.
type Task struct {
	Number int
	Title  string
	Tags   []string

	// NotBefore is the first day the task is shown, zero if unset.
	NotBefore time.Time

	// DisplayFrequency, above one, shows the task one day in that many.
	DisplayFrequency int64

	// ID is the task's permanent identifier, nil for a file written by the
	// Raku version that the task program has not yet upgraded.
	ID *big.Int

	// TrelloID is the Trello card the task mirrors, empty if none.
	TrelloID string
}

// taskFile matches an open task's file name, capturing its number.
var taskFile = regexp.MustCompile(`^(\d+)-.*\.task$`)

// bodyMarker matches the line introducing the first note, which ends the
// headers.
var bodyMarker = regexp.MustCompile(`^--- \d+$`)

// DefaultDir is the task directory the task program uses: $TASKDIR, else
// .task in the home directory.
func DefaultDir() string {
	if dir := os.Getenv("TASKDIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".task")
	}
	return ".task"
}

// ReadDir returns every open task in dir, ordered by number.
func ReadDir(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading task directory: %w", err)
	}

	var out []Task
	for _, entry := range entries {
		m := taskFile.FindStringSubmatch(entry.Name())
		if entry.IsDir() || m == nil {
			continue
		}
		number, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			// Closed or renumbered between the listing and the read.
			continue
		}
		if err != nil {
			return nil, err
		}
		t, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		t.Number = number
		out = append(out, t)
	}

	slices.SortFunc(out, func(a, b Task) int { return a.Number - b.Number })
	return out, nil
}

// parse reads a task file's headers.
func parse(data []byte) (Task, error) {
	var t Task
	for line := range strings.SplitSeq(string(data), "\n") {
		if bodyMarker.MatchString(line) {
			break
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimLeft(value, " \t")

		switch strings.ToLower(field) {
		case "title":
			t.Title = value
		case "tags":
			t.Tags = strings.Fields(value)
		case "not-before":
			day, err := time.ParseInLocation("2006-01-02", value, time.Local)
			if err != nil {
				return Task{}, fmt.Errorf("not-before: %w", err)
			}
			t.NotBefore = day
		case "display-frequency":
			freq, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Task{}, fmt.Errorf("display-frequency: %w", err)
			}
			t.DisplayFrequency = freq
		case "task-id":
			id, ok := new(big.Int).SetString(value, 10)
			if !ok {
				return Task{}, fmt.Errorf("invalid task-id %q", value)
			}
			t.ID = id
		case "trello-id":
			t.TrelloID = value
		}
	}
	if t.Title == "" {
		return Task{}, errors.New("no title")
	}
	return t, nil
}

// Mature reports whether the task has reached its not-before day.
func (t Task) Mature(now time.Time) bool {
	return t.NotBefore.IsZero() || !now.Before(t.NotBefore)
}

// DisplayToday reports whether a task with a display frequency comes up
// today. It uses the task program's calculation, (ID + Modified Julian Day)
// mod frequency, so both agree on which days a task is shown.
func (t Task) DisplayToday(now time.Time) bool {
	if t.DisplayFrequency <= 1 || t.ID == nil {
		return true
	}
	day := new(big.Int).Add(t.ID, big.NewInt(modifiedJulianDay(now)))
	return new(big.Int).Mod(day, big.NewInt(t.DisplayFrequency)).Sign() == 0
}

// modifiedJulianDay numbers the local calendar day now falls on.
func modifiedJulianDay(now time.Time) int64 {
	const unixEpochMJD = 40587
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()/86400 + unixEpochMJD
}

// Visible returns the tasks the task program's default listing shows: not
// carrying an ignored tag, due to be displayed today, and mature.
func Visible(all []Task, ignoreTags []string, now time.Time) []Task {
	var out []Task
	for _, t := range all {
		ignored := slices.ContainsFunc(t.Tags, func(tag string) bool {
			return slices.Contains(ignoreTags, tag)
		})
		if !ignored && t.DisplayToday(now) && t.Mature(now) {
			out = append(out, t)
		}
	}
	return out
}

// IgnoreTags reads ignore-tags from the task program's configuration,
// ~/.task.yaml, overridden by ~/.task.secret.yaml as the task program does.
// Missing files are not an error.
func IgnoreTags() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}

	var tags []string
	for _, name := range []string{".task.yaml", ".task.secret.yaml"} {
		path := filepath.Join(home, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var cfg struct {
			IgnoreTags []string `yaml:"ignore-tags"`
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if cfg.IgnoreTags != nil {
			tags = cfg.IgnoreTags
		}
	}
	return tags, nil
}
