package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestSettingsScreen(t *testing.T) {
	status := map[string]string{"google": "connected, 2 calendars shown", "trello": "not linked"}
	s, crow, ccol := buildSettings(24, 80, now, "joelle", status, "", false)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"SETTINGS", "YOUR SETTINGS (joelle)",
		"1 Password: change it",
		"2 Google calendar: connect, choose calendars (connected, 2 calendars shown)",
		"3 Trello: link, choose your task lists (not linked)", "Type an option's number", "PF3=Back Enter=Select",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings screen lacks %q:\n%s", want, text)
		}
	}
	if crow != 21 || ccol != len(adminOptionLabel)+2 {
		t.Errorf("cursor at %d,%d; want the option field", crow, ccol)
	}
	// A message, red as an error or white as a report.
	for _, isError := range []bool{true, false} {
		s, _, _ = buildSettings(24, 80, now, "joelle", nil, "Your password is changed.", isError)
		for _, f := range s {
			if f.Content == "Your password is changed." && (f.Color == go3270.Red) != isError {
				t.Errorf("message colored %v, isError %v", f.Color, isError)
			}
		}
	}

	for _, c := range []struct {
		aid           go3270.AID
		typed         string
		command       string
		leave, refuse bool
	}{
		{go3270.AIDPF3, "1", "", true, false},
		{go3270.AIDEnter, " 1 ", "password", false, false},
		{go3270.AIDEnter, "2", "google", false, false},
		{go3270.AIDEnter, "3", "trello", false, false},
		{go3270.AIDEnter, "", "", false, false},
		{go3270.AIDEnter, "9", "", false, true},
		{go3270.AIDPF4, "1", "", false, false},
	} {
		command, leave, msg := settingsChoice(go3270.Response{AID: c.aid, Values: map[string]string{settingsOptionField: c.typed}})
		if command != c.command || leave != c.leave || (msg != "") != c.refuse {
			t.Errorf("%x %q: %q, %v, %q", c.aid, c.typed, command, leave, msg)
		}
	}
}

func TestSettingsStatus(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if got := settingsStatus(store, 1); got["google"] != "not connected" || got["trello"] != "not linked" {
		t.Errorf("nothing linked: %v", got)
	}
	if err := store.SetGoogleClient(&users.GoogleClient{ClientID: "cid", ClientSecret: "cs"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrelloClient(&users.TrelloClient{APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[0].Google = &users.GoogleLink{ClientID: "cid", RefreshToken: "rt", Calendars: []users.Calendar{{ID: "a"}, {ID: "b"}}}
		(*list)[0].Trello = &users.TrelloLink{APIKey: "old key", Token: "tok", Lists: []users.TrelloList{{ListID: "l1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := settingsStatus(store, 1); got["google"] != "connected, 2 calendars shown" || got["trello"] != "to link again" {
		t.Errorf("linked: %v", got)
	}
	if got := settingsStatus(nil, 1); len(got) != 0 {
		t.Errorf("no store: %v", got)
	}
}
