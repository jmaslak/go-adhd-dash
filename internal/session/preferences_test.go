// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
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
		return p.handle(go3270.Response{AID: aid, Values: map[string]string{prefNoFlashField: v}}, store, u, nil, noLog)
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

	// A time zone is saved by name; one that does not exist is refused,
	// and blank is the server's.
	zoneKey := func(v string) (bool, string) {
		return p.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{prefNoFlashField: "N", prefZoneField: v}}, store, u, nil, noLog)
	}
	if back, said := zoneKey(" Asia/Tokyo "); !back || said != "Your preferences are saved." || prefsOf(store, u.ID).TimeZone != "Asia/Tokyo" {
		t.Errorf("zone: back %v, %q, saved %+v", back, said, prefsOf(store, u.ID))
	}
	if rows := build(); !strings.Contains(rows[prefZoneRow], "===> Asia/Tokyo") || !strings.Contains(strings.Join(rows, "\n"), "Times are shown in Asia/Tokyo (JST).") {
		t.Errorf("saved zone not shown:\n%s", strings.Join(rows, "\n"))
	}
	if loc := (Config{Users: store}).locationFor(u); loc.String() != "Asia/Tokyo" {
		t.Errorf("user's location is %v", loc)
	}
	for _, bad := range []string{"Mars/Olympus_Mons", "Local", "../etc/passwd"} {
		if back, _ := zoneKey(bad); back || !p.isError || !strings.Contains(p.message, "no time zone") || prefsOf(store, u.ID).TimeZone != "Asia/Tokyo" {
			t.Errorf("%q: back %v, %q, saved %+v", bad, back, p.message, prefsOf(store, u.ID))
		}
	}
	if rows := build(); !strings.Contains(rows[prefZoneRow], "===> ../etc/passwd") {
		t.Errorf("bad zone not drawn again: %q", rows[prefZoneRow])
	}
	if _, said := zoneKey(""); said != "Your preferences are saved." || prefsOf(store, u.ID).TimeZone != "" {
		t.Errorf("blank zone: %q, saved %+v", said, prefsOf(store, u.ID))
	}
	if loc := (Config{Users: store}).locationFor(u); loc != time.Local {
		t.Errorf("blank zone's location is %v", loc)
	}
	if (Config{Users: store}).locationFor(nil) != time.Local {
		t.Errorf("no one's location is not the server's")
	}

	// Nothing to read: no preferences.
	if !reflect.DeepEqual(prefsOf(nil, 1), users.Preferences{}) || !reflect.DeepEqual(prefsOf(store, 99), users.Preferences{}) {
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
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[0].Preferences.TimeZone = "Asia/Tokyo"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := settingsStatus(store, 1)["preferences"]; got != "no flashing, Asia/Tokyo" {
		t.Errorf("no flashing and a zone: %q", got)
	}
	command, _, _ := settingsChoice(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{settingsOptionField: "4"}})
	if command != "preferences" {
		t.Errorf("option 4 is %q", command)
	}
	s, _, _ := buildSettings(24, 80, now, "admin", map[string]string{"preferences": "no flashing"}, "", false)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "4 Preferences: time zone, clocks, weather, flashing (no flashing)") {
		t.Errorf("settings screen:\n%s", text)
	}
}

