// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// fakePurpleAirCheck reads sensor 123 with the API key "good", and read
// key "rk" if private, or fails to be asked at all with err.
type fakePurpleAirCheck struct {
	private bool
	err     error
	checks  int
}

func (f *fakePurpleAirCheck) Check(_ context.Context, s weather.PurpleAirSensor) (weather.PurpleAirReading, error) {
	f.checks++
	switch {
	case f.err != nil:
		return weather.PurpleAirReading{}, f.err
	case s.APIKey != "good":
		return weather.PurpleAirReading{}, fmt.Errorf("purpleair: %w: The provided api_key was not valid.", weather.ErrBadKey)
	case s.Index != 123 || (f.private && s.ReadKey == ""):
		return weather.PurpleAirReading{}, fmt.Errorf("purpleair: %w: Cannot find a sensor.", weather.ErrNoSensor)
	case f.private && s.ReadKey != "rk":
		return weather.PurpleAirReading{}, fmt.Errorf("purpleair: %w: An invalid read_key.", weather.ErrBadReadKey)
	}
	return weather.PurpleAirReading{Name: "Back yard", AQI: 44, Time: now}, nil
}

func TestPurpleAirScreen(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	u := &list[0]
	var p purpleAirState
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	check := &fakePurpleAirCheck{}
	key := func(aid go3270.AID, apiKey, sensor, readKey string) bool {
		return p.handle(go3270.Response{AID: aid, Values: map[string]string{paKeyField: apiKey, paSensorField: sensor, paReadKeyField: readKey}}, store, u, check, logf)
	}
	build := func() []string {
		var current *users.PurpleAir
		if s, ok := purpleAirOf(store, u.ID); ok {
			current = &users.PurpleAir{APIKey: s.APIKey, Sensor: s.Index, ReadKey: s.ReadKey}
		}
		s, crow, ccol := buildPurpleAir(24, 80, now, u.Name, current, &p)
		if crow != paFirstRow || ccol != paLabelWidth {
			t.Errorf("cursor at %d,%d", crow, ccol)
		}
		return screenText(t, s, 24, 80)
	}
	stored := func() (weather.PurpleAirSensor, bool) { return purpleAirOf(store, u.ID) }

	rows := build()
	text := strings.Join(rows, "\n")
	for _, want := range []string{"PURPLEAIR", "Your PurpleAir sensor (admin) none set", "develop.purpleair.com", "select=12345", "Blank them all to remove it.", "PF3=Back Enter=Save"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	for i, want := range []string{" API key ===>", " Sensor ====>             its index, a number", " Read key ==>                                   for a private sensor only"} {
		if rows[paFirstRow+i] != want {
			t.Errorf("row %d\n%q\nwant\n%q", paFirstRow+i, rows[paFirstRow+i], want)
		}
	}

	// What will not do is refused, unasked, and drawn again.
	for _, c := range []struct{ apiKey, sensor, readKey, why string }{
		{"", "123", "", "API key too"},
		{"good", "", "", "a number"},
		{"good", "twelve", "", `not "twelve"`},
		{"good", "-4", "", "a number"},
		{"two words", "123", "", "no spaces"},
		{"good", "123", strings.Repeat("x", 65), "at most 64"},
	} {
		if key(go3270.AIDEnter, c.apiKey, c.sensor, c.readKey) || !p.isError || !strings.Contains(p.message, c.why) || check.checks != 0 {
			t.Errorf("%+v: %q, %d checks", c, p.message, check.checks)
		}
	}
	if rows := build(); !strings.Contains(rows[paFirstRow+2], strings.Repeat("x", 32)) {
		t.Errorf("not drawn again: %q", rows[paFirstRow+2])
	}

	// Refusals are not kept.
	if key(go3270.AIDEnter, "bad", "123", ""); !strings.Contains(p.message, "PurpleAir refused the API key: The provided api_key was not valid.") {
		t.Errorf("bad key: %q", p.message)
	}
	if key(go3270.AIDEnter, "good", "999", ""); p.message != "PurpleAir has no sensor 999; a private one needs its read key." {
		t.Errorf("no sensor: %q", p.message)
	}
	check.private = true
	if key(go3270.AIDEnter, "good", "123", "wrong"); p.message != "PurpleAir refused the read key for sensor 123." {
		t.Errorf("bad read key: %q", p.message)
	}
	if _, ok := stored(); ok {
		t.Error("a refused sensor was kept")
	}

	// One it reads is kept, saying what it reads; the keys are not logged.
	if key(go3270.AIDEnter, " good ", " 123 ", " rk ") || p.isError || p.message != "Saved: Back yard, now 44 Good." {
		t.Errorf("good: %q", p.message)
	}
	if s, ok := stored(); !ok || s != (weather.PurpleAirSensor{APIKey: "good", Index: 123, ReadKey: "rk"}) {
		t.Errorf("stored %+v, %v", s, ok)
	}
	if len(logged) != 1 || logged[0] != `"admin" set their PurpleAir sensor to 123` {
		t.Errorf("logged %q", logged)
	}
	if rows := build(); !strings.Contains(rows[paFirstRow], "good") || !strings.Contains(rows[paFirstRow+1], "123") || strings.Contains(rows[2], "none set") {
		t.Errorf("set:\n%s", strings.Join(rows, "\n"))
	}
	if st := settingsStatus(store, u.ID); st["purpleair"] != "sensor 123" {
		t.Errorf("settings status %q", st["purpleair"])
	}
	// The same again is not asked about.
	checks := check.checks
	if key(go3270.AIDEnter, "good", "123", "rk"); check.checks != checks || p.message != "" {
		t.Errorf("same: %d checks, %q", check.checks-checks, p.message)
	}

	// One that cannot be asked about, or whose reading will not do, is kept,
	// saying so.
	check.err = errors.New("purpleair: dial tcp: no route to host")
	if key(go3270.AIDEnter, "good", "124", ""); p.isError || !strings.Contains(p.message, "could not be asked") {
		t.Errorf("unchecked: %q", p.message)
	}
	check.err = fmt.Errorf("purpleair: %w: no humidity, which the EPA's correction needs", weather.ErrNoReading)
	if key(go3270.AIDEnter, "good", "125", ""); p.isError || p.message != "Saved, but its reading will not do: no humidity, which the EPA's correction needs" {
		t.Errorf("no reading: %q", p.message)
	}
	if s, _ := stored(); s.Index != 125 {
		t.Errorf("not kept: %+v", s)
	}
	check.err = nil

	// PF3 with a change asks first; a second leaves.
	if key(go3270.AIDPF3, "good", "999", "") || !p.leaveArmed {
		t.Error("first PF3 left")
	}
	if !key(go3270.AIDPF3, "good", "999", "") {
		t.Error("second PF3 did not leave")
	}
	if !key(go3270.AIDPF3, "good", "125", "") {
		t.Error("PF3 with nothing changed did not leave")
	}

	// Blank removes it.
	if key(go3270.AIDEnter, "", "", ""); !strings.Contains(p.message, "Removed your PurpleAir sensor") {
		t.Errorf("removed: %q", p.message)
	}
	if _, ok := stored(); ok {
		t.Error("still kept")
	}
	for _, l := range logged {
		if strings.Contains(l, "good") || strings.Contains(l, "rk") {
			t.Errorf("a key logged: %q", l)
		}
	}
}

func TestSettingsPurpleAirOption(t *testing.T) {
	if cmd, _, _ := settingsChoice(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{settingsOptionField: "6"}}); cmd != "purpleair" {
		t.Errorf("6: %q", cmd)
	}
	s, _, _ := buildSettings(24, 80, now, "admin", map[string]string{"purpleair": "sensor 123"}, "", false)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "  6 PurpleAir: your own sensor, for the air quality now (sensor 123)") {
		t.Errorf("settings lacks option 6:\n%s", text)
	}
}

