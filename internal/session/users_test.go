package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// usersRig drives the user editor against a users file of its own.
type usersRig struct {
	t     *testing.T
	store *users.Store
	u     usersState
	s     go3270.Screen
	rows  []string
	crow  int
	ccol  int
}

func newUsersRig(t *testing.T) *usersRig {
	t.Helper()
	r := &usersRig{t: t, store: users.NewStore(filepath.Join(t.TempDir(), "users.json"))}
	r.draw()
	return r
}

func (r *usersRig) draw() {
	r.t.Helper()
	list, _, err := r.store.Load()
	r.s, r.crow, r.ccol = buildUsers(24, 80, now, list, err, &r.u)
	r.rows = screenText(r.t, r.s, 24, 80)
}

func (r *usersRig) key(aid go3270.AID, values map[string]string) bool {
	r.t.Helper()
	leave := r.u.handle(go3270.Response{AID: aid, Values: values}, r.store, noLog)
	r.draw()
	return leave
}

func (r *usersRig) list() []users.User {
	r.t.Helper()
	list, _, err := r.store.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return list
}

func TestUsersScreen(t *testing.T) {
	r := newUsersRig(t)
	for i, want := range map[int]string{
		usHeaderRow: " USERS 1 user, 1 admin",
		usColumnRow: " S Name                      Type         Flag",
		usFirstRow:  "   admin                     admin        N",
		20:          " New user ===>           Type ===>",
		22:          " " + usPrompt, // all of it, on 80 columns
		21:          " Password ===>",
	} {
		if r.rows[i] != want {
			t.Errorf("row %d is %q, want %q", i, r.rows[i], want)
		}
	}
	if r.crow != usFirstRow || r.ccol != 1 {
		t.Errorf("cursor at %d,%d", r.crow, r.ccol)
	}
	for _, f := range r.s {
		switch {
		case f.Name == usNewPassword && !f.Hidden:
			t.Error("password field shows what is typed")
		case f.Row == usFirstRow && (f.Col == usNameCol || f.Col == usTypeEndCol || f.Col == usFlagEndCol) && !f.Autoskip:
			t.Errorf("field at col %d does not skip on", f.Col)
		}
	}
}

