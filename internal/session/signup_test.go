package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// signupRig is a users file with the admin and a new-user account,
// "signup", and the sign-up screen for it.
type signupRig struct {
	t       *testing.T
	store   *users.Store
	account users.User
	s       signupState
	created []string
	rows    []string
	crow    int
}

func newSignupRig(t *testing.T) *signupRig {
	t.Helper()
	r := &signupRig{t: t, store: users.NewStore(filepath.Join(t.TempDir(), "users.json"))}
	if _, _, err := r.store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		r.account = users.User{ID: nextID(), Name: "signup", NewUser: true}
		*list = append(*list, r.account)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.build()
	return r
}

func (r *signupRig) build() {
	s, crow, _ := buildSignup(24, 80, now, r.account.Name, &r.s)
	r.rows, r.crow = screenText(r.t, s, 24, 80), crow
}

func (r *signupRig) key(aid go3270.AID, name, pw, again string) (bool, string) {
	quit, farewell := r.s.handle(go3270.Response{AID: aid, Values: map[string]string{suNameField: name, suPassField: pw, suAgainField: again}},
		r.store, r.account, noLog, func(name string) { r.created = append(r.created, name) })
	r.build()
	return quit, farewell
}

func (r *signupRig) list() []users.User {
	list, _, err := r.store.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func TestSignupScreen(t *testing.T) {
	r := newSignupRig(t)
	text := strings.Join(r.rows, "\n")
	for _, want := range []string{"SIGN UP", "Make your own user (signed in as signup, for signing up)", "User name           ===>",
		"Password, again     ===>", "connect again, and log in as your new user", suPrompt, "PF3=Log off Enter=Make the user"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if r.crow != suFirstRow {
		t.Errorf("cursor on row %d, want the user name", r.crow)
	}
	for _, row := range r.rows {
		if len([]rune(row)) > 80 {
			t.Errorf("row too long: %q", row)
		}
	}
}

func TestSignup(t *testing.T) {
	r := newSignupRig(t)
	for _, c := range []struct {
		name, pw, again, want string
		onPassword            bool
	}{
		{"", "long enough", "long enough", "Type a user name.", false},
		{"waytoolong", "long enough", "long enough", "at most 8 characters", false},
		{".bob", "long enough", "long enough", "starts with '.'", false},
		{"bob", "", "", "Type the password twice.", true},
		{"bob", "long enough", "long enougj", "not typed the same twice", true},
		{"bob", "short", "short", "at least 8 characters", true},
		{"bob", "bobs password", "bobs password", "cannot contain the user name", true},
		{"ADMIN", "long enough", "long enough", `There is already a user called "ADMIN".`, false},
	} {
		quit, _ := r.key(go3270.AIDEnter, c.name, c.pw, c.again)
		if quit || !r.s.isError || !strings.Contains(r.s.message, c.want) || r.s.onPassword != c.onPassword {
			t.Errorf("%q %q %q: quit %v, %q (on password %v); want %q", c.name, c.pw, c.again, quit, r.s.message, r.s.onPassword, c.want)
		}
		if c.onPassword && r.crow != suFirstRow+2 {
			t.Errorf("%q: cursor on row %d, want the password", c.name, r.crow)
		}
	}
	// The name typed is drawn again, the passwords never.
	r.key(go3270.AIDEnter, "bob", "long enough", "different!")
	if !strings.Contains(r.rows[suFirstRow], "bob") || strings.Contains(strings.Join(r.rows, "\n"), "long enough") {
		t.Errorf("redrawn:\n%s", strings.Join(r.rows, "\n"))
	}
	if len(r.list()) != 2 || len(r.created) != 0 {
		t.Fatalf("made a user when refused: %+v", r.list())
	}

	// Made: an ordinary user, and the session ends to log in as it.
	quit, farewell := r.key(go3270.AIDEnter, " bob ", "long enough", "long enough")
	if !quit || farewell != "Made the user bob. Connect again, and log in as bob." || len(r.created) != 1 || r.created[0] != "bob" {
		t.Fatalf("made: quit %v, %q, created %v, message %q", quit, farewell, r.created, r.s.message)
	}
	list := r.list()
	bob := list[len(list)-1]
	if bob.Name != "bob" || bob.Kind() != users.KindUser || bob.Flag || !users.CheckPassword(bob.Password, "long enough") {
		t.Errorf("made %+v", bob)
	}
	if u, ok, _ := r.store.Authenticate(t.Context(), "BOB", "long enough"); !ok || u.ID != bob.ID {
		t.Errorf("cannot log in as the user made")
	}

	// An account no longer a new-user account signs no one up.
	if err := r.store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[1].NewUser = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.s = signupState{}
	if quit, farewell := r.key(go3270.AIDEnter, "eve", "long enough", "long enough"); !quit || !strings.Contains(farewell, "can no longer sign users up") || len(r.list()) != 3 {
		t.Errorf("no longer a new-user account: quit %v, %q, %d users", quit, farewell, len(r.list()))
	}

	// PF3 logs off; other keys do nothing.
	if quit, farewell := r.key(go3270.AIDPF3, "", "", ""); !quit || farewell != "Logged off. Goodbye." {
		t.Errorf("PF3: %v, %q", quit, farewell)
	}
	if quit, _ := r.key(go3270.AIDPF8, "x", "", ""); quit || r.s.message != "" {
		t.Errorf("PF8: %v, %q", quit, r.s.message)
	}
}
