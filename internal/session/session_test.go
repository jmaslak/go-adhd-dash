package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestControlsLight(t *testing.T) {
	if controlsLight(nil) {
		t.Errorf("a session not logged in can set the light")
	}
	if controlsLight(&users.User{Name: "a", Admin: true}) || !controlsLight(&users.User{Name: "b", Flag: true}) {
		t.Errorf("light control not by the flag setting")
	}
}

func TestFlagEvents(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Users: store}
	if _, n := cfg.FlagEvents(); n != 0 {
		t.Errorf("%d controllers with none marked", n)
	}

	// The light's meetings are the agenda's, for each user marked, less
	// all-day and out-of-office events.
	src := &staticAgenda{events: []agenda.Event{
		{Summary: "Standup", Start: now, End: now.Add(15 * time.Minute)},
		{Summary: "Holiday", Start: dayOf(now), End: dayOf(now).AddDate(0, 0, 1), AllDay: true},
		{Summary: "OOO", Start: now, End: now.Add(time.Hour)},
	}}
	cfg.Agenda = agenda.NewCache(src)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cfg.Agenda.Run(ctx, time.Hour)
	for deadline := time.Now().Add(5 * time.Second); cfg.Agenda.Snapshot().Fetched.IsZero() && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if err := store.Update(func(list *[]users.User, nextID func() int) error {
		(*list)[0].Flag = true
		*list = append(*list, users.User{ID: nextID(), Name: "other"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	meetings, n := cfg.FlagEvents()
	if n != 1 || len(meetings) != 1 || meetings[0].Summary != "Standup" {
		t.Errorf("controllers %d, meetings %+v", n, meetings)
	}
}

// staticAgenda is an agenda source with fixed events.
type staticAgenda struct{ events []agenda.Event }

func (s *staticAgenda) Events(context.Context, time.Time, time.Time) ([]agenda.Event, error) {
	return s.events, nil
}

func TestLightFor(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(list *[]users.User, nextID func() int) error {
		(*list)[0].Flag = true
		*list = append(*list, users.User{ID: nextID(), Name: "bob"}, users.User{ID: nextID(), Name: "carol"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	light := busy.New(nil, func() ([]agenda.Event, int) { return nil, 1 })
	cfg := Config{Users: store, Busy: light, BusyKeys: light}
	cfg.Personal = busy.NewPersonal(func(id int) []agenda.Event { return cfg.UserMeetings(id) })
	admin, bob, carol := &users.User{ID: 1, Flag: true}, &users.User{ID: 2}, &users.User{ID: 3}

	// The flag user sees and sets the light; the others, each their own.
	if src, keys := cfg.lightFor(admin); src != busy.Source(light) || keys == nil {
		t.Errorf("flag user does not get the light")
	}
	bobSrc, bobKeys := cfg.lightFor(bob)
	carolSrc, _ := cfg.lightFor(carol)
	if bobSrc == busy.Source(light) || bobSrc == carolSrc || bobKeys == nil {
		t.Fatalf("other users do not get states of their own")
	}
	bobKeys.Key('b') //nolint:errcheck
	if bobSrc.Status().Light != "red" || carolSrc.Status().Light != "off" || light.Status().Light != "off" {
		t.Errorf("bob's key: bob %s, carol %s, light %s", bobSrc.Status().Light, carolSrc.Status().Light, light.Status().Light)
	}
	if !bobSrc.Status().Enabled {
		t.Errorf("a user's own state is not shown")
	}

	// Made a flag user, bob gets the light at once, though his session's
	// copy of him says otherwise.
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[1].Flag = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if src, _ := cfg.lightFor(bob); src != busy.Source(light) {
		t.Errorf("bob, made a flag user, does not get the light")
	}
}

// TestSimulatedFlag checks the banner and title color each user sees: a
// flag user's follow the light, another user's their own simulated flag,
// and neither one's keys touch the other's.
func TestSimulatedFlag(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(list *[]users.User, nextID func() int) error {
		(*list)[0].Flag = true
		*list = append(*list, users.User{ID: nextID(), Name: "bob"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	light := busy.New(nil, func() ([]agenda.Event, int) { return nil, 1 })
	cfg := Config{Users: store, Busy: light, BusyKeys: light}
	cfg.Personal = busy.NewPersonal(func(id int) []agenda.Event { return cfg.UserMeetings(id) })
	flagUser, bob := &users.User{ID: 1, Flag: true}, &users.User{ID: 2}

	// sees is the user's banner, and their title row's color.
	sees := func(u *users.User) string {
		t.Helper()
		src, keys := cfg.lightFor(u)
		v := gather(cfg, now, nil, nil, u.ID, src)
		v.BusyKeys = keys != nil
		s, _, _, _ := buildDashboard(24, 80, v, 0, "")
		banner := "none"
		for _, b := range []string{"IN MEETING", "AVAILABLE", "NOT IN MEETING"} {
			if strings.Contains(strings.Join(screenText(t, s, 24, 80), "\n"), "** "+b+" **") {
				banner = b
			}
		}
		color, ok := headerColor(src)
		if !ok {
			return banner + "/untinted"
		}
		return fmt.Sprintf("%s/%v", banner, color)
	}
	key := func(u *users.User, k rune) {
		t.Helper()
		_, keys := cfg.lightFor(u)
		if err := keys.Key(k); err != nil {
			t.Fatal(err)
		}
	}

	untinted := "NOT IN MEETING/untinted"
	red, green := fmt.Sprintf("IN MEETING/%v", go3270.Red), fmt.Sprintf("AVAILABLE/%v", go3270.Green)
	for _, step := range []struct {
		who       *users.User
		key       rune
		flag, bob string
	}{
		{flagUser, 'b', red, untinted},   // the flag user's red is not bob's
		{bob, 'g', red, green},           // bob's green is not the flag's
		{flagUser, 'o', untinted, green}, // the flag off leaves bob green
		{bob, 'b', untinted, red},        // bob red leaves the flag off
		{bob, 'o', untinted, untinted},
	} {
		key(step.who, step.key)
		if got := sees(flagUser); got != step.flag {
			t.Errorf("after %c by user %d: the flag user sees %s, want %s", step.key, step.who.ID, got, step.flag)
		}
		if got := sees(bob); got != step.bob {
			t.Errorf("after %c by user %d: bob sees %s, want %s", step.key, step.who.ID, got, step.bob)
		}
	}
	if light.Status().Light != "off" {
		t.Errorf("the light ended %s", light.Status().Light)
	}
}

func TestLightWatch(t *testing.T) {
	a := busy.New(nil, func() ([]agenda.Event, int) { return nil, 1 })
	b := busy.New(nil, func() ([]agenda.Event, int) { return nil, 1 })
	a.Key('.') //nolint:errcheck
	b.Key('.') //nolint:errcheck
	woken := 0
	wake := func() { woken++ }

	var w lightWatch
	w.follow(a, wake)
	w.follow(a, wake) // the same: watched once
	a.Key('b')        //nolint:errcheck
	if woken != 1 {
		t.Errorf("woken %d times by a's change, want 1", woken)
	}
	if !w.differs(busy.Status{Enabled: true, Light: "off"}) || w.differs(a.Status()) {
		t.Errorf("differs wrong")
	}

	// Following b instead: a no longer wakes.
	w.follow(b, wake)
	a.Key('o') //nolint:errcheck
	b.Key('g') //nolint:errcheck
	if woken != 2 {
		t.Errorf("woken %d times, want 2: once by b, none by a", woken)
	}
	w.stop()
	b.Key('b') //nolint:errcheck
	if woken != 2 || w.differs(busy.Status{}) {
		t.Errorf("woken after stopping, or differs with nothing followed")
	}
}
