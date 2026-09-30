package users

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashPassword(t *testing.T) {
	h, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %q", h)
	}
	h2, _ := HashPassword("secret")
	if h == h2 {
		t.Error("same hash twice: no salt")
	}
	if !CheckPassword(h, "secret") || CheckPassword(h, "Secret") || CheckPassword(h, "") {
		t.Error("check wrong")
	}
	for _, bad := range []string{"", "secret", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$a2V5", "$argon2id$v=19$m=x$c2FsdA$a2V5", "$argon2id$v=19$m=8,t=0,p=1$c2FsdA$a2V5", h[:len(h)-3] + "!!!"} {
		if CheckPassword(bad, "secret") {
			t.Errorf("unreadable hash %q matched", bad)
		}
	}
	// A hash made with other parameters still checks.
	old := argon2Params
	argon2Params.memory, argon2Params.time = 8*1024, 1
	cheap, _ := HashPassword("secret")
	argon2Params = old
	if !strings.Contains(cheap, "m=8192,t=1") || !CheckPassword(cheap, "secret") {
		t.Errorf("hash with other parameters %q does not check", cheap)
	}
}

func TestLoadMakesFirstUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s := NewStore(path)
	list, created, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !created || len(list) != 1 || list[0].Name != "admin" || !list[0].Admin || !CheckPassword(list[0].Password, "admin") {
		t.Fatalf("first load: created %v, %+v", created, list)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, %v; want 0600", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"admin"`) && strings.Contains(string(data), `"password": "admin"`) {
		t.Error("password stored in the clear")
	}
	if _, created, _ := s.Load(); created {
		t.Error("made again")
	}

	u, ok, err := s.Authenticate("ADMIN", "admin")
	if err != nil || !ok || u.Name != "admin" {
		t.Errorf("authenticate: %+v %v %v", u, ok, err)
	}
	if _, ok, _ := s.Authenticate("admin", "wrong"); ok {
		t.Error("wrong password accepted")
	}
	if _, ok, _ := s.Authenticate("nobody", "admin"); ok {
		t.Error("no such user accepted")
	}
}

func TestUpdateValidates(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	add := func(name string, admin bool) error {
		return s.Update(func(list *[]User, nextID func() int) error {
			*list = append(*list, User{ID: nextID(), Name: name, Admin: admin, Password: "x"})
			return nil
		})
	}
	if err := add("joelle", false); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Joelle": "already a user", "": "must have a name", "a b": "space"} {
		if err := add(name, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", name, err, want)
		}
	}

	// The last admin cannot lose it, nor be removed.
	err := s.Update(func(list *[]User, _ func() int) error {
		(*list)[0].Admin = false
		return nil
	})
	if !errors.Is(err, ErrNoAdmin) {
		t.Errorf("unadmining the last admin: %v", err)
	}
	err = s.Update(func(list *[]User, _ func() int) error {
		*list = (*list)[1:]
		return nil
	})
	if !errors.Is(err, ErrNoAdmin) {
		t.Errorf("removing the last admin: %v", err)
	}
	list, _, _ := s.Load()
	if len(list) != 2 || list[1].ID != 2 || !list[0].Admin {
		t.Errorf("after refused changes: %+v", list)
	}

	// With another admin, it can.
	err = s.Update(func(list *[]User, _ func() int) error {
		(*list)[1].Admin = true
		*list = (*list)[1:]
		return nil
	})
	if err != nil {
		t.Errorf("handing over admin: %v", err)
	}
	if err := add("next", false); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := s.Load(); list[len(list)-1].ID != 3 {
		t.Errorf("IDs reused: %+v", list)
	}
}

func TestLoadBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, created, err := NewStore(path).Load(); err == nil || created {
		t.Errorf("bad file: created %v, %v", created, err)
	}
}

func TestRestrictedAdmin(t *testing.T) {
	list := []User{{ID: 1, Name: "admin", Admin: true}, {ID: 2, Name: "calc", Restricted: true}}
	if err := Validate(list); err != nil {
		t.Errorf("restricted non-admin: %v", err)
	}
	list[0].Restricted = true
	if err := Validate(list); err == nil || !strings.Contains(err.Error(), "both an admin and restricted") {
		t.Errorf("restricted admin: %v", err)
	}
}
