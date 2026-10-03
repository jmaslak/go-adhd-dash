// Package backup saves the server's configuration and data, the users file
// and the checklist database, in one archive, and puts them back from one.
//
// An archive is a gzipped tar file named for when it was made,
// adhd-dash-YYYYMMDD-HHMMSS.tar.gz, holding manifest.json, saying what it
// holds, then adhd-dash-users.json and adhd-dash-checklists.db, each if
// there was one. The users file holds password hashes and Google and
// Trello tokens, so archives, and their directory, are readable only by
// their owner.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// Files are where the server keeps what is backed up.
type Files struct {
	Users      string // the users file
	Checklists string // the checklist database
}

// LockPath is the file a server holds locked while it uses f, so that f is
// not restored under it: the checklist database's name with .lock added.
func (f Files) LockPath() string { return f.Checklists + ".lock" }

// The names inside an archive.
const (
	manifestName   = "manifest.json"
	usersName      = "adhd-dash-users.json"
	checklistsName = "adhd-dash-checklists.db"
)

// Archive names: the prefix, when it was made, and the suffix.
const (
	namePrefix = "adhd-dash-"
	nameSuffix = ".tar.gz"
	timeLayout = "20060102-150405"
)

// ErrNothing reports that there was nothing to back up.
var ErrNothing = errors.New("nothing to back up: neither the users file nor the checklist database exists")

// manifest says when an archive was made, from where, and what it holds.
type manifest struct {
	Created    time.Time `json:"created"`
	Users      string    `json:"users,omitempty"`      // the users file it is from
	Checklists string    `json:"checklists,omitempty"` // the database it is from
}

// DefaultDir is where backups are kept unless told otherwise: backup in
// the home directory.
func DefaultDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "backup")
	}
	return "backup"
}

// Create backs f up into a new archive in dir, made if need be, named for
// now, and returns its path. The database is copied as it is at one
// moment, so a server may go on using it meanwhile.
func Create(dir string, f Files, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("making the backup directory: %w", err)
	}
	staging, err := os.MkdirTemp(dir, ".staging-*")
	if err != nil {
		return "", fmt.Errorf("backing up: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck // only scratch

	m := manifest{Created: now}
	var entries [][2]string // name in the archive, file holding it
	switch _, err := os.Stat(f.Users); {
	case err == nil:
		m.Users = f.Users
		entries = append(entries, [2]string{usersName, f.Users})
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("backing up users: %w", err)
	}
	switch _, err := os.Stat(f.Checklists); {
	case err == nil:
		snapshot := filepath.Join(staging, checklistsName)
		if err := snapshotChecklists(f.Checklists, snapshot); err != nil {
			return "", err
		}
		m.Checklists = f.Checklists
		entries = append(entries, [2]string{checklistsName, snapshot})
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("backing up checklists: %w", err)
	}
	if len(entries) == 0 {
		return "", ErrNothing
	}

	tmp := filepath.Join(staging, "archive")
	if err := writeArchive(tmp, m, entries); err != nil {
		return "", err
	}
	// Linked to its name, never over another archive: made the same
	// second as another, it is numbered.
	base := namePrefix + now.Format(timeLayout)
	for n := 1; ; n++ {
		path := filepath.Join(dir, base+nameSuffix)
		if n > 1 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, n, nameSuffix))
		}
		err := os.Link(tmp, path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) || n >= 100 {
			return "", fmt.Errorf("backing up: %w", err)
		}
	}
}

// snapshotChecklists copies the database at path, as it is at one moment,
// to to.
func snapshotChecklists(path, to string) error {
	s, err := checklist.Open(path)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // only read
	return s.BackupTo(to)
}

// writeArchive writes the archive at path: m, then each entry.
func writeArchive(path string, m manifest, entries [][2]string) (err error) {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backing up: %w", err)
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("backing up: %w", cerr)
		}
	}()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	manifestJSON, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("backing up: %w", err)
	}
	if err := addBytes(tw, manifestName, append(manifestJSON, '\n'), m.Created); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(e[1])
		if err != nil {
			return fmt.Errorf("backing up %s: %w", e[1], err)
		}
		if err := addBytes(tw, e[0], data, m.Created); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("backing up: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("backing up: %w", err)
	}
	return out.Sync()
}

func addBytes(tw *tar.Writer, name string, data []byte, at time.Time) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: at, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backing up %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("backing up %s: %w", name, err)
	}
	return nil
}

// Restored says what Restore did.
type Restored struct {
	Created    time.Time // when the archive was made
	Users      bool      // whether the users file was restored
	Checklists bool      // whether the checklist database was restored
	Saved      string    // the backup made of what was replaced; "" if there was nothing
}