func TestPreferencesZoneList(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p preferencesState
	build := func(rows, cols int) (text []string, crow int) {
		s, crow, _ := buildPreferences(rows, cols, now, u.Name, prefsOf(store, u.ID), &p)
		return screenText(t, s, rows, cols), crow
	}
	key := func(aid go3270.AID, row, col int, zone string) {
		if back, _ := p.handle(go3270.Response{AID: aid, Row: row, Col: col, Values: map[string]string{prefNoFlashField: "Y", prefZoneField: zone}}, store, u, nil, noLog); back {
			t.Fatalf("%v went back", aid)
		}
	}

	// PF1 off the time zone says where to press it.
	key(go3270.AIDPF1, prefFirstRow, prefLabelWidth, "")
	if p.listing || !strings.Contains(p.message, "cursor is on a time zone") {
		t.Errorf("PF1 off the zone: listing %v, %q", p.listing, p.message)
	}

	// PF1 on it lists them, opening at the page of the zone typed, which
	// is picked out.
	key(go3270.AIDPF1, prefZoneRow, prefLabelWidth+3, "Europe/Amsterdam")
	rows, _ := build(24, 80)
	text := strings.Join(rows, "\n")
	if !p.listing || !strings.Contains(rows[0], "TIME ZONES") || !strings.Contains(text, "Europe/Amsterdam") || p.page == 0 {
		t.Fatalf("zone list, page %d:\n%s", p.page, text)
	}
	if !strings.Contains(text, fmt.Sprintf("page %d of %d", p.page+1, p.pages)) || !strings.Contains(text, "PF7=Up PF8=Down") {
		t.Errorf("no page count or keys:\n%s", text)
	}
	// Several columns to a row.
	if f := strings.Fields(rows[zoneListRow]); len(f) < 3 {
		t.Errorf("row %d has %d names: %q", zoneListRow, len(f), rows[zoneListRow])
	}

	// Paging every page shows every zone once, and stops at either end.
	p.page = 0
	seen := map[string]int{}
	for i := 0; i < p.pages; i++ {
		rows, _ := build(24, 80)
		for _, r := range rows[zoneListRow : 24-2] {
			for _, f := range strings.Fields(r) {
				seen[f]++
			}
		}
		if i < p.pages-1 {
			key(go3270.AIDPF8, 0, 0, "")
		}
	}
	for _, n := range zoneNames {
		if seen[n] != 1 {
			t.Errorf("%s shown %d times", n, seen[n])
		}
	}
	key(go3270.AIDPF8, 0, 0, "")
	if p.page != p.pages-1 {
		t.Errorf("PF8 at the end went to page %d of %d", p.page+1, p.pages)
	}
	for range p.pages + 1 {
		key(go3270.AIDPF7, 0, 0, "")
	}
	if p.page != 0 {
		t.Errorf("PF7 at the start went to page %d", p.page+1)
	}

	// PF3 goes back to the preferences as typed, cursor on the time zone,
	// having saved nothing.
	key(go3270.AIDPF3, 0, 0, "")
	rows, crow := build(24, 80)
	if p.listing || crow != prefZoneRow || !strings.Contains(rows[prefZoneRow], "Europe/Amsterdam") || !strings.Contains(rows[prefFirstRow], "===> Y") {
		t.Errorf("back from the list, cursor row %d:\n%s", crow, strings.Join(rows, "\n"))
	}
	if !reflect.DeepEqual(prefsOf(store, u.ID), users.Preferences{}) {
		t.Errorf("listing saved %+v", prefsOf(store, u.ID))
	}

	// A wide screen fits more to a page.
	narrow := len(zonePages(17, 79))
	if wide := len(zonePages(23, 131)); wide >= narrow {
		t.Errorf("27x132 has %d pages, 24x80 %d", wide, narrow)
	}
}

func TestZoneNamesLoad(t *testing.T) {
	if len(zoneNames) < 400 || !slices.Contains(zoneNames, "UTC") || !slices.Contains(zoneNames, "America/Denver") {
		t.Fatalf("%d zone names", len(zoneNames))
	}
	for _, n := range zoneNames {
		if _, err := loadZone(n); err != nil {
			t.Errorf("listed zone %q does not load: %v", n, err)
		}
	}
}

