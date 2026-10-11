// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// forecast is 26 hours from the hour before now, the temperature falling
// by a degree an hour from 3.4, and the codes taking turns.
func forecast() weather.Snapshot {
	snap := weather.Snapshot{Fetched: now.Add(-5 * time.Minute)}
	codes := []int{0, 2, 71, 96}
	for i := range 26 {
		snap.Hours = append(snap.Hours, weather.Hour{
			Time: now.Add(time.Duration(i-1) * time.Hour), Temp: 3.4 - float64(i), Wind: 12.6, Precip: 10 * (i % 11), Code: codes[i%len(codes)],
		})
	}
	return snap
}

func TestWeatherScreen(t *testing.T) {
	denver := time.FixedZone("MDT", -6*3600)
	at := now.Add(30 * time.Minute).In(denver) // 04:30 there
	s, crow, ccol := buildWeather(24, 80, at, weather.Denver, true, forecast())
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	if crow != 23 || ccol != 0 {
		t.Errorf("cursor at %d,%d", crow, ccol)
	}
	for _, want := range []string{"WEATHER", "Weather for Denver, Colorado, United States (as of 03:55)", "Open-Meteo.com", "PF3=Back"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	// Two columns of twelve hours, from the hour under way, in the user's
	// time zone.
	head := " Time        °F Prec  mph Sky"
	if !strings.HasPrefix(rows[weatherHeadRow], head) || !strings.Contains(rows[weatherHeadRow][41:], head) {
		t.Errorf("headings %q", rows[weatherHeadRow])
	}
	for _, c := range []struct {
		row  int
		want string
	}{
		{weatherFirstRow, " Sun 04:00   2°  10%   13 Partly cloudy   Sun 16:00 -10°  20%   13 Partly cloudy"},
		{weatherFirstRow + 1, " Sun 05:00   1°  20%   13 Light snow      Sun 17:00 -11°  30%   13 Light snow"},
		{weatherFirstRow + 2, " Sun 06:00   0°  30%   13 T-storm, hail   Sun 18:00 -12°  40%   13 T-storm, hail"},
		{weatherFirstRow + 11, " Sun 15:00  -9°  10%   13 Clear           Mon 03:00 -21°  20%   13 Clear"},
	} {
		if rows[c.row] != c.want {
			t.Errorf("row %d:\n%q\nwant\n%q", c.row, rows[c.row], c.want)
		}
	}
	if strings.Contains(text, "Mon 04:00") || strings.Contains(text, "Sun 03:00") {
		t.Errorf("more than the next 24 hours:\n%s", text)
	}

	// Each underlined heading, its sky the widest sky's width, ends just
	// after its text, as a field is underlined to the next field's
	// attribute byte: the second's, at the end of the row, is the next
	// row's first.
	headings := 0
	for _, f := range s {
		if f.Highlighting != go3270.Underscore {
			continue
		}
		headings++
		if !strings.HasSuffix(f.Content, "Sky          ") {
			t.Errorf("heading %q: Sky not 13 wide", f.Content)
		}
		end := f.Row*80 + f.Col + 1 + utf8.RuneCountInString(f.Content)
		if !slices.ContainsFunc(s, func(g go3270.Field) bool { return g.Row*80+g.Col == end }) {
			t.Errorf("heading at %d,%d %q runs on past its text", f.Row, f.Col, f.Content)
		}
	}
	if headings != 2 {
		t.Errorf("%d underlined headings; want 2", headings)
	}

	// Metric is in °C and km/h.
	paris := weather.Place{Name: "Paris, Ile-de-France, France", Country: "FR"}
	s, _, _ = buildWeather(24, 80, at, paris, false, forecast())
	if rows := screenText(t, s, 24, 80); !strings.Contains(rows[weatherHeadRow], "°C Prec km/h Sky") || !strings.Contains(rows[2], "Paris") {
		t.Errorf("Paris:\n%s", strings.Join(rows, "\n"))
	}
}

func TestWeatherScreenAQI(t *testing.T) {
	snap := forecast()
	for i := range snap.Hours {
		snap.Hours[i].AQI, snap.Hours[i].HasAQI = 40+i, true
	}
	build := func() []string {
		s, _, _ := buildWeather(24, 80, now, weather.Denver, true, snap)
		return screenText(t, s, 24, 80)
	}
	rows := build()
	if want := " Air quality now: 41 Good, forecast by Open-Meteo"; rows[weatherAQIRow] != want {
		t.Errorf("now row\n%q\nwant\n%q", rows[weatherAQIRow], want)
	}
	if want := " Worst forecast, next 24 hours: 64 Moderate, Mon 09:00"; rows[weatherAQIRow+1] != want {
		t.Errorf("worst row\n%q\nwant\n%q", rows[weatherAQIRow+1], want)
	}
	if !strings.HasPrefix(rows[weatherHeadRow], " Time") || rows[weatherAQIRow+2] != "" {
		t.Errorf("below:\n%s", strings.Join(rows, "\n"))
	}
	if rows[22] != " "+weatherPrompt {
		t.Errorf("message row %q", rows[22])
	}

	// Failures say why on the message row, still showing what was read
	// before: the forecast's own first, then AirNow's, then the air
	// quality forecast's.
	snap.AQIErr = errors.New("timed out")
	if rows := build(); rows[22] != " Could not read the air quality: timed out" || !strings.Contains(rows[weatherAQIRow], "41 Good") {
		t.Errorf("air quality failed: %q", rows[22])
	}
	snap.ObservedErr = errors.New("airnow: 500 Internal Server Error")
	if rows := build(); rows[22] != " Could not read AirNow: airnow: 500 Internal Server Error" {
		t.Errorf("AirNow failed: %q", rows[22])
	}
	snap.ObservedErr = fmt.Errorf("airnow: %w: Invalid API key", weather.ErrBadKey)
	if rows := build(); rows[22] != " AirNow refused its API key; an admin can change it (ADMIN, option 11)." {
		t.Errorf("AirNow refused: %q", rows[22])
	}
	snap.Err = errors.New("refused")
	if rows := build(); rows[22] != " Could not read it again: refused" {
		t.Errorf("forecast failed: %q", rows[22])
	}
}

func TestWeatherScreenStates(t *testing.T) {
	stale := forecast()
	stale.Err = errors.New("connection refused")
	for _, c := range []struct {
		name string
		snap weather.Snapshot
		want string
	}{
		{"loading", weather.Snapshot{Loading: true}, "(reading the forecast...)"},
		{"failed", weather.Snapshot{Err: errors.New("connection refused")}, "\n Could not read the forecast: connection refused\n"},
		{"stale", stale, "\n Could not read it again: connection refused\n"},
		{"stale", stale, "Sun 10:00"},
		{"empty", weather.Snapshot{Fetched: now}, "The forecast has no hours from now."},
	} {
		s, _, _ := buildWeather(24, 80, now, weather.Denver, true, c.snap)
		if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, c.want) {
			t.Errorf("%s: lacks %q:\n%s", c.name, c.want, text)
		}
	}
}

