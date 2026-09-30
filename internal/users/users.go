// Package users keeps the user database in a JSON file: each user's name,
// whether they are an admin, and their password, hashed with Argon2id. At
// least one user is always an admin.
//
// Users carry IDs, unique across the file and never reused while it lasts,
// so that a change made on a screen drawn before another session changed
// the file still lands on the right user.
package users

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// User is one user.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`

	// Restricted users can use the calculator and nothing else. No admin
	// is restricted.
	Restricted bool `json:"restricted,omitempty"`

	Password string `json:"password"` // Argon2id, in PHC string format
}

// The first user, made when there is no file yet.
const (
	FirstName     = "admin"
	FirstPassword = "admin"
)

// ErrNoAdmin reports a change that would leave no admin.
var ErrNoAdmin = errors.New("at least one user must be an admin")

// file is the JSON file's contents.
type file struct {
	Users []User `json:"users"`
}

// Store reads and writes the user file. One Store is shared by every
// session, so that changes do not interleave.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store for the file at path, which need not exist yet.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultPath is where the users are kept unless told otherwise:
// .adhd-dash-users.json in the home directory.
func DefaultPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".adhd-dash-users.json")
	}
	return ".adhd-dash-users.json"
}

// Path is the file the Store keeps.
func (s *Store) Path() string { return s.path }

// Load returns every user, in the order they were added. With no file yet,
// it makes one holding the first user, an admin called FirstName with the
// password FirstPassword, reporting created.
func (s *Store) Load() (list []User, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err = s.read()
	if !errors.Is(err, fs.ErrNotExist) {
		return list, false, err
	}
	hash, err := hashPassword(FirstPassword) // at startup, with nothing to wait for
	if err != nil {
		return nil, false, err
	}
	list = []User{{ID: 1, Name: FirstName, Admin: true, Password: hash}}
	if err := s.write(list); err != nil {
		return nil, false, err
	}
	return list, true, nil
}

// Update reads the users, passes them to change, and writes back what it
// leaves, unless it returns an error or the users it leaves are not valid:
// names must be given and unique (ignoring case), and at least one user an
// admin (else ErrNoAdmin). NextID gives change IDs for users it adds.
func (s *Store) Update(change func(list *[]User, nextID func() int) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.read()
	if err != nil {
		return err
	}
	last := 0
	for _, u := range list {
		last = max(last, u.ID)
	}
	nextID := func() int {
		last++
		return last
	}
	if err := change(&list, nextID); err != nil {
		return err
	}
	if err := Validate(list); err != nil {
		return err
	}
	return s.write(list)
}

// Validate reports why list is not a valid set of users: a name empty,
// holding a space, or used twice (ignoring case), a user both an admin and
// restricted, or no admin.
func Validate(list []User) error {
	seen := map[string]bool{}
	admins := 0
	for _, u := range list {
		switch key := strings.ToLower(u.Name); {
		case u.Name == "":
			return errors.New("a user must have a name")
		case strings.ContainsAny(u.Name, " \t"):
			return fmt.Errorf("user name %q has a space in it", u.Name)
		case strings.ContainsFunc(u.Name, unicode.IsControl):
			return fmt.Errorf("user name %q has a control character in it", u.Name)
		case seen[key]:
			return fmt.Errorf("there is already a user called %q", u.Name)
		default:
			seen[key] = true
		}
		if u.Admin && u.Restricted {
			return fmt.Errorf("%s cannot be both an admin and restricted", u.Name)
		}
		if u.Admin {
			admins++
		}
	}
	if admins == 0 {
		return ErrNoAdmin
	}
	return nil
}

// Authenticate returns the user called name (ignoring case) if password is
// theirs. The check takes one of the MaxConcurrentHashes slots, waiting for
// it until ctx is done, and then fails with ErrBusy.
func (s *Store) Authenticate(ctx context.Context, name, password string) (User, bool, error) {
	list, _, err := s.Load()
	if err != nil {
		return User{}, false, err
	}
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return User{}, false, err
	}
	defer release()
	for _, u := range list {
		if strings.EqualFold(u.Name, name) {
			return u, CheckPassword(u.Password, password), nil
		}
	}
	// No such user: hash anyway, so that the time taken does not tell.
	CheckPassword(dummyHash, password)
	return User{}, false, nil
}

// dummyHash is checked against for a user who does not exist.
var dummyHash, _ = hashPassword("no such user")

// MaxConcurrentHashes is how many Argon2id hashes (password checks and new
// passwords) may be worked out at once. Each takes 64 MiB, on purpose, so
// without a limit anyone able to reach the login screen could exhaust the
// server's memory by trying many logins at once.
const MaxConcurrentHashes = 4

// hashSlots holds a token for each hash being worked out.
var hashSlots = make(chan struct{}, MaxConcurrentHashes)

// ErrBusy reports that no hash slot came free in time.
var ErrBusy = errors.New("the server is busy checking other passwords; try again in a moment")

// acquireHashSlot waits for a hash slot until ctx is done, returning the
// function that gives it back.
func acquireHashSlot(ctx context.Context) (release func(), err error) {
	select {
	case hashSlots <- struct{}{}:
		return func() { <-hashSlots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w (%v)", ErrBusy, ctx.Err())
	}
}

// read returns the users in the file; a missing file is fs.ErrNotExist.
func (s *Store) read() ([]User, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("reading users: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reading users from %s: %w", s.path, err)
	}
	return f.Users, nil
}

// write replaces the file by renaming a new one over it, so that a reader
// never sees it half written and a failure leaves the old one. The file is
// readable only by its owner, as it holds password hashes.
func (s *Store) write(list []User) error {
	data, err := json.MarshalIndent(file{Users: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("writing users: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".users-*.tmp")
	if err != nil {
		return fmt.Errorf("writing users: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing users: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing users: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing users: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("writing users: %w", err)
	}
	return nil
}

// argon2Params are the Argon2id parameters new hashes are made with: RFC
// 9106's second recommended option (64 MiB, 3 passes, 4 lanes), a 16-byte
// salt and a 32-byte key. Each hash records its own, so these can be raised
// without breaking those already stored.
var argon2Params = struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	saltLen int
	keyLen  uint32
}{64 * 1024, 3, 4, 16, 32}

// HashPassword hashes password with Argon2id and a random salt, in PHC
// string format: $argon2id$v=19$m=65536,t=3,p=4$salt$key. It takes one of
// the MaxConcurrentHashes slots, waiting for it until ctx is done, and then
// fails with ErrBusy.
func HashPassword(ctx context.Context, password string) (string, error) {
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return hashPassword(password)
}

// hashPassword is HashPassword without waiting for a slot.
func hashPassword(password string) (string, error) {
	p := argon2Params
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("making a salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches hash, made by
// HashPassword, with whatever parameters it records. A hash it cannot read
// matches nothing. It does not take a hash slot; Authenticate does.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil || threads == 0 || time == 0 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
