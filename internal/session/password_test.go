package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestPasswordChange(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	hash, err := users.HashPassword(context.Background(), "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list, users.User{ID: nextID(), Name: "joelle", Password: hash})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	u := &users.User{ID: 2, Name: "joelle"}
	var p passwordState
	key := func(aid go3270.AID, current, next, again string) (bool, string) {
		return p.handle(go3270.Response{AID: aid, Values: map[string]string{pwCurrentField: current, pwNewField: next, pwAgainField: again}}, store, u, func(string, ...any) {})
	}
	works := func(password string) bool {
		_, ok, err := store.Authenticate(context.Background(), "joelle", password)
		return err == nil && ok
	}

	// The screen: three hidden fields, nothing drawn in them.
	s, crow, ccol := buildPassword(24, 80, now, "joelle", &p)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, "Change your password (joelle)") || crow != pwFirstRow || ccol != pwLabelWidth+1 {
		t.Errorf("screen:\n%s", text)
	}
	for _, f := range s {
		if f.Write && (!f.Hidden || f.Content != "") {
			t.Errorf("field %q shows what is typed", f.Name)
		}
	}

	for _, c := range []struct{ current, next, again, want string }{
		{"", "a", "a", "Type your current password"},
		{"old-secret", "new-one", "new-two", "not typed the same twice"},
		{"old-secret", "old-secret", "old-secret", "same as the old one"},
		{"old-secret", "short", "short", "at least 8 characters"},
		{"old-secret", "Joelle-rocks", "Joelle-rocks", "cannot contain the user name"},
	} {
		if leave, _ := key(go3270.AIDEnter, c.current, c.next, c.again); leave || !strings.Contains(p.message, c.want) {
			t.Errorf("%+v: leave %v, message %q", c, leave, p.message)
		}
	}
	if !works("old-secret") {
		t.Fatalf("the password changed on a refusal")
	}

	// A wrong current password is refused, and counted.
	if leave, _ := key(go3270.AIDEnter, "wrong", "new-secret", "new-secret"); leave || !strings.Contains(p.message, "not your current password") {
		t.Errorf("wrong current: leave %v, %q", leave, p.message)
	}

	// Right, it is changed, and the screen left saying so.
	leave, said := key(go3270.AIDEnter, "old-secret", "new-secret", "new-secret")
	if !leave || said != "Your password is changed." || !works("new-secret") || works("old-secret") {
		t.Errorf("change: leave %v, said %q", leave, said)
	}

	// Three wrong current passwords leave.
	p = passwordState{}
	for i := range pwTries {
		leave, said = key(go3270.AIDEnter, "wrong", "x-secret", "x-secret")
		if (i < pwTries-1) == leave {
			t.Errorf("try %d: leave %v", i+1, leave)
		}
	}
	if !strings.Contains(said, "Too many wrong passwords") || !works("new-secret") {
		t.Errorf("after %d wrong: said %q", pwTries, said)
	}
	if leave, _ := key(go3270.AIDPF3, "", "", ""); !leave {
		t.Errorf("PF3 did not leave")
	}

	// The admin user cannot go back to the default password.
	admin := &users.User{ID: 1, Name: users.FirstName}
	p = passwordState{}
	if leave, _ := p.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{
		pwCurrentField: users.FirstPassword + "x", pwNewField: users.FirstPassword, pwAgainField: users.FirstPassword,
	}}, store, admin, func(string, ...any) {}); leave || !strings.Contains(p.message, "default password") {
		t.Errorf("default password: %q", p.message)
	}
}

// TestPasswordForced checks the password screen for admin logged in with
// the default password: it says why, offers only logging off, and records
// the change once made.
func TestPasswordForced(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	admin := &list[0]
	p := passwordState{forced: true}
	s, _, _ := buildPassword(24, 80, now, admin.Name, &p)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"You logged in with the default password: choose a new one to go on.", "PF3=Log off"} {
		if !strings.Contains(text, want) {
			t.Errorf("forced screen lacks %q:\n%s", want, text)
		}
	}
	key := func(aid go3270.AID, current, next, again string) (bool, string) {
		return p.handle(go3270.Response{AID: aid, Values: map[string]string{pwCurrentField: current, pwNewField: next, pwAgainField: again}}, store, admin, noLog)
	}
	if leave, _ := key(go3270.AIDEnter, users.FirstPassword, users.FirstPassword, users.FirstPassword); leave || p.changed || !strings.Contains(p.message, "the same as the old one") {
		t.Errorf("kept the default: leave %v, changed %v, %q", leave, p.changed, p.message)
	}
	if leave, _ := key(go3270.AIDPF3, "", "", ""); !leave || p.changed {
		t.Errorf("PF3: leave %v, changed %v", leave, p.changed)
	}
	if leave, said := key(go3270.AIDEnter, users.FirstPassword, "a real one now", "a real one now"); !leave || !p.changed || said != "Your password is changed." {
		t.Errorf("changed: leave %v, changed %v, %q", leave, p.changed, said)
	}
	if _, ok, _ := store.Authenticate(t.Context(), "admin", "a real one now"); !ok {
		t.Error("the new password does not work")
	}
}