// ErrInUse reports that a server is using the files to be restored.
var ErrInUse = errors.New("a server is using these files; stop it first")

// Restore puts back what the archive at path holds, over f, refusing
// while a server holds f's lock. Everything in the archive is checked
// before anything is replaced: the users file must be valid, and the
// database sound. What is replaced is first backed up, into dir, as at
// now. What the archive does not hold is left as it is.
func Restore(path string, f Files, dir string, now time.Time) (Restored, error) {
	unlock, err := Lock(f.LockPath())
	if err != nil {
		return Restored{}, err
	}
	defer unlock()

	staged, m, err := extract(path, f)
	for _, tmp := range staged {
		defer os.Remove(tmp) //nolint:errcheck // gone once renamed
	}
	if err != nil {
		return Restored{}, err
	}
	r := Restored{Created: m.Created}
	if tmp, ok := staged[usersName]; ok {
		if err := checkUsers(tmp); err != nil {
			return r, fmt.Errorf("the users file in %s: %w", path, err)
		}
	}
	if tmp, ok := staged[checklistsName]; ok {
		if err := checkChecklists(tmp); err != nil {
			return r, fmt.Errorf("the checklist database in %s: %w", path, err)
		}
	}

	// Backing up the database replays any of its journal left by a server
	// that stopped without closing it, so the journal can then go.
	switch saved, err := Create(dir, f, now); {
	case err == nil:
		r.Saved = saved
	case !errors.Is(err, ErrNothing):
		return r, fmt.Errorf("backing up what would be replaced: %w", err)
	}
	if tmp, ok := staged[checklistsName]; ok {
		for _, journal := range []string{f.Checklists + "-wal", f.Checklists + "-shm"} {
			if err := os.Remove(journal); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return r, fmt.Errorf("restoring checklists: %w", err)
			}
		}
		if err := os.Rename(tmp, f.Checklists); err != nil {
			return r, fmt.Errorf("restoring checklists: %w", err)
		}
		r.Checklists = true
	}
	if tmp, ok := staged[usersName]; ok {
		if err := os.Rename(tmp, f.Users); err != nil {
			return r, fmt.Errorf("restoring users: %w", err)
		}
		r.Users = true
	}
	return r, nil
}

// extract writes each file in the archive at path to a new file beside
// where f keeps it, returning them by their names in the archive, and the
// archive's manifest. Anything else in the archive is refused.
func extract(path string, f Files) (staged map[string]string, m manifest, err error) {
	staged = map[string]string{}
	in, err := os.Open(path)
	if err != nil {
		return staged, m, fmt.Errorf("reading the backup: %w", err)
	}
	defer in.Close() //nolint:errcheck // only read
	gz, err := gzip.NewReader(in)
	if err != nil {
		return staged, m, fmt.Errorf("reading the backup %s: %w", path, err)
	}
	tr := tar.NewReader(gz)
	haveManifest := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return staged, m, fmt.Errorf("reading the backup %s: %w", path, err)
		}
		dest := map[string]string{usersName: f.Users, checklistsName: f.Checklists}[hdr.Name]
		switch {
		case hdr.Typeflag != tar.TypeReg:
			return staged, m, fmt.Errorf("the backup %s holds %s, which is not a file", path, hdr.Name)
		case hdr.Name == manifestName:
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return staged, m, fmt.Errorf("reading the backup %s's manifest: %w", path, err)
			}
			haveManifest = true
		case dest == "" || staged[hdr.Name] != "":
			return staged, m, fmt.Errorf("the backup %s holds %s, which is not expected", path, hdr.Name)
		default:
			tmp, err := os.CreateTemp(filepath.Dir(dest), ".restore-*")
			if err != nil {
				return staged, m, fmt.Errorf("restoring %s: %w", hdr.Name, err)
			}
			staged[hdr.Name] = tmp.Name()
			_, err = io.Copy(tmp, tr)
			err = errors.Join(err, tmp.Chmod(0o600), tmp.Sync(), tmp.Close())
			if err != nil {
				return staged, m, fmt.Errorf("restoring %s: %w", hdr.Name, err)
			}
		}
	}
	if !haveManifest {
		return staged, m, fmt.Errorf("%s is not a backup: it has no %s", path, manifestName)
	}
	return staged, m, nil
}

// checkUsers reports whether the users file at path is valid.
func checkUsers(path string) error {
	list, _, err := users.NewStore(path).Load()
	if err != nil {
		return err
	}
	return users.Validate(list)
}

// checkChecklists reports whether the database at path is sound, leaving
// it closed, with no journal beside it.
func checkChecklists(path string) error {
	s, err := checklist.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(s.Check(), s.Close())
}