func TestPlaceOf(t *testing.T) {
	if p := placeOf(users.Preferences{}); p != weather.Denver {
		t.Errorf("none: %+v", p)
	}
	l := &users.Location{Typed: "Paris", Name: "Paris, France", Latitude: 48.85, Longitude: 2.35, Country: "FR"}
	if p := placeOf(users.Preferences{Location: l}); p != (weather.Place{Name: "Paris, France", Latitude: 48.85, Longitude: 2.35, Country: "FR"}) {
		t.Errorf("Paris: %+v", p)
	}
}

func TestImperialFor(t *testing.T) {
	paris := &users.Location{Typed: "Paris", Name: "Paris, France", Country: "FR"}
	for _, c := range []struct {
		prefs users.Preferences
		want  bool
	}{
		{users.Preferences{}, true}, // Denver
		{users.Preferences{Location: paris}, false},
		{users.Preferences{Units: "C"}, false},
		{users.Preferences{Location: paris, Units: "F"}, true},
	} {
		if got := imperialFor(c.prefs); got != c.want {
			t.Errorf("%+v: %v", c.prefs, got)
		}
	}
}

func TestWholeNumber(t *testing.T) {
	for x, want := range map[float64]string{-0.4: "0", 0.5: "1", -0.5: "-1", 72.49: "72", -12.6: "-13"} {
		if got := wholeNumber(x); got != want {
			t.Errorf("%v: %q, want %q", x, got, want)
		}
	}
}

