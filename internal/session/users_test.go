package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

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
		usColumnRow: " S Name                      Admin Restricted",
		usFirstRow:  "   admin                     Y     N",
		20:          " New user ===>                       Admin ===>   Restricted ===>",
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
		case f.Row == usFirstRow && (f.Col == usNameCol || f.Col == usEndCol || f.Col == usResEndCol) && !f.Autoskip:
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
	r.key(go3270.AIDEnter, map[string]string{usNewName: "joelle", usNewPassword: "pw", usNewAdmin: "q"})
	if !strings.Contains(r.u.message, `not "Q"`) {
		t.Errorf("bad admin: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: " joelle ", usNewPassword: "pw one", usNewAdmin: "n"})
	list := r.list()
	if r.u.isError || r.u.message != "Added joelle." || len(list) != 2 || list[1].Name != "joelle" || list[1].Admin || !users.CheckPassword(list[1].Password, "pw one") {
		t.Fatalf("add: message %q, users %+v", r.u.message, list)
	}
	if r.crow != 20 {
		t.Errorf("cursor after adding on row %d, want the new user row", r.crow)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "JOELLE", usNewPassword: "x"})
	if !strings.Contains(r.u.message, "already a user") {
		t.Errorf("duplicate: %q", r.u.message)
	}

	// The only admin cannot lose it, nor be removed.
	r.key(go3270.AIDEnter, map[string]string{"uadm:1": "N"})
	if r.u.message != "At least one user must be an admin: that would leave none." || !r.list()[0].Admin || r.u.typed["uadm:1"] != "N" {
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

	// Handing over admin and removing the old one, in one go: asked first,
	// with nothing saved until PF4.
	r.key(go3270.AIDEnter, map[string]string{"uadm:2": "y", "usel:1": "D"})
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
	if r.u.pending != nil || r.u.message != "Nothing was saved." || len(r.list()) != 2 || r.u.typed["usel:1"] != "D" || r.u.typed["uadm:2"] != "y" {
		t.Fatalf("PF3: message %q, typed %v", r.u.message, r.u.typed)
	}
	if !strings.Contains(strings.Join(r.rows, "\n"), "joelle                    y") {
		t.Errorf("typed not drawn again after PF3:\n%s", strings.Join(r.rows, "\n"))
	}
	r.key(go3270.AIDEnter, map[string]string{"uadm:2": "y", "usel:1": "D"})
	r.key(go3270.AIDPF4, nil)
	list = r.list()
	if r.u.isError || len(list) != 1 || list[0].Name != "joelle" || !list[0].Admin {
		t.Fatalf("hand over: message %q, users %+v", r.u.message, list)
	}
	if r.u.message != "Removed 1 user. Changed 1 admin setting." {
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
	r.key(go3270.AIDEnter, map[string]string{"upw:2": "new pw"})
	if r.u.message != "Changed the password of joelle." || r.u.passwordFor != 0 || !users.CheckPassword(r.list()[0].Password, "new pw") {
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
	r.key(go3270.AIDEnter, map[string]string{usNewName: "calc", usNewPassword: "pw", usNewRes: "y"})
	list := r.list()
	if r.u.isError || len(list) != 2 || !list[1].Restricted || list[1].Admin {
		t.Fatalf("add restricted: message %q, users %+v", r.u.message, list)
	}
	if !strings.Contains(r.rows[usFirstRow+1], "calc                      N     Y") {
		t.Errorf("row %q", r.rows[usFirstRow+1])
	}

	r.key(go3270.AIDEnter, map[string]string{"ures:2": "N"})
	if r.list()[1].Restricted || r.u.message != "Changed 1 restricted setting." {
		t.Errorf("unrestrict: message %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{"ures:2": "q"})
	if !strings.Contains(r.u.message, `under Restricted, not "Q"`) {
		t.Errorf("bad flag: %q", r.u.message)
	}

	// No admin is restricted.
	r.key(go3270.AIDEnter, map[string]string{"ures:1": "Y"})
	if !strings.Contains(r.u.message, "cannot be both an admin and restricted") || r.list()[0].Restricted {
		t.Errorf("restrict the admin: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewName: "both", usNewPassword: "pw", usNewAdmin: "Y", usNewRes: "Y"})
	if !strings.Contains(r.u.message, "cannot be both") || len(r.list()) != 2 {
		t.Errorf("add a restricted admin: %q", r.u.message)
	}
	r.key(go3270.AIDEnter, map[string]string{usNewRes: "Y"})
	if r.u.message != "Type the new user's name." {
		t.Errorf("restricted with no name: %q", r.u.message)
	}
}