func TestAQINowPurpleAir(t *testing.T) {
	hours := []weather.Hour{{Time: now, AQI: 39, HasAQI: true}}
	airNow := weather.Snapshot{Hours: hours, Observed: &weather.Observation{AQI: 61, Area: "Denver", Time: now}}
	reading := &weather.PurpleAirReading{Name: "Back yard", AQI: 72, Time: now.Add(-5 * time.Minute)}
	pa := &weather.PurpleAirSnapshot{Reading: reading, Fetched: now}

	for _, c := range []struct {
		name   string
		snap   weather.Snapshot
		pa     *weather.PurpleAirSnapshot
		at     time.Time
		aqi    int
		source string
	}{
		{"PurpleAir first", airNow, pa, now, 72, "measured 09:55 by your PurpleAir, Back yard"},
		{"then AirNow", airNow, nil, now, 61, "measured 10:00 by AirNow, Denver"},
		{"PurpleAir stale", airNow, pa, now.Add(55 * time.Minute), 61, "measured 10:00 by AirNow, Denver"},
		{"PurpleAir none read", airNow, &weather.PurpleAirSnapshot{Loading: true}, now, 61, "measured 10:00 by AirNow, Denver"},
		{"then the forecast", weather.Snapshot{Hours: hours}, nil, now, 39, "forecast by Open-Meteo"},
	} {
		aqi, source, ok := aqiNow(c.snap, c.pa, c.at)
		if !ok || aqi != c.aqi || source != c.source {
			t.Errorf("%s: %d %q %v; want %d %q", c.name, aqi, source, ok, c.aqi, c.source)
		}
	}
	indoor := *reading
	indoor.Indoor = true
	if _, source, _ := aqiNow(airNow, &weather.PurpleAirSnapshot{Reading: &indoor}, now); source != "measured 09:55 indoors by your PurpleAir, Back yard" {
		t.Errorf("indoor: %q", source)
	}
}

