package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestPreferences(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p preferencesState
	build := func() []string {
		s, crow, ccol := buildPreferences(24, 80, now, u.Name, prefsOf(store, u.ID), &p)
		if crow != prefFirstRow || ccol != prefLabelWidth {
			t.Errorf("cursor at %d,%d; want the field", crow, ccol)
		}
		return screenText(t, s, 24, 80)
	}
	key := func(aid go3270.AID, v string) (bool, string) {
		return p.handle(go3270.Response{AID: aid, Values: map[string]string{prefNoFlashField: v}}, store, u, noLog)
	}

	rows := build()
	text := strings.Join(rows, "\n")
	for _, want := range []string{"PREFERENCES", "Your preferences (admin)", "Avoid flashing ===> N", "the timer shows DONE steadily", prefPrompt, "PF3=Back Enter=Save"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}

	// A bad answer is refused, and drawn again.
	if back, _ := key(go3270.AIDEnter, "q"); back || !p.isError || !strings.Contains(p.message, `not "Q"`) {
		t.Errorf("bad: back %v, %q", back, p.message)
	}
	if rows := build(); !strings.Contains(rows[prefFirstRow], "===> q") {
		t.Errorf("not drawn again: %q", rows[prefFirstRow])
	}
	// PF3 saves nothing.
	if back, said := key(go3270.AIDPF3, "y"); !back || said != "" || prefsOf(store, u.ID).NoFlash {
		t.Errorf("PF3: back %v, %q, saved %v", back, said, prefsOf(store, u.ID))
	}
	// Enter saves, in either case, and goes back saying so.
	p = preferencesState{}
	if back, said := key(go3270.AIDEnter, " y "); !back || said != "Your preferences are saved." || !prefsOf(store, u.ID).NoFlash {
		t.Errorf("Y: back %v, %q, saved %v", back, said, prefsOf(store, u.ID))
	}
	if rows := build(); !strings.Contains(rows[prefFirstRow], "===> Y") {
		t.Errorf("saved Y not shown: %q", rows[prefFirstRow])
	}
	if _, said := key(go3270.AIDEnter, "Y"); said != "Your preferences are unchanged." {
		t.Errorf("same again: %q", said)
	}
	if _, said := key(go3270.AIDEnter, "n"); said != "Your preferences are saved." || prefsOf(store, u.ID).NoFlash {
		t.Errorf("N: %q, saved %v", said, prefsOf(store, u.ID))
	}
	// Blank is N.
	key(go3270.AIDEnter, "y")
	if key(go3270.AIDEnter, ""); prefsOf(store, u.ID).NoFlash {
		t.Errorf("blank kept Y")
	}

	// Nothing to read: no preferences.
	if (prefsOf(nil, 1) != users.Preferences{}) || (prefsOf(store, 99) != users.Preferences{}) {
		t.Errorf("preferences of no store, or no user")
	}
}

func TestSettingsPreferencesStatus(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if got := settingsStatus(store, 1)["preferences"]; got != "" {
		t.Errorf("nothing set: %q", got)
	}
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[0].Preferences.NoFlash = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := settingsStatus(store, 1)["preferences"]; got != "no flashing" {
		t.Errorf("no flashing: %q", got)
	}
	command, _, _ := settingsChoice(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{settingsOptionField: "4"}})
	if command != "preferences" {
		t.Errorf("option 4 is %q", command)
	}
	s, _, _ := buildSettings(24, 80, now, "admin", map[string]string{"preferences": "no flashing"}, "", false)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "4 Preferences: how the screens behave (no flashing)") {
		t.Errorf("settings screen:\n%s", text)
	}
}