func TestTitleWeather(t *testing.T) {
	v := sampleView(3)
	v.Weather, v.Imperial = &weather.Hour{Time: now, Temp: 71.6, Precip: 10, Code: 2}, true
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	title := screenText(t, s, 24, 80)[titleRow]
	// Two spaces before the clock, which, on the 27th, is the longer by a
	// column.
	want := " EXECUTIVE FUNCTION DASHBOARD       72°F 10% Partly cloudy  Sun Sep 27 10:00:00"
	if title != want {
		t.Errorf("title row\n%q\nwant\n%q", title, want)
	}
	// Blue, dimmer than the title and clock, which are intensified white.
	for _, f := range s {
		if f.Row != titleRow || f.Col == 79 { // the busy banner starts in the last column
			continue
		}
		weather := f.Content == "72°F 10% Partly cloudy"
		if wantColor := map[bool]go3270.Color{true: go3270.Blue, false: go3270.White}[weather]; f.Intense == weather || f.Color != wantColor {
			t.Errorf("title row field %q: intense %v, color %v", f.Content, f.Intense, f.Color)
		}
	}
	// Tinted, it is part of the one field.
	if title := screenText(t, tintTitle(s, 24, 80, go3270.Red), 24, 80)[titleRow]; !strings.Contains(title, "72°F 10% Partly cloudy") {
		t.Errorf("tinted %q", title)
	}

	// Metric, and cut to the room there is.
	const titleWidth = len("EXECUTIVE FUNCTION DASHBOARD")
	contents := func(fs []go3270.Field) string {
		var out []string
		for _, f := range fs {
			out = append(out, fmt.Sprintf("%d:%s", f.Col, f.Content))
		}
		return strings.Join(out, " | ")
	}
	v.Weather, v.Imperial = &weather.Hour{Time: now, Temp: -0.3, Precip: 100, Code: 96}, false
	if got := contents(titleWeather(80, titleWidth, v)); got != "35:0°C 100% T-storm, hail" {
		t.Errorf("metric: %q", got)
	}
	if got := contents(titleWeather(64, titleWidth, v)); got != "30:0°C 100% T>" {
		t.Errorf("64 columns: %q", got)
	}
	// With too little room, or no weather, there is none.
	if got := titleWeather(56, titleWidth, v); got != nil {
		t.Errorf("56 columns: %q", contents(got))
	}
	v.Weather = nil
	if got := titleWeather(80, titleWidth, v); got != nil {
		t.Errorf("no weather: %q", contents(got))
	}
}

func TestTitleAQI(t *testing.T) {
	const titleWidth = len("EXECUTIVE FUNCTION DASHBOARD")
	v := sampleView(3)
	v.Imperial = true
	at := func(aqi int, has bool) view {
		v.Weather = &weather.Hour{Time: now, Temp: 71.6, Precip: 10, Code: 2, AQI: aqi, HasAQI: has}
		return v
	}

	// At 50 and under, Good, or with none, there is no index.
	for _, w := range []view{at(0, true), at(49, true), at(50, true), at(200, false)} {
		if got := titleWeather(80, titleWidth, w); len(got) != 1 || got[0].Content != "72°F 10% Partly cloudy" {
			t.Errorf("AQI %d (%v): %+v", w.Weather.AQI, w.Weather.HasAQI, got)
		}
	}

	// Above 50, it follows the weather, in its category's color, the sky
	// cut to make room for it.
	s, _, _, _ := buildDashboard(24, 80, at(52, true), 0, "")
	want := " EXECUTIVE FUNCTION DASHBOARD  72°F 10% Partly clo> AQI 52  Sun Sep 27 10:00:00"
	if title := screenText(t, s, 24, 80)[titleRow]; title != want {
		t.Errorf("title row\n%q\nwant\n%q", title, want)
	}
	for _, c := range []struct {
		aqi     int
		color   go3270.Color
		intense bool
	}{
		{51, go3270.Yellow, false}, {52, go3270.Yellow, false}, {101, go3270.Red, false},
		{151, go3270.Red, true}, {201, go3270.Pink, false}, {301, go3270.Pink, true},
	} {
		got := titleWeather(80, titleWidth, at(c.aqi, true))
		if len(got) != 2 || got[1].Content != fmt.Sprintf("AQI %d", c.aqi) || got[1].Color != c.color || got[1].Intense != c.intense || got[0].Color != go3270.Blue {
			t.Errorf("AQI %d: %+v", c.aqi, got)
		}
	}
	// Tinted, it is part of the one field.
	if title := screenText(t, tintTitle(s, 24, 80, go3270.Red), 24, 80)[titleRow]; !strings.Contains(title, "Partly clo> AQI 52  Sun") {
		t.Errorf("tinted %q", title)
	}

	// Short of room, the index is kept whole over the weather.
	if got := titleWeather(60, titleWidth, at(152, true)); len(got) != 1 || got[0].Content != "AQI 152" || got[0].Col != 30 {
		t.Errorf("60 columns: %+v", got)
	}
}

