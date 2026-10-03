package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// setup makes a users file and a checklist database, with a checklist
// named name, in a directory of its own, and returns them.
func setup(t *testing.T) Files {
	t.Helper()
	dir := t.TempDir()
	f := Files{Users: filepath.Join(dir, "users.json"), Checklists: filepath.Join(dir, "cl.db")}
	if _, _, err := users.NewStore(f.Users).Load(); err != nil {
		t.Fatal(err)
	}
	setChecklist(t, f, "before")
	return f
}

// setChecklist makes the database's one checklist named name.
func setChecklist(t *testing.T, f Files, name string) {
	t.Helper()
	s, err := checklist.Open(f.Checklists)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	if err := s.Update(func(l *[]checklist.Checklist, nextID func() int) error {
		*l = []checklist.Checklist{{ID: nextID(), Name: name, Owner: 1, Items: []checklist.Item{{ID: nextID(), Text: "an item"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// checklistName is the name of the database's one checklist.
func checklistName(t *testing.T, f Files) string {
	t.Helper()
	s, err := checklist.Open(f.Checklists)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck
	lists, err := s.Load()
	if err != nil || len(lists) != 1 {
		t.Fatalf("checklists %+v, %v", lists, err)
	}
	return lists[0].Name
}

// contents are the names in the archive at path.
func contents(t *testing.T, path string) []string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close() //nolint:errcheck
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		out = append(out, hdr.Name)
	}
	return out
}

func TestCreateAndRestore(t *testing.T) {
	f := setup(t)
	dir := filepath.Join(t.TempDir(), "backup")
	at := time.Date(2026, 10, 2, 17, 44, 28, 0, time.Local)

	// The database open and in use meanwhile, as a server's would be.
	live, err := checklist.Open(f.Checklists)
	if err != nil {
		t.Fatal(err)
	}
	path, err := Create(dir, f, at)
	live.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "adhd-dash-20261002-174428.tar.gz" {
		t.Errorf("named %s", filepath.Base(path))
	}
	if got := strings.Join(contents(t, path), " "); got != "manifest.json adhd-dash-users.json adhd-dash-checklists.db" {
		t.Errorf("holds %s", got)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the backup directory holds %d files, want the backup alone", len(entries))
	}
	// Another the same second is numbered.
	again, err := Create(dir, f, at)
	if err != nil || filepath.Base(again) != "adhd-dash-20261002-174428-2.tar.gz" {
		t.Errorf("a second backup the same second: %s, %v", again, err)
	}

	// Changed, then restored: the change is undone, and backed up first.
	setChecklist(t, f, "after")
	usersBefore, _ := os.ReadFile(f.Users)
	if err := users.NewStore(f.Users).Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list, users.User{ID: nextID(), Name: "added", Password: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(path, f, dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Users || !r.Checklists || !r.Created.Equal(at) || filepath.Base(r.Saved) != "adhd-dash-20261002-184428.tar.gz" {
		t.Errorf("restored %+v", r)
	}
	if got := checklistName(t, f); got != "before" {
		t.Errorf("checklist %q after restoring; want before", got)
	}
	if data, _ := os.ReadFile(f.Users); string(data) != string(usersBefore) {
		t.Errorf("users file not restored: %s", data)
	}
	if fi, _ := os.Stat(f.Users); fi.Mode().Perm() != 0o600 {
		t.Errorf("users file mode %v after restoring", fi.Mode().Perm())
	}
	// What was replaced can itself be restored.
	if _, err := Restore(r.Saved, f, dir, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := checklistName(t, f); got != "after" {
		t.Errorf("checklist %q after restoring what was replaced; want after", got)
	}
	// Nothing left behind beside the files.
	entries, _ := os.ReadDir(filepath.Dir(f.Users))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Errorf("left %s", e.Name())
		}
	}
}

func TestCreatePartial(t *testing.T) {
	dir := t.TempDir()
	f := Files{Users: filepath.Join(dir, "users.json"), Checklists: filepath.Join(dir, "cl.db")}
	if _, err := Create(filepath.Join(dir, "backup"), f, time.Now()); !errors.Is(err, ErrNothing) {
		t.Errorf("with nothing: %v", err)
	}
	if _, err := os.Stat(f.Checklists); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backing up made a database: %v", err)
	}

	// The users file alone; restored, the database is left as it is.
	if _, _, err := users.NewStore(f.Users).Load(); err != nil {
		t.Fatal(err)
	}
	path, err := Create(filepath.Join(dir, "backup"), f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(contents(t, path), " "); got != "manifest.json adhd-dash-users.json" {
		t.Errorf("holds %s", got)
	}
	setChecklist(t, f, "kept")
	r, err := Restore(path, f, filepath.Join(dir, "backup"), time.Now().Add(time.Second))
	if err != nil || !r.Users || r.Checklists {
		t.Errorf("restored %+v, %v; want the users file alone", r, err)
	}
	if got := checklistName(t, f); got != "kept" {
		t.Errorf("checklist %q; want it kept", got)
	}
}

func TestRestoreRefuses(t *testing.T) {
	f := setup(t)
	dir := filepath.Join(t.TempDir(), "backup")
	path, err := Create(dir, f, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// While a server holds the lock.
	unlock, err := Lock(f.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(path, f, dir, time.Now()); !errors.Is(err, ErrInUse) {
		t.Errorf("restoring while locked: %v", err)
	}
	unlock()

	// A backup that is not one, or holds something unexpected, or bad
	// users: nothing is replaced, nor backed up.
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	write := func(files map[string]string) {
		t.Helper()
		out, err := os.Create(bad)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(out)
		tw := tar.NewWriter(gz)
		for _, name := range []string{"manifest.json", "adhd-dash-users.json", "../evil", "adhd-dash-checklists.db"} {
			if data, ok := files[name]; ok {
				tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}) //nolint:errcheck
				tw.Write([]byte(data))                                                                              //nolint:errcheck
			}
		}
		tw.Close()  //nolint:errcheck
		gz.Close()  //nolint:errcheck
		out.Close() //nolint:errcheck
	}
	setChecklist(t, f, "current")
	usersBefore, _ := os.ReadFile(f.Users)
	before, _ := os.ReadDir(dir)
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"adhd-dash-users.json": "{}"}, "no manifest.json"},
		{map[string]string{"manifest.json": "{}", "../evil": "x"}, "not expected"},
		{map[string]string{"manifest.json": "{}", "adhd-dash-users.json": `{"users": []}`}, "must be an admin"},
		{map[string]string{"manifest.json": "{}", "adhd-dash-checklists.db": "not a database at all, at all, at all, at all, at all"}, "checklist database"},
	} {
		write(c.files)
		if _, err := Restore(bad, f, dir, time.Now()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v; want %q", c.files, err, c.want)
		}
	}
	if _, err := Restore("/nonexistent.tar.gz", f, dir, time.Now()); err == nil {
		t.Errorf("restored from a missing file")
	}
	if got := checklistName(t, f); got != "current" {
		t.Errorf("checklist %q after refused restores", got)
	}
	if data, _ := os.ReadFile(f.Users); string(data) != string(usersBefore) {
		t.Errorf("users file changed by refused restores")
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Errorf("refused restores made backups")
	}
	entries, _ := os.ReadDir(filepath.Dir(f.Users))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-") {
			t.Errorf("left %s", e.Name())
		}
	}
}
