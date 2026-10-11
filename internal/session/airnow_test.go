// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// fakeAirNow takes the keys it is given, refusing the rest, or fails to
// be asked at all with err; it counts the keys checked.
type fakeAirNow struct {
	good   string
	err    error
	checks int
}

func (f *fakeAirNow) CheckAirNowKey(_ context.Context, key string) error {
	f.checks++
	switch {
	case f.err != nil:
		return f.err
	case key != f.good:
		return fmt.Errorf("airnow: %w: Invalid API key", weather.ErrBadKey)
	}
	return nil
}

func TestAirNowKey(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var k airNowKeyState
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	check := &fakeAirNow{good: "GOOD-KEY"}
	key := func(aid go3270.AID, typed string) bool {
		return k.handle(go3270.Response{AID: aid, Values: map[string]string{airNowKeyField: typed}}, store, check, logf)
	}
	build := func() ([]string, int, int) {
		stored, err := store.AirNowKey()
		s, crow, ccol := buildAirNowKey(24, 80, now, stored, err, &k)
		return screenText(t, s, 24, 80), crow, ccol
	}
	stored := func() string {
		key, err := store.AirNowKey()
		if err != nil {
			t.Fatal(err)
		}
		return key
	}

	rows, crow, ccol := build()
	text := strings.Join(rows, "\n")
	for _, want := range []string{"AIRNOW API KEY", "AirNow API key none set: the air quality is forecast", "https://docs.airnowapi.org/", "API key ===>", "Blank the key to remove it.", "PF3=Back Enter=Save"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if crow != 10 || ccol != len(airNowKeyLabel)+2 || !strings.HasPrefix(rows[crow], " "+airNowKeyLabel) {
		t.Errorf("cursor at %d,%d: %q", crow, ccol, rows[crow])
	}

	// A key AirNow refuses is not kept, and is drawn again.
	if key(go3270.AIDEnter, " BAD-KEY ") || stored() != "" || !k.isError || k.message != "AirNow refused the key: Invalid API key" {
		t.Errorf("refused: %q, %q", stored(), k.message)
	}
	if rows, _, _ := build(); !strings.Contains(rows[10], "BAD-KEY") {
		t.Errorf("not drawn again: %q", rows[10])
	}
	// Nor one with a space in it, which is not asked about.
	checks := check.checks
	if key(go3270.AIDEnter, "TWO WORDS"); stored() != "" || !k.isError || check.checks != checks {
		t.Errorf("space: %q, %q, %d checks", stored(), k.message, check.checks-checks)
	}
	// One it takes is kept, the key not logged.
	if key(go3270.AIDEnter, "GOOD-KEY") || stored() != "GOOD-KEY" || k.isError || k.message != "Saved; AirNow takes it." {
		t.Errorf("good: %q, %q", stored(), k.message)
	}
	if len(logged) != 1 || logged[0] != "set the AirNow API key" {
		t.Errorf("logged %q", logged)
	}
	if rows, _, _ := build(); !strings.Contains(rows[2], "set: the air quality is measured") || !strings.Contains(rows[10], "GOOD-KEY") {
		t.Errorf("set:\n%s", strings.Join(rows, "\n"))
	}
	// The same again is not asked about.
	checks = check.checks
	if key(go3270.AIDEnter, "GOOD-KEY"); check.checks != checks || k.message != "" {
		t.Errorf("same: %d checks, %q", check.checks-checks, k.message)
	}

	// One that cannot be checked is kept, saying so.
	check.err = errors.New("airnow: dial tcp: no route to host")
	if key(go3270.AIDEnter, "OTHER-KEY"); stored() != "OTHER-KEY" || k.isError || !strings.Contains(k.message, "could not be asked") {
		t.Errorf("unchecked: %q, %q", stored(), k.message)
	}
	check.err = nil

	// PF3 with a change typed asks first; a second PF3 leaves.
	if key(go3270.AIDPF3, "NEW-KEY") || !k.leaveArmed || stored() != "OTHER-KEY" {
		t.Errorf("first PF3: %v, %q", k.leaveArmed, stored())
	}
	if !key(go3270.AIDPF3, "NEW-KEY") || stored() != "OTHER-KEY" {
		t.Errorf("second PF3 did not leave, or saved %q", stored())
	}
	if !key(go3270.AIDPF3, "OTHER-KEY") {
		t.Error("PF3 with nothing changed did not leave")
	}

	// Blank removes it, unasked.
	checks = check.checks
	if key(go3270.AIDEnter, "  "); stored() != "" || check.checks != checks || !strings.Contains(k.message, "Removed") {
		t.Errorf("removed: %q, %q", stored(), k.message)
	}

	// With no way to check, a key is kept unchecked.
	if k.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{airNowKeyField: "ANY-KEY"}}, store, nil, logf); stored() != "ANY-KEY" {
		t.Errorf("no checker: %q", stored())
	}
	for _, l := range logged {
		if strings.Contains(l, "KEY") {
			t.Errorf("a key logged: %q", l)
		}
	}
}

func TestAdminAirNowOption(t *testing.T) {
	if action, _ := adminChoice(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{adminOptionField: "11"}}); action != adminAirNowKey {
		t.Errorf("11: %v", action)
	}
	s, _, _ := buildAdmin(24, 80, now, "", false, 1)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, " 11 AirNow API key") {
		t.Errorf("menu lacks option 11:\n%s", text)
	}
}