func TestWeatherScreenPurpleAir(t *testing.T) {
	snap := forecast()
	for i := range snap.Hours {
		snap.Hours[i].AQI, snap.Hours[i].HasAQI = 40, true
	}
	pa := &weather.PurpleAirSnapshot{Reading: &weather.PurpleAirReading{Name: "Back yard", AQI: 72, Time: now.Add(-5 * time.Minute)}, Fetched: now}
	build := func() []string {
		s, _, _ := buildWeather(24, 80, now, weather.Denver, true, snap, pa)
		return screenText(t, s, 24, 80)
	}
	if rows := build(); rows[weatherAQIRow] != " Air quality now: 72 Moderate, measured 09:55 by your PurpleAir, Back yard" || rows[22] != " "+weatherPrompt {
		t.Errorf("now %q, message %q", rows[weatherAQIRow], rows[22])
	}
	// Gone quiet, it says so, the forecast's shown in its place.
	pa.Reading.Time = now.Add(-2 * time.Hour)
	if rows := build(); rows[weatherAQIRow] != " Air quality now: 40 Good, forecast by Open-Meteo" || rows[22] != " Your PurpleAir sensor has not reported since Sun 08:00." {
		t.Errorf("stale: %q, %q", rows[weatherAQIRow], rows[22])
	}
	pa.Err = fmt.Errorf("purpleair: %w: The provided api_key was not valid.", weather.ErrBadKey)
	if rows := build(); rows[22] != " PurpleAir refused your API key; change it in SETTINGS, option 6." {
		t.Errorf("refused: %q", rows[22])
	}
	pa.Err = errors.New("purpleair: 500 Internal Server Error")
	if rows := build(); rows[22] != " Could not read your PurpleAir sensor: purpleair: 500 Internal Server Error" {
		t.Errorf("failed: %q", rows[22])
	}
}

func TestGatherPurpleAir(t *testing.T) {
	start := time.Now().Truncate(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sensors/123":
			_, _ = fmt.Fprintf(w, `{"sensor":{"name":"Back yard","last_seen":%d,"humidity":40,"pm2.5_cf_1_a":30,"pm2.5_cf_1_b":30,"channel_flags":0}}`, time.Now().Unix())
		case "/air-quality":
			_, _ = fmt.Fprintf(w, `{"hourly":{"time":[%d],"us_aqi":[20]}}`, start.Unix())
		default:
			_, _ = fmt.Fprintf(w, `{"hourly":{"time":[%d],"temperature_2m":[20],"precipitation_probability":[0],"weather_code":[0],"wind_speed_10m":[5]}}`, start.Unix())
		}
	}))
	defer srv.Close()
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(l *[]users.User, _ func() int) error {
		(*l)[0].PurpleAir = &users.PurpleAir{APIKey: "k", Sensor: 123}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client := &weather.Client{ForecastURL: srv.URL + "/forecast", AirQualityURL: srv.URL + "/air-quality", PurpleAirURL: srv.URL + "/sensors/"}
	cfg := Config{Users: store, Weather: weather.NewCache(client, nil), PurpleAir: weather.NewPurpleAirCache(client)}

	// 30 µg/m³ at 40% is 18.022 corrected, AQI 68: the sensor's, not the
	// forecast's 20.
	var v view
	for deadline := time.Now().Add(5 * time.Second); (v.Weather == nil || v.Weather.AQI != 68) && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		v = gather(cfg, start.Add(10*time.Minute), nil, nil, list[0].ID, nil)
	}
	if v.Weather == nil || v.Weather.AQI != 68 || !v.Weather.HasAQI {
		t.Errorf("weather %+v", v.Weather)
	}
}
