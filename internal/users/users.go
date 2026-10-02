// Package users keeps the user database in a JSON file: each user's name,
// whether they are an admin, their password, hashed with Argon2id, and the
// Google calendar they have connected, if any. At least one user is always
// an admin, and exactly one is the console's: the user the console is
// logged in as. The file also holds the Google OAuth client every user's
// calendar is connected through, set by an admin.
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
	"unicode/utf8"

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

	// Console is set for the one user the console is logged in as, with no
	// login screen.
	Console bool `json:"console,omitempty"`

	// Flag is set for a user who controls the busy light: their calendar's
	// meetings light it, and they can set it by hand.
	Flag bool `json:"flag,omitempty"`

	Password string `json:"password"` // Argon2id, in PHC string format

	// Google is the user's connected Google calendar; nil for none.
	Google *GoogleLink `json:"google,omitempty"`

	// Trello is the user's linked Trello account; nil for none.
	Trello *TrelloLink `json:"trello,omitempty"`
}

// TrelloClient is the Trello API key users link their Trello accounts
// through.
type TrelloClient struct {
	APIKey string `json:"api_key"`
}

// TrelloLink is a user's token for their Trello account, and the lists
// whose cards are their tasks.
type TrelloLink struct {
	// APIKey is the key the token was granted to; it is no use with any
	// other.
	APIKey   string `json:"api_key"`
	Token    string `json:"token"`
	Username string `json:"username,omitempty"` // their Trello user name, to show

	Lists []TrelloList `json:"lists"`
}

// TrelloList is a Trello list a user chose, and the tag its tasks are
// shown with. The IDs find it; the names are as they were when chosen.
type TrelloList struct {
	BoardID string `json:"board_id"`
	Board   string `json:"board"`
	ListID  string `json:"list_id"`
	List    string `json:"list"`
	Tag     string `json:"tag,omitempty"`
}

// TrelloLinked reports whether u's Trello account can be read through
// client: they have a token, granted to its key.
func (u User) TrelloLinked(client *TrelloClient) bool {
	return client != nil && u.Trello != nil && u.Trello.Token != "" && u.Trello.APIKey == client.APIKey
}

// GoogleClient is the OAuth client users connect their calendars through.
type GoogleClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// Site is what the web site's pages say about where they are and whose the
// service is.
type Site struct {
	// BaseURL is the address the pages are reached at from outside, such
	// as https://adhd.example.com/, ending in a slash.
	BaseURL string `json:"base_url"`

	// Organization runs the service; Contact is an email address for
	// questions about the privacy policy and terms.
	Organization string `json:"organization,omitempty"`
	Contact      string `json:"contact,omitempty"`
}