func TestUsersAddChangeRemove(t *testing.T) {
	r := newUsersRig(t)

	// Adding needs a name and a password.
	r.key(go3270.AIDEnter, map[string]string{usNewName: "joelle"})
	if !r.u.isError || r.u.message != "Type the new user's password." || len(r.list()) != 1 || r.u.typed[usNewName] != "joelle" {
		t.Errorf("no password: message %q, typed %v", r.u.message, r.u.typed)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewPassword: "pw"})
	if r.u.message != "Type the new user's name." || r.u.typed[usNewPassword] != "" {
		t.Errorf("no name: message %q, typed %v (a password must not be kept)", r.u.message, r.u.typed)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "joelle", usNewPassword: "pw", usNewType: "boss"})
	if !strings.Contains(r.u.message, `the new user's Type, not "boss"`) {
		t.Errorf("bad type: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: " joelle ", usNewPassword: "pass word one"})
	list := r.list()
	if r.u.isError || r.u.message != "Added joelle." || len(list) != 2 || list[1].Name != "joelle" || list[1].Admin || !users.CheckPassword(list[1].Password, "pass word one") {
		t.Fatalf("add: message %q, users %+v", r.u.message, list)
	}
	if r.crow != 20 {
		t.Errorf("cursor after adding on row %d, want the new user row", r.crow)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "JOELLE", usNewPassword: "long enough pw"})
	if !strings.Contains(r.u.message, "already a user") {
		t.Errorf("duplicate: %q", r.u.message)
	}

	// The only admin cannot lose it, nor be removed.
	r.key(go3270.AIDEnter, map[string]string{"utype:1": "user"})
	if r.u.message != "At least one user must be an admin: that would leave none." || !r.list()[0].Admin || r.u.typed["utype:1"] != "user" {
		t.Errorf("unadmin last admin: message %q, typed %v", r.u.message, r.u.typed)
	}
	r.key(go3270.AIDEnter, map[string]string{"usel:1": "d"})
	if !strings.Contains(r.u.message, "At least one user") || len(r.list()) != 2 || r.u.pending != nil {
		t.Errorf("remove last admin: %q, pending %v; want refused before asking", r.u.message, r.u.pending)
	}
	r.key(go3270.AIDEnter, map[string]string{"usel:1": "z"})
	if !strings.Contains(r.u.message, `not "Z"`) {
		t.Errorf("bad command: %q", r.u.message)
	}

	// Handing over admin and removing the old one, in one go: asked
	// first, with nothing saved until PF4.
	r.key(go3270.AIDEnter, map[string]string{"utype:2": "admin", "usel:1": "D"})
	text := strings.Join(r.rows, "\n")
	for _, want := range []string{"DELETE USERS", "Delete this user?", "admin (admin)", "other changes typed with it are saved with it", "PF4=Delete"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	if list := r.list(); len(list) != 2 || list[1].Admin {
		t.Fatalf("saved before confirming: %+v", list)
	}
	r.key(go3270.AIDEnter, nil) // any other key leaves it up
	if r.u.pending == nil {
		t.Fatal("Enter dismissed the confirmation")
	}
	r.key(go3270.AIDPF3, nil)
	if r.u.pending != nil || r.u.message != "Nothing was saved." || len(r.list()) != 2 || r.u.typed["usel:1"] != "D" || r.u.typed["utype:2"] != "admin" {
		t.Fatalf("PF3: message %q, typed %v", r.u.message, r.u.typed)
	}
	if !strings.Contains(strings.Join(r.rows, "\n"), "joelle                    admin") {
		t.Errorf("typed not drawn again after PF3:\n%s", strings.Join(r.rows, "\n"))
	}
	r.key(go3270.AIDEnter, map[string]string{"utype:2": "admin", "usel:1": "D"})
	r.key(go3270.AIDPF4, nil)
	list = r.list()
	if r.u.isError || len(list) != 1 || list[0].Name != "joelle" || !list[0].Admin {
		t.Fatalf("hand over: message %q, users %+v", r.u.message, list)
	}
	if r.u.message != "Removed 1 user. Changed 1 user type." {
		t.Errorf("hand over message %q", r.u.message)
	}

	// Changing a password: P, then the password.
	r.key(go3270.AIDEnter, map[string]string{"usel:2": "P"})
	if r.u.passwordFor != 2 || !strings.Contains(r.rows[20], "Change the password of joelle") || r.crow != 21 {
		t.Fatalf("P: for %d, row %q, cursor row %d", r.u.passwordFor, r.rows[20], r.crow)
	}
	r.key(go3270.AIDEnter, nil)
	if r.u.message != "Type the new password, or press PF3 to cancel." {
		t.Errorf("empty password: %q", r.u.message)
	}
	r.key(go3270.AIDPF8, nil) // paging is fine without one
	if r.u.isError || r.u.passwordFor != 2 {
		t.Errorf("PF8 while changing: message %q, for %d", r.u.message, r.u.passwordFor)
	}
	r.key(go3270.AIDEnter, map[string]string{"upw:2": "new password 2"})
	if r.u.message != "Changed the password of joelle." || r.u.passwordFor != 0 || !users.CheckPassword(r.list()[0].Password, "new password 2") {
		t.Errorf("change: message %q, for %d", r.u.message, r.u.passwordFor)
	}

	// PF3 cancels a password change, then leaves.
	r.key(go3270.AIDEnter, map[string]string{"usel:2": "p"})
	if leave := r.key(go3270.AIDPF3, nil); leave || r.u.passwordFor != 0 {
		t.Errorf("PF3 while changing: leave %v, for %d", leave, r.u.passwordFor)
	}
	if !r.key(go3270.AIDPF3, nil) {
		t.Error("PF3 does not leave")
	}
}

func TestUsersPaging(t *testing.T) {
	r := newUsersRig(t)
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		for i := range 20 {
			*list = append(*list, users.User{ID: nextID(), Name: fmt.Sprint("user", i), Password: "x"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.draw()
	if !strings.Contains(r.rows[usHeaderRow], "21 users, page 1/2") {
		t.Errorf("header %q", r.rows[usHeaderRow])
	}
	r.key(go3270.AIDPF8, nil)
	if r.u.page != 1 || !strings.Contains(r.rows[usFirstRow], "user14") {
		t.Errorf("page 2: page %d, first row %q", r.u.page, r.rows[usFirstRow])
	}
	r.key(go3270.AIDPF8, nil)
	r.key(go3270.AIDPF7, nil)
	if r.u.page != 0 || !strings.Contains(r.rows[usFirstRow], "admin") {
		t.Errorf("back to page 1: page %d, first row %q", r.u.page, r.rows[usFirstRow])
	}
}

func TestDeleteUsersConfirm(t *testing.T) {
	p := &usersPending{deleting: []users.User{{ID: 2, Name: "a"}, {ID: 3, Name: "b"}}}
	text := strings.Join(screenText(t, buildDeleteUsersConfirm(24, 80, now, p), 24, 80), "\n")
	if !strings.Contains(text, "Delete these 2 users?") || strings.Contains(text, "other changes") || strings.Contains(text, "(admin)") {
		t.Errorf("two users, nothing else:\n%s", text)
	}
}

func TestUsersRestricted(t *testing.T) {
	r := newUsersRig(t)
	r.key(go3270.AIDEnter, map[string]string{usNewName: "calc", usNewPassword: "long enough pw", usNewType: " Restricted "})
	list := r.list()
	if r.u.isError || len(list) != 2 || !list[1].Restricted || list[1].Admin {
		t.Fatalf("add restricted: message %q, users %+v", r.u.message, list)
	}
	if !strings.Contains(r.rows[usFirstRow+1], "calc                      restricted   N") {
		t.Errorf("row %q", r.rows[usFirstRow+1])
	}

	// Each type sets admin and restricted to match.
	for _, c := range []struct {
		kind                  string
		admin, restricted, nu bool
	}{
		{"user", false, false, false},
		{"admin", true, false, false},
		{"newuser", false, false, true},
		{"restricted", false, true, false},
	} {
		r.key(go3270.AIDEnter, map[string]string{"utype:2": c.kind})
		if x := r.list()[1]; x.Admin != c.admin || x.Restricted != c.restricted || x.NewUser != c.nu || r.u.message != "Changed 1 user type." {
			t.Errorf("%s: %+v, message %q", c.kind, x, r.u.message)
		}
		if !strings.Contains(r.rows[usFirstRow+1], "calc                      "+c.kind) {
			t.Errorf("%s: row %q", c.kind, r.rows[usFirstRow+1])
		}
	}
	r.key(go3270.AIDEnter, map[string]string{"utype:2": "boss"})
	if !strings.Contains(r.u.message, `under Type, not "boss"`) || !r.list()[1].Restricted {
		t.Errorf("bad type: %q", r.u.message)
	}
	// Typed as shown, in any case, changes nothing.
	r.key(go3270.AIDEnter, map[string]string{"utype:2": "RESTRICTED"})
	if r.u.isError || strings.Contains(r.u.message, "Changed") {
		t.Errorf("same type: %q", r.u.message)
	}

	// The only admin cannot become anything else.
	r.key(go3270.AIDEnter, map[string]string{"utype:1": "restricted"})
	if !strings.Contains(r.u.message, "At least one user must be an admin") || !r.list()[0].Admin {
		t.Errorf("restrict the admin: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "newbie", usNewPassword: "long enough pw", usNewType: "newuser"})
	if x := r.list()[2]; !x.NewUser || x.Admin || x.Restricted {
		t.Errorf("add a new user: %+v, %q", x, r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewType: "restricted"})
	if r.u.message != "Type the new user's name." {
		t.Errorf("restricted with no name: %q", r.u.message)
	}
}

// TestUsersRemoveWithCalendar checks that a user's checklists and Google
// calendar go with them: their link with their entry, and their
// authorization at Google.
func TestUsersRemoveWithCalendar(t *testing.T) {
	fake := newFakeGoogle(t)
	r := newUsersRig(t)
	r.u.checklists = openChecklists(t)
	if err := r.u.checklists.Update(func(l *[]checklist.Checklist, nextID func() int) error {
		*l = append(*l,
			checklist.Checklist{ID: nextID(), Name: "admin's", Owner: 1},
			checklist.Checklist{ID: nextID(), Name: "joelle's", Owner: 2},
			checklist.Checklist{ID: nextID(), Name: "joelle's too", Owner: 2})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list, users.User{ID: nextID(), Name: "joelle",
			Google: &users.GoogleLink{ClientID: "cid", RefreshToken: "joelle-token", Calendars: []users.Calendar{{ID: "j@example.com"}}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.draw()

	r.key(go3270.AIDEnter, map[string]string{"usel:2": "D"})
	if text := strings.Join(r.rows, "\n"); !strings.Contains(text, "joelle with 2 checklists and their Google calendar") {
		t.Errorf("confirmation does not say the checklists and calendar go too:\n%s", text)
	}
	r.key(go3270.AIDPF4, nil)
	if list := r.list(); len(list) != 1 || r.u.isError || r.u.message != "Removed 1 user. Removed 2 checklists." {
		t.Fatalf("after removing: %+v, message %q", list, r.u.message)
	}
	if lists, _ := r.u.checklists.Load(); len(lists) != 1 || lists[0].Name != "admin's" {
		t.Errorf("checklists left: %+v; want only admin's", lists)
	}
	fake.mu.Lock()
	revoked := fake.revoked
	fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "joelle-token" {
		t.Errorf("revoked %v; want joelle's token", revoked)
	}
	if data, err := os.ReadFile(r.store.Path()); err != nil || strings.Contains(string(data), "joelle") {
		t.Errorf("the users file still has joelle: %v\n%s", err, data)
	}

	// A user with no calendar has nothing to withdraw.
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list, users.User{ID: nextID(), Name: "bob"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.draw()
	if bob := r.list()[1]; bob.ID != 3 {
		t.Errorf("bob was given ID %d, joelle's; want 3", bob.ID)
	}
	r.key(go3270.AIDEnter, map[string]string{"usel:3": "D"})
	r.key(go3270.AIDPF4, nil)
	fake.mu.Lock()
	n := len(fake.revoked)
	fake.mu.Unlock()
	if len(r.list()) != 1 || n != 1 {
		t.Errorf("removing bob: users %+v, %d revoked", r.list(), n)
	}
}

func TestUsersFlag(t *testing.T) {
	r := newUsersRig(t)
	r.key(go3270.AIDEnter, map[string]string{usNewName: "calc", usNewPassword: "long enough pw", usNewType: "restricted"})

	// Y under Flag gives a user control of the busy light.
	r.key(go3270.AIDEnter, map[string]string{"uflag:1": "y"})
	if !r.list()[0].Flag || r.u.message != "Changed 1 flag setting." || !strings.HasSuffix(r.rows[usFirstRow], "admin        Y") {
		t.Errorf("flag: message %q, row %q", r.u.message, r.rows[usFirstRow])
	}
	r.key(go3270.AIDEnter, map[string]string{"uflag:1": "z"})
	if !strings.Contains(r.u.message, `under Flag, not "Z"`) {
		t.Errorf("bad flag: %q", r.u.message)
	}
	// Not for a restricted user.
	r.key(go3270.AIDEnter, map[string]string{"uflag:2": "Y"})
	if !strings.Contains(r.u.message, "cannot both control the busy light and be restricted") || r.list()[1].Flag {
		t.Errorf("flag a restricted user: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{"uflag:1": "N"})
	if r.list()[0].Flag {
		t.Errorf("flag not cleared")
	}
}

func TestUsersPasswordRules(t *testing.T) {
	r := newUsersRig(t)
	for typed, want := range map[[2]string]string{
		{"joelle", "short"}:            "at least 8 characters",
		{"joelle", "my-JOELLE-secret"}: "cannot contain the user name",
	} {
		r.key(go3270.AIDEnter, map[string]string{usNewName: typed[0], usNewPassword: typed[1]})
		if !r.u.isError || !strings.Contains(r.u.message, want) || len(r.list()) != 1 {
			t.Errorf("%q: message %q, users %d", typed, r.u.message, len(r.list()))
		}
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "joelle", usNewPassword: "good secret"})
	if len(r.list()) != 2 {
		t.Fatalf("adding with a good password: %q", r.u.message)
	}
	// Changing one with P, likewise.
	r.key(go3270.AIDEnter, map[string]string{"usel:2": "p"})
	r.key(go3270.AIDEnter, map[string]string{"upw:2": "joellejoelle"})
	if !strings.Contains(r.u.message, "cannot contain the user name") || !users.CheckPassword(r.list()[1].Password, "good secret") {
		t.Errorf("changing to one with the name: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{"upw:2": "seven77"})
	if !strings.Contains(r.u.message, "at least 8 characters") {
		t.Errorf("changing to a short one: %q", r.u.message)
	}
}

func TestUsersNameLength(t *testing.T) {
	r := newUsersRig(t)
	for _, f := range r.s {
		if f.Name == usNewName {
			// The field holds no more than a name may have.
			end := -1
			for _, g := range r.s {
				if g.Row == f.Row && g.Col > f.Col && (end < 0 || g.Col < end) {
					end = g.Col
				}
			}
			if end-f.Col-1 != users.MaxNameLength {
				t.Errorf("new name field holds %d characters, want %d", end-f.Col-1, users.MaxNameLength)
			}
		}
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "ninechars", usNewPassword: "long enough pw"})
	if !strings.Contains(r.u.message, "longer than 8 characters") || len(r.list()) != 1 {
		t.Errorf("nine-character name: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "eightchr", usNewPassword: "long enough pw"})
	if len(r.list()) != 2 {
		t.Errorf("eight-character name refused: %q", r.u.message)
	}
}

func TestUsersNameCharacters(t *testing.T) {
	r := newUsersRig(t)
	for name, want := range map[string]string{
		"jo@x":  `The new user's name has '@' in it`,
		".jo":   `The new user's name starts with '.'`,
		"ADMIN": "already a user",
	} {
		r.key(go3270.AIDEnter, map[string]string{usNewName: name, usNewPassword: "long enough pw"})
		if !strings.Contains(r.u.message, want) || len(r.list()) != 1 {
			t.Errorf("%q: message %q, want %q", name, r.u.message, want)
		}
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "j.m-a_1", usNewPassword: "long enough pw"})
	if len(r.list()) != 2 {
		t.Errorf("j.m-a_1 refused: %q", r.u.message)
	}
}