func TestPreferencesClocks(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p preferencesState
	build := func() []string {
		s, _, _ := buildPreferences(24, 80, now, u.Name, prefsOf(store, u.ID), &p)
		return screenText(t, s, 24, 80)
	}
	// values is the screen's fields as drawn, with changes made.
	values := func(changes ...string) map[string]string {
		out := storedPrefs(prefsOf(store, u.ID))
		for n := 1; n <= users.MaxClocks; n++ {
			for _, f := range []string{clockField(prefClockZoneField, n), clockField(prefClockNameField, n)} {
				if _, ok := out[f]; !ok {
					out[f] = ""
				}
			}
		}
		for i := 0; i < len(changes); i += 2 {
			out[changes[i]] = changes[i+1]
		}
		return out
	}
	enter := func(v map[string]string) (bool, string) {
		return p.handle(go3270.Response{AID: go3270.AIDEnter, Values: v}, store, u, nil, noLog)
	}

	// The usual clocks are filled in until others are chosen, and all ten
	// rows fit.
	rows := build()
	text := strings.Join(rows, "\n")
	for _, want := range []string{"Calendar clocks", "Up to 10", "Clock 1 ==========> America/Denver", "Denver", "Clock 10 =========>", "PF1=List zones"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(rows[prefClockFirstRow+3], "Asia/Tokyo") || !strings.Contains(rows[prefClockFirstRow+3], "Japan") {
		t.Errorf("fourth clock row: %q", rows[prefClockFirstRow+3])
	}
	if !strings.HasPrefix(rows[prefClockFirstRow+users.MaxClocks-1], " Clock 10") {
		t.Errorf("tenth clock row: %q", rows[prefClockFirstRow+users.MaxClocks-1])
	}

	// Saving them unchanged keeps none chosen.
	if back, said := enter(values()); !back || said != "Your preferences are unchanged." || prefsOf(store, u.ID).Clocks != nil {
		t.Errorf("unchanged: back %v, %q, %+v", back, said, prefsOf(store, u.ID).Clocks)
	}

	// A name with no zone, or a zone that does not exist, is refused,
	// and what was typed drawn again.
	if back, _ := enter(values(clockField(prefClockZoneField, 7), "", clockField(prefClockNameField, 7), "Home")); back || !strings.Contains(p.message, "Clock 7") {
		t.Errorf("name alone: back %v, %q", back, p.message)
	}
	if back, _ := enter(values(clockField(prefClockZoneField, 2), "Mars/Base")); back || !strings.Contains(p.message, `Clock 2: there is no time zone "Mars/Base"`) {
		t.Errorf("bad zone: back %v, %q", back, p.message)
	}
	if rows := build(); !strings.Contains(rows[prefClockFirstRow+1], "Mars/Base") {
		t.Errorf("bad zone not drawn again: %q", rows[prefClockFirstRow+1])
	}

	// Blank rows are dropped and the rest kept in order, names cut to fit.
	p = preferencesState{}
	v := values()
	for n := 1; n <= users.MaxClocks; n++ {
		v[clockField(prefClockZoneField, n)], v[clockField(prefClockNameField, n)] = "", ""
	}
	v[clockField(prefClockZoneField, 3)], v[clockField(prefClockNameField, 3)] = " Asia/Kolkata ", "Bengaluru office team"
	v[clockField(prefClockZoneField, 10)] = "UTC"
	if back, said := enter(v); !back || said != "Your preferences are saved." {
		t.Errorf("chosen: back %v, %q (%s)", back, said, p.message)
	}
	want := []users.Clock{{Zone: "Asia/Kolkata", Name: "Bengaluru offi"}, {Zone: "UTC"}}
	if got := prefsOf(store, u.ID).Clocks; !slices.Equal(got, want) {
		t.Errorf("saved %+v, want %+v", got, want)
	}
	if rows := build(); !strings.Contains(rows[prefClockFirstRow], "Asia/Kolkata") || !strings.Contains(rows[prefClockFirstRow+1], "UTC") || strings.Contains(rows[prefClockFirstRow+2], "/") {
		t.Errorf("chosen clocks not drawn:\n%s", strings.Join(rows, "\n"))
	}

	// All blank is none at all, kept as such.
	for n := 1; n <= users.MaxClocks; n++ {
		v[clockField(prefClockZoneField, n)], v[clockField(prefClockNameField, n)] = "", ""
	}
	enter(v)
	if got := prefsOf(store, u.ID).Clocks; got == nil || len(got) != 0 {
		t.Errorf("none: %#v", got)
	}
	if got := clocksOf(prefsOf(store, u.ID)); len(got) != 0 {
		t.Errorf("none shows %v", got)
	}
	// Typing the usual ones back is none chosen again.
	if _, said := enter(storedPrefs(users.Preferences{})); said != "Your preferences are saved." || prefsOf(store, u.ID).Clocks != nil {
		t.Errorf("back to the usual: %q, %#v", said, prefsOf(store, u.ID).Clocks)
	}

	// PF1 on a clock's zone lists them, at that clock's zone, and goes
	// back to it.
	v = values(clockField(prefClockZoneField, 4), "Europe/Paris")
	p.handle(go3270.Response{AID: go3270.AIDPF1, Row: prefClockFirstRow + 3, Col: prefLabelWidth + 2, Values: v}, store, u, nil, noLog)
	if !p.listing || p.zoneField != clockField(prefClockZoneField, 4) {
		t.Fatalf("PF1 on clock 4: listing %v, field %q", p.listing, p.zoneField)
	}
	if text := strings.Join(build(), "\n"); !strings.Contains(text, "Europe/Paris") {
		t.Errorf("list is not at clock 4's zone:\n%s", text)
	}
	p.handle(go3270.Response{AID: go3270.AIDPF3}, store, u, nil, noLog)
	s, crow, _ := buildPreferences(24, 80, now, u.Name, prefsOf(store, u.ID), &p)
	if crow != prefClockFirstRow+3 || !strings.Contains(screenText(t, s, 24, 80)[crow], "Europe/Paris") {
		t.Errorf("back on row %d", crow)
	}
	// PF1 on a clock's name does not.
	p = preferencesState{}
	p.handle(go3270.Response{AID: go3270.AIDPF1, Row: prefClockFirstRow, Col: prefClockNameCol + 1, Values: values()}, store, u, nil, noLog)
	if p.listing {
		t.Errorf("PF1 on a name listed the zones")
	}
}

// fakeGeocoder finds the places it has, counting the lookups.
type fakeGeocoder struct {
	places  map[string]weather.Place
	err     error
	lookups int
}

func (g *fakeGeocoder) Geocode(_ context.Context, location string) (weather.Place, error) {
	g.lookups++
	if g.err != nil {
		return weather.Place{}, g.err
	}
	if p, ok := g.places[location]; ok {
		return p, nil
	}
	return weather.Place{}, weather.ErrNotFound
}

func TestPreferencesLocation(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p preferencesState
	paris := weather.Place{Name: "Paris, Ile-de-France, France", Latitude: 48.85, Longitude: 2.35, Country: "FR"}
	geo := &fakeGeocoder{places: map[string]weather.Place{"Paris, France": paris}}
	var logged []string
	build := func() []string {
		s, _, _ := buildPreferences(24, 80, now, u.Name, prefsOf(store, u.ID), &p)
		return screenText(t, s, 24, 80)
	}
	enter := func(location string, g Geocoder) (bool, string) {
		v := storedPrefs(prefsOf(store, u.ID))
		v[prefLocationField] = location
		return p.handle(go3270.Response{AID: go3270.AIDEnter, Values: v}, store, u, g, func(f string, a ...any) {
			logged = append(logged, fmt.Sprintf(f, a...))
		})
	}

	// Denver is filled in until another is typed, and is the place.
	rows := build()
	if want := " Weather location => Denver, CO, USA"; !strings.HasPrefix(rows[prefLocationRow], want) || !strings.Contains(rows[prefLocationRow], "Blank for Denver, CO, USA") {
		t.Errorf("location row %q", rows[prefLocationRow])
	}
	if placeOf(prefsOf(store, u.ID)) != weather.Denver {
		t.Errorf("place %+v", placeOf(prefsOf(store, u.ID)))
	}
	// Saving it unchanged looks nothing up.
	if back, said := enter("denver, co, usa", geo); !back || said != "Your preferences are unchanged." || geo.lookups != 0 || prefsOf(store, u.ID).Location != nil {
		t.Errorf("unchanged: %v %q, %d lookups, %+v", back, said, geo.lookups, prefsOf(store, u.ID).Location)
	}

	// Another is looked up, and kept as typed and as found.
	if back, said := enter(" Paris, France ", geo); !back || said != "Your preferences are saved; the weather is for Paris, Ile-de-France, France." {
		t.Errorf("Paris: %v %q", back, said)
	}
	want := users.Location{Typed: "Paris, France", Name: paris.Name, Latitude: 48.85, Longitude: 2.35, Country: "FR"}
	if got := prefsOf(store, u.ID).Location; got == nil || *got != want {
		t.Errorf("saved %+v; want %+v", got, want)
	}
	if placeOf(prefsOf(store, u.ID)) != paris {
		t.Errorf("place %+v", placeOf(prefsOf(store, u.ID)))
	}
	if len(logged) != 1 || !strings.Contains(logged[0], `set weather location to "Paris, France"`) {
		t.Errorf("logged %q", logged)
	}
	if rows := build(); !strings.Contains(rows[prefLocationRow], "=> Paris, France") {
		t.Errorf("location row %q", rows[prefLocationRow])
	}
	// Typed the same again, it is not looked up again.
	if _, said := enter("PARIS, FRANCE", geo); said != "Your preferences are unchanged." || geo.lookups != 1 {
		t.Errorf("again: %q, %d lookups", said, geo.lookups)
	}

	// A place not found, or a failed lookup, is refused and drawn again;
	// nothing is saved.
	if back, _ := enter("Atlantis", geo); back || !p.isError || !strings.Contains(p.message, `No place "Atlantis" was found`) {
		t.Errorf("Atlantis: %v %q", back, p.message)
	}
	if rows := build(); !strings.Contains(rows[prefLocationRow], "=> Atlantis") {
		t.Errorf("not drawn again: %q", rows[prefLocationRow])
	}
	if back, _ := enter("Rome, Italy", &fakeGeocoder{err: errors.New("timed out")}); back || !strings.Contains(p.message, `Could not look up "Rome, Italy": timed out.`) {
		t.Errorf("failed: %v %q", back, p.message)
	}
	if back, _ := enter("Rome, Italy", nil); back || !strings.Contains(p.message, "not available") {
		t.Errorf("no weather: %v %q", back, p.message)
	}
	if got := prefsOf(store, u.ID).Location; got == nil || *got != want {
		t.Errorf("changed to %+v", got)
	}

	// Blank is Denver again, with nothing to look up.
	if _, said := enter("  ", nil); said != "Your preferences are saved; the weather is for Denver, Colorado, United States." || prefsOf(store, u.ID).Location != nil {
		t.Errorf("blank: %q, %+v", said, prefsOf(store, u.ID).Location)
	}
}

func TestPreferencesUnits(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p preferencesState
	var logged []string
	build := func() []string {
		s, _, _ := buildPreferences(24, 80, now, u.Name, prefsOf(store, u.ID), &p)
		return screenText(t, s, 24, 80)
	}
	enter := func(units string) (bool, string) {
		v := storedPrefs(prefsOf(store, u.ID))
		v[prefUnitsField] = units
		return p.handle(go3270.Response{AID: go3270.AIDEnter, Values: v}, store, u, nil, func(f string, a ...any) {
			logged = append(logged, fmt.Sprintf(f, a...))
		})
	}

	// Blank until chosen, under the location and above the time zone.
	rows := build()
	if want := " Weather units ====>   F: °F, mph. C: °C, km/h. Blank: F in the US, else C."; rows[prefUnitsRow] != want {
		t.Errorf("units row\n%q\nwant\n%q", rows[prefUnitsRow], want)
	}
	if !strings.HasPrefix(rows[prefZoneRow], " Time zone") || !strings.HasPrefix(rows[prefLocationRow], " Weather location") {
		t.Errorf("rows around it:\n%s", strings.Join(rows, "\n"))
	}

	// Either, in either case, is saved; blank goes back to the usual.
	if back, said := enter(" c "); !back || said != "Your preferences are saved." || prefsOf(store, u.ID).Units != "C" {
		t.Errorf("C: %v %q, %q", back, said, prefsOf(store, u.ID).Units)
	}
	if !strings.Contains(build()[prefUnitsRow], "===> C") {
		t.Errorf("saved C not shown: %q", build()[prefUnitsRow])
	}
	if len(logged) != 1 || logged[0] != `"admin" set weather units to "C"` {
		t.Errorf("logged %q", logged)
	}
	if _, said := enter("C"); said != "Your preferences are unchanged." {
		t.Errorf("same again: %q", said)
	}
	if _, said := enter("F"); said != "Your preferences are saved." || prefsOf(store, u.ID).Units != "F" {
		t.Errorf("F: %q, %q", said, prefsOf(store, u.ID).Units)
	}
	if _, said := enter(""); said != "Your preferences are saved." || prefsOf(store, u.ID).Units != "" {
		t.Errorf("blank: %q, %q", said, prefsOf(store, u.ID).Units)
	}

	// Anything else is refused, and drawn again.
	if back, _ := enter("k"); back || !p.isError || !strings.Contains(p.message, `not "K"`) {
		t.Errorf("K: %v %q", back, p.message)
	}
	if !strings.Contains(build()[prefUnitsRow], "===> k") {
		t.Errorf("not drawn again: %q", build()[prefUnitsRow])
	}
}