// GoogleLink is a user's authorization to read their Google calendars, and
// which of them to show.
type GoogleLink struct {
	// ClientID is the client the token was issued to; it is no use with
	// any other.
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`

	Calendars []Calendar `json:"calendars"`
}

// Calendar is one Google calendar a user has chosen to show.
type Calendar struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // as Google gave it, for the user to know it by

	// Alias, if set, is shown in brackets before the calendar's events.
	Alias string `json:"alias,omitempty"`
}

// Connected reports whether u's calendar can be read through client: they
// have a token, issued to that client.
func (u User) Connected(client *GoogleClient) bool {
	return client != nil && u.Google != nil && u.Google.RefreshToken != "" && u.Google.ClientID == client.ClientID
}

// The first user, made when there is no file yet.
const (
	FirstName     = "admin"
	FirstPassword = "admin"
)

// ErrNoAdmin reports a change that would leave no admin.
var ErrNoAdmin = errors.New("at least one user must be an admin")

// ErrNoConsole reports a change that would leave no user for the console.
var ErrNoConsole = errors.New("one user must be the console's")

// file is the JSON file's contents.
type file struct {
	Users []User `json:"users"`

	// GoogleClient is the OAuth client for Google calendars; nil for none.
	GoogleClient *GoogleClient `json:"google_client,omitempty"`

	// TrelloClient is the Trello API key for Trello accounts; nil for none.
	TrelloClient *TrelloClient `json:"trello_client,omitempty"`

	// Site describes the web site, set by an admin; nil until then.
	Site *Site `json:"site,omitempty"`

	// LastID is the highest ID ever given, so that a removed user's is not
	// given again: their checklists, kept by user ID, would go with it.
	LastID int `json:"last_id,omitempty"`
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
// adhd-dash-users.json in the home directory.
func DefaultPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-users.json")
	}
	return "adhd-dash-users.json"
}

// Path is the file the Store keeps.
func (s *Store) Path() string { return s.path }

// Load returns every user, in the order they were added. With no file yet,
// it makes one holding the first user, an admin called FirstName with the
// password FirstPassword, reporting created.
func (s *Store) Load() (list []User, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if !errors.Is(err, fs.ErrNotExist) {
		return f.Users, false, err
	}
	hash, err := hashPassword(FirstPassword) // at startup, with nothing to wait for
	if err != nil {
		return nil, false, err
	}
	list = []User{{ID: 1, Name: FirstName, Admin: true, Console: true, Password: hash}}
	if err := s.write(file{Users: list}); err != nil {
		return nil, false, err
	}
	return list, true, nil
}

// GoogleClient returns the users and the Google OAuth client, nil if none
// is set, as one read of the file.
func (s *Store) GoogleClient() ([]User, *GoogleClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	return f.Users, f.GoogleClient, err
}

// ErrNoUser reports a change to a user who is no longer there.
var ErrNoUser = errors.New("that user no longer exists")

// ConnectGoogle stores the Google refresh token issued through clientID to
// the user with id, keeping any calendars they chose before: connecting
// again is usually only for a new authorization.
func (s *Store) ConnectGoogle(id int, clientID, refreshToken string) error {
	return s.Update(func(list *[]User, _ func() int) error {
		for i := range *list {
			u := &(*list)[i]
			if u.ID != id {
				continue
			}
			var keep []Calendar
			if u.Google != nil {
				keep = u.Google.Calendars
			}
			u.Google = &GoogleLink{ClientID: clientID, RefreshToken: refreshToken, Calendars: keep}
			return nil
		}
		return ErrNoUser
	})
}

// TrelloClient returns the users and the Trello API key, nil if none is
// set, as one read of the file.
func (s *Store) TrelloClient() ([]User, *TrelloClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	return f.Users, f.TrelloClient, err
}

// SetTrelloClient sets the Trello API key, or with nil removes it.
func (s *Store) SetTrelloClient(client *TrelloClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.TrelloClient = client
	return s.write(f)
}

// ConnectTrello stores the Trello token granted to apiKey for the user with
// id, whose Trello user name is username, keeping any lists they chose
// before.
func (s *Store) ConnectTrello(id int, apiKey, token, username string) error {
	return s.Update(func(list *[]User, _ func() int) error {
		for i := range *list {
			u := &(*list)[i]
			if u.ID != id {
				continue
			}
			var keep []TrelloList
			if u.Trello != nil {
				keep = u.Trello.Lists
			}
			u.Trello = &TrelloLink{APIKey: apiKey, Token: token, Username: username, Lists: keep}
			return nil
		}
		return ErrNoUser
	})
}

// Site returns the web site's settings, nil if none are set.
func (s *Store) Site() (*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f.Site, err
}

// SetSite sets the web site's settings, or with nil removes them.
func (s *Store) SetSite(site *Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.Site = site
	return s.write(f)
}

// SetGoogleClient sets the Google OAuth client, or with nil removes it.
func (s *Store) SetGoogleClient(client *GoogleClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.GoogleClient = client
	return s.write(f)
}

// Update reads the users, passes them to change, and writes back what it
// leaves, unless it returns an error or the users it leaves are not valid:
// names must be given and unique (ignoring case), at least one user an admin
// (else ErrNoAdmin), and exactly one the console's (else ErrNoConsole, for
// none). NextID gives change IDs for users it adds.
func (s *Store) Update(change func(list *[]User, nextID func() int) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	last := f.LastID
	for _, u := range f.Users {
		last = max(last, u.ID)
	}
	nextID := func() int {
		last++
		return last
	}
	if err := change(&f.Users, nextID); err != nil {
		return err
	}
	f.LastID = last
	if err := Validate(f.Users); err != nil {
		return err
	}
	return s.write(f)
}

// Validate reports why list is not a valid set of users: a name empty,
// holding a space, longer than MaxNameLength, or used twice (ignoring
// case), a user both an admin and
// restricted, or both restricted and controlling the busy light, no admin,
// or not exactly one user the console's.
func Validate(list []User) error {
	seen := map[string]bool{}
	admins, consoles := 0, 0
	for _, u := range list {
		switch key := strings.ToLower(u.Name); {
		case u.Name == "":
			return errors.New("a user must have a name")
		case strings.ContainsAny(u.Name, " \t"):
			return fmt.Errorf("user name %q has a space in it", u.Name)
		case utf8.RuneCountInString(u.Name) > MaxNameLength:
			return fmt.Errorf("user name %q is longer than %d characters", u.Name, MaxNameLength)
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
		if u.Flag && u.Restricted {
			return fmt.Errorf("%s cannot both control the busy light and be restricted", u.Name)
		}
		if u.Admin {
			admins++
		}
		if u.Console {
			consoles++
		}
	}
	switch {
	case admins == 0:
		return ErrNoAdmin
	case consoles == 0:
		return ErrNoConsole
	case consoles > 1:
		return errors.New("only one user can be the console's")
	}
	return nil
}

// ConsoleUser returns the user the console is logged in as.
func ConsoleUser(list []User) (User, bool) {
	for _, u := range list {
		if u.Console {
			return u, true
		}
	}
	return User{}, false
}

// Admin returns the admin user: the one called FirstName if they are an
// admin, else the console's user if they are, else the first admin.
func Admin(list []User) (User, bool) {
	for _, u := range list {
		if u.Admin && strings.EqualFold(u.Name, FirstName) {
			return u, true
		}
	}
	if u, ok := ConsoleUser(list); ok && u.Admin {
		return u, true
	}
	for _, u := range list {
		if u.Admin {
			return u, true
		}
	}
	return User{}, false
}

// defaultConsole marks the console's user in a list that has none, as a
// file from before there was one has: the first user, FirstName, if they
// are still there and an admin, else the first admin.
func defaultConsole(list []User) {
	if _, ok := ConsoleUser(list); ok {
		return
	}
	pick := -1
	for i, u := range list {
		switch {
		case u.Admin && strings.EqualFold(u.Name, FirstName):
			list[i].Console = true
			return
		case u.Admin && pick < 0:
			pick = i
		}
	}
	if pick >= 0 {
		list[pick].Console = true
	}
}

// MaxNameLength is the most characters a user name may have.
const MaxNameLength = 8

// MinPasswordLength is the fewest characters a password may have.
const MinPasswordLength = 8

// CheckNewPassword reports why password will not do for the user called
// name, or nil if it will: it must have at least MinPasswordLength
// characters, and must not contain the user name, in any case.
func CheckNewPassword(name, password string) error {
	switch {
	case utf8.RuneCountInString(password) < MinPasswordLength:
		return fmt.Errorf("a password must have at least %d characters", MinPasswordLength)
	case name != "" && strings.Contains(strings.ToLower(password), strings.ToLower(name)):
		return errors.New("a password cannot contain the user name")
	}
	return nil
}

// IsDefaultLogin reports whether name and password are the first user's,
// as made with a new users file: FirstName, with FirstPassword. They work
// only on the console, which needs no password, so that the admin sets a
// real one there before anyone can sign in as them from anywhere.
func IsDefaultLogin(name, password string) bool {
	return strings.EqualFold(name, FirstName) && password == FirstPassword
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

// read returns the file's contents; a missing file is fs.ErrNotExist.
func (s *Store) read() (file, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return file{}, err
		}
		return file{}, fmt.Errorf("reading users: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}, fmt.Errorf("reading users from %s: %w", s.path, err)
	}
	defaultConsole(f.Users)
	return f, nil
}

// write replaces the file by renaming a new one over it, so that a reader
// never sees it half written and a failure leaves the old one. The file is
// readable only by its owner, as it holds password hashes and Google
// credentials.
func (s *Store) write(f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
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
