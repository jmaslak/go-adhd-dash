package users

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashPassword(t *testing.T) {
	h, err := HashPassword(context.Background(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %q", h)
	}
	h2, _ := HashPassword(context.Background(), "secret")
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
	cheap, _ := HashPassword(context.Background(), "secret")
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
	if !created || len(list) != 1 || list[0].Name != "admin" || !list[0].Admin || !list[0].Console || !CheckPassword(list[0].Password, "admin") {
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

	u, ok, err := s.Authenticate(context.Background(), "ADMIN", "admin")
	if err != nil || !ok || u.Name != "admin" {
		t.Errorf("authenticate: %+v %v %v", u, ok, err)
	}
	if _, ok, _ := s.Authenticate(context.Background(), "admin", "wrong"); ok {
		t.Error("wrong password accepted")
	}
	if _, ok, _ := s.Authenticate(context.Background(), "nobody", "admin"); ok {
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
	for name, want := range map[string]string{"Joelle": "already a user", "": "must have a name", "a b": "space", "a\u001db": "control character", "x\u009f": "control character"} {
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

	// Nor can the console's user be removed, even with another admin.
	err = s.Update(func(list *[]User, _ func() int) error {
		(*list)[1].Admin = true
		*list = (*list)[1:]
		return nil
	})
	if !errors.Is(err, ErrNoConsole) {
		t.Errorf("removing the console's user: %v", err)
	}

	// With the console handed over too, it can.
	err = s.Update(func(list *[]User, _ func() int) error {
		(*list)[1].Admin, (*list)[1].Console = true, true
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
	list := []User{{ID: 1, Name: "admin", Admin: true, Console: true}, {ID: 2, Name: "calc", Restricted: true}}
	if err := Validate(list); err != nil {
		t.Errorf("restricted non-admin: %v", err)
	}
	list[0].Restricted = true
	if err := Validate(list); err == nil || !strings.Contains(err.Error(), "both an admin and restricted") {
		t.Errorf("restricted admin: %v", err)
	}
}

func TestConsole(t *testing.T) {
	// A file from before there was a console user gets one: admin, if an
	// admin, else the first admin.
	for _, c := range []struct{ json, want string }{
		{`{"users":[{"id":1,"name":"joelle","admin":true},{"id":2,"name":"Admin","admin":true}]}`, "Admin"},
		{`{"users":[{"id":1,"name":"admin"},{"id":2,"name":"joelle","admin":true}]}`, "joelle"},
		{`{"users":[{"id":1,"name":"admin","admin":true},{"id":2,"name":"joelle","console":true}]}`, "joelle"},
	} {
		path := filepath.Join(t.TempDir(), "users.json")
		if err := os.WriteFile(path, []byte(c.json), 0o600); err != nil {
			t.Fatal(err)
		}
		list, _, err := NewStore(path).Load()
		if err != nil {
			t.Fatal(err)
		}
		if u, ok := ConsoleUser(list); !ok || u.Name != c.want {
			t.Errorf("%s: console user %+v, %v; want %s", c.json, u, ok, c.want)
		}
		if err := Validate(list); err != nil {
			t.Errorf("%s: %v", c.json, err)
		}
	}

	list := []User{{ID: 1, Name: "admin", Admin: true}, {ID: 2, Name: "joelle"}}
	if err := Validate(list); !errors.Is(err, ErrNoConsole) {
		t.Errorf("no console user: %v", err)
	}
	list[0].Console, list[1].Console = true, true
	if err := Validate(list); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Errorf("two console users: %v", err)
	}
	list[0].Console = false
	if err := Validate(list); err != nil {
		t.Errorf("a non-admin console user: %v", err)
	}
}

func TestHashSlots(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	// Every slot taken: a check waits until its context is done, then
	// fails with ErrBusy, having worked nothing out.
	for range MaxConcurrentHashes {
		hashSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	_, ok, err := s.Authenticate(ctx, "admin", "admin")
	cancel()
	if !errors.Is(err, ErrBusy) || ok {
		t.Errorf("all slots taken: ok %v, err %v", ok, err)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > time.Second {
		t.Errorf("gave up after %v, want about the timeout", d)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	if _, err := HashPassword(ctx, "x"); !errors.Is(err, ErrBusy) {
		t.Errorf("hashing with all slots taken: %v", err)
	}
	cancel()

	// A slot comes free while one waits: it goes ahead.
	done := make(chan bool)
	go func() {
		_, ok, err := s.Authenticate(context.Background(), "admin", "admin")
		done <- ok && err == nil
	}()
	time.Sleep(20 * time.Millisecond)
	<-hashSlots
	if !<-done {
		t.Error("waiting check did not go ahead once a slot came free")
	}
	for range MaxConcurrentHashes - 1 {
		<-hashSlots
	}
	if len(hashSlots) != 0 {
		t.Errorf("%d slots left taken", len(hashSlots))
	}
}