func TestAQILines(t *testing.T) {
	denver := time.FixedZone("MDT", -6*3600)
	hours := []weather.Hour{
		{Time: now, AQI: 39, HasAQI: true},
		{Time: now.Add(time.Hour), AQI: 112, HasAQI: true},
		{Time: now.Add(2 * time.Hour), AQI: 58, HasAQI: true},
	}
	text := func(l line) string {
		var out []string
		for _, f := range l {
			out = append(out, f.Content)
		}
		return strings.Join(out, " ")
	}
	at := now.In(denver)

	// Forecast, with no measurement.
	current, worst := aqiLines(weather.Snapshot{Hours: hours}, hours, at)
	if got := text(current); got != "Air quality now: 39 Good, forecast by Open-Meteo" {
		t.Errorf("now %q", got)
	}
	if got := text(worst); got != "Worst forecast, next 24 hours: 112 Unhealthy (sens.), Sun 05:00" {
		t.Errorf("worst %q", got)
	}
	if current[1].Color != go3270.Green || worst[1].Color != go3270.Red {
		t.Errorf("colors %v, %v", current[1].Color, worst[1].Color)
	}

	// Measured, by AirNow, in the user's time zone.
	obs := &weather.Observation{AQI: 61, Area: "Denver", Time: now.Add(-time.Hour)}
	current, _ = aqiLines(weather.Snapshot{Hours: hours, Observed: obs}, hours, at)
	if got := text(current); got != "Air quality now: 61 Moderate, measured 03:00 by AirNow, Denver" || current[1].Color != go3270.Yellow {
		t.Errorf("measured %q, %v", got, current[1].Color)
	}
	obs.Time = time.Time{}
	if current, _ = aqiLines(weather.Snapshot{Hours: hours, Observed: obs}, hours, at); text(current) != "Air quality now: 61 Moderate, measured by AirNow, Denver" {
		t.Errorf("time not known %q", text(current))
	}

	// With none at all, no lines.
	if current, worst := aqiLines(weather.Snapshot{}, []weather.Hour{{Time: now}}, now); current != nil || worst != nil {
		t.Errorf("none: %q, %q", text(current), text(worst))
	}
	// The longest worst fits in 80 columns; the measured one is cut at the
	// area.
	long := []weather.Hour{{Time: now, AQI: 150, HasAQI: true}}
	if _, worst := aqiLines(weather.Snapshot{}, long, now); lineWidth(worst) > 79 {
		t.Errorf("%d wide: %q", lineWidth(worst), text(worst))
	}
	obs = &weather.Observation{AQI: 150, Area: "Southern Wasatch Front, UT", Time: now}
	current, _ = aqiLines(weather.Snapshot{Observed: obs}, nil, now)
	if rows := screenText(t, placeLine(3, 80, current), 24, 80); rows[3] != " Air quality now: 150 Unhealthy (sens.), measured 10:00 by AirNow, Southern Was>" {
		t.Errorf("long %q", rows[3])
	}
}

func TestGatherWeather(t *testing.T) {
	start := time.Now().Truncate(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/air-quality" {
			_, _ = fmt.Fprintf(w, `{"hourly":{"time":[%d,%d],"us_aqi":[57,60]}}`, start.Unix(), start.Add(time.Hour).Unix())
			return
		}
		if r.URL.Path == "/airnow" {
			_, _ = w.Write([]byte(`[{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"XYZ","reportingAreaName":"Denver","parameterName":"PM2.5","nowcastAQI":77}]`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"hourly":{"time":[%d,%d],"temperature_2m":[20.4,22],"precipitation_probability":[30,0],"weather_code":[61,0],"wind_speed_10m":[5,6]}}`,
			start.Unix(), start.Add(time.Hour).Unix())
	}))
	defer srv.Close()
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	list, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(l *[]users.User, _ func() int) error {
		(*l)[0].Preferences.Units = "C"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client := &weather.Client{ForecastURL: srv.URL + "/forecast", AirQualityURL: srv.URL + "/air-quality", AirNowURL: srv.URL + "/airnow"}
	cfg := Config{Users: store, Weather: weather.NewCache(client, nil)}

	// None until the forecast is read, then the hour under way's.
	at := start.Add(10 * time.Minute)
	if v := gather(cfg, at, nil, nil, list[0].ID, nil); v.Weather != nil {
		t.Errorf("before reading: %+v", v.Weather)
	}
	var v view
	for deadline := time.Now().Add(5 * time.Second); v.Weather == nil && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		v = gather(cfg, at, nil, nil, list[0].ID, nil)
	}
	if v.Weather == nil || v.Weather.Temp != 20.4 || v.Weather.Code != 61 || v.Weather.AQI != 57 || !v.Weather.HasAQI || v.Imperial {
		t.Errorf("weather %+v, imperial %v", v.Weather, v.Imperial)
	}
	// With an AirNow key, the air quality measured is over the forecast's.
	cfg.Weather = weather.NewCache(client, func() string { return "k" })
	v = view{}
	for deadline := time.Now().Add(5 * time.Second); v.Weather == nil && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		v = gather(cfg, at, nil, nil, list[0].ID, nil)
	}
	if v.Weather == nil || v.Weather.AQI != 77 || !v.Weather.HasAQI || v.Weather.Temp != 20.4 {
		t.Errorf("measured: %+v", v.Weather)
	}
	// Without a weather cache, there is none.
	if v := gather(Config{Users: store}, at, nil, nil, list[0].ID, nil); v.Weather != nil {
		t.Errorf("no cache: %+v", v.Weather)
	}
}
