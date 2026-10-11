// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// geocodeBody is what Open-Meteo's geocoding finds for "Portland": the
// bigger one first.
const geocodeBody = `{"results":[
	{"name":"Portland","latitude":45.52,"longitude":-122.67,"country_code":"US","country":"United States","admin1":"Oregon","admin2":"Multnomah"},
	{"name":"Portland","latitude":43.66,"longitude":-70.25,"country_code":"US","country":"United States","admin1":"Maine","admin2":"Cumberland"},
	{"name":"Portland","latitude":-38.34,"longitude":141.6,"country_code":"AU","country":"Australia","admin1":"Victoria"}
]}`

func TestGeocode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Query().Get("name"), "Portland") {
			_, _ = w.Write([]byte(`{"generationtime_ms":0.5}`))
			return
		}
		_, _ = w.Write([]byte(geocodeBody))
	}))
	defer srv.Close()
	c := &Client{GeocodeURL: srv.URL}

	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"Portland", "Portland, Oregon, United States"},
		{" Portland , ME ", "Portland, Maine, United States"},
		{"portland, maine, usa", "Portland, Maine, United States"},
		{"Portland, Cumberland", "Portland, Maine, United States"},
		{"Portland, AU", "Portland, Victoria, Australia"},
		{"Portland, Victoria,", "Portland, Victoria, Australia"},
	} {
		p, err := c.Geocode(context.Background(), tc.typed)
		if err != nil || p.Name != tc.want {
			t.Errorf("%q: %q, %v; want %q", tc.typed, p.Name, err, tc.want)
		}
	}
	if p, _ := c.Geocode(context.Background(), "Portland, ME"); p.Latitude != 43.66 || p.Country != "US" || !p.Imperial() {
		t.Errorf("Maine: %+v", p)
	}
	if p, _ := c.Geocode(context.Background(), "Portland, Australia"); p.Imperial() {
		t.Errorf("Australia is imperial: %+v", p)
	}
	for _, typed := range []string{"Portland, TX", "Nowhere", "", " , CO"} {
		if _, err := c.Geocode(context.Background(), typed); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v, want not found", typed, err)
		}
	}
}

func TestGeocodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true,"reason":"Parameter count must be between 1 and 100"}`))
	}))
	defer srv.Close()
	c := &Client{GeocodeURL: srv.URL}
	if _, err := c.Geocode(context.Background(), "Denver"); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Errorf("err %v", err)
	}
}

// fakeOpenMeteo is Open-Meteo's forecast and air quality APIs, as a test
// server.
type fakeOpenMeteo struct {
	*httptest.Server

	// aqiDown fails the air quality, and aqiFirst is its first hour's
	// index.
	aqiDown  atomic.Bool
	aqiFirst atomic.Int32

	// airNow is AirNow's answer, "" for a failure; airNowReads counts
	// them, and airNowKey is the last key given.
	airNow      atomic.Value
	airNowReads atomic.Int32
	airNowKey   atomic.Value
}

// denverAirNow is AirNow's answer for Denver, in the form of its
// ziplatLong service: ozone and fine particles, the worse, for the hour
// ending 10:00 MST; coarse particles with none, and nitrogen dioxide less.
const denverAirNow = `[
	{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"MST","reportingAreaName":"Denver","siteID":"080310026","siteName":"La Casa","parameterName":"OZONE","nowcastAQI":35,"aqiCategoryName":"Good","reportingAgency":"Colorado Dept. of Public Health and Environment","lookupBehavior":"Closest Reading By Pollutant","consideredMonitors":"All","lookupBoundary":"50 Miles"},
	{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"MST","reportingAreaName":"Denver","siteID":"080310027","siteName":"I-25 Denver","parameterName":"PM2.5","nowcastAQI":61,"aqiCategoryName":"Moderate","reportingAgency":"Colorado Dept. of Public Health and Environment","lookupBehavior":"Closest Reading By Pollutant","consideredMonitors":"All","lookupBoundary":"50 Miles"},
	{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"MST","reportingAreaName":"Denver","parameterName":"PM10","nowcastAQI":null,"aqiCategoryName":"Unavailable"},
	{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"MST","reportingAreaName":"Denver","parameterName":"NO2","nowcastAQI":20,"aqiCategoryName":"Good"}
]`

// client is a client of f.
func (f *fakeOpenMeteo) client() *Client {
	return &Client{ForecastURL: f.URL + "/forecast", AirQualityURL: f.URL + "/air-quality", AirNowURL: f.URL + "/airnow"}
}

// forecastServer serves a forecast of three hours from start, the second
// missing its temperature, counting the forecasts asked for and keeping
// the last one's query; and their air quality, 30 to start with, then 120
// and none.
func forecastServer(t *testing.T, start time.Time, requests *atomic.Int32, query *atomic.Value) *fakeOpenMeteo {
	t.Helper()
	f := &fakeOpenMeteo{}
	f.aqiFirst.Store(30)
	f.airNow.Store(denverAirNow)
	s := start.Unix()
	times := strconv.FormatInt(s, 10) + `,` + strconv.FormatInt(s+3600, 10) + `,` + strconv.FormatInt(s+7200, 10)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/airnow" {
			f.airNowReads.Add(1)
			f.airNowKey.Store(r.URL.Query().Get("api_key"))
			body := f.airNow.Load().(string)
			if body == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"WebServiceError":[{"Message":"Invalid API key"}]}`))
				return
			}
			_, _ = w.Write([]byte(body))
			return
		}
		if r.URL.Path == "/air-quality" {
			if f.aqiDown.Load() {
				http.Error(w, `{"error":true,"reason":"air quality down"}`, http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(w, `{"hourly":{"time":[%s],"us_aqi":[%d,120,null]}}`, times, f.aqiFirst.Load())
			return
		}
		requests.Add(1)
		query.Store(r.URL.Query())
		_, _ = w.Write([]byte(`{"hourly":{"time":[` + times + `],` +
			`"temperature_2m":[60.5,null,58],"precipitation_probability":[0,5,40],` +
			`"weather_code":[0,2,61],"wind_speed_10m":[3.2,4,12.6]}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func TestForecast(t *testing.T) {
	start := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, start, &requests, &query)
	c := srv.client()

	hours, err := c.Forecast(context.Background(), Denver, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 2 {
		t.Fatalf("hours %+v; want the one missing its temperature dropped", hours)
	}
	want := Hour{Time: start.Add(2 * time.Hour), Temp: 58, Wind: 12.6, Precip: 40, Code: 61}
	if !hours[1].Time.Equal(want.Time) || hours[1].Temp != want.Temp || hours[1].Wind != want.Wind || hours[1].Precip != want.Precip || hours[1].Code != want.Code {
		t.Errorf("hour %+v; want %+v", hours[1], want)
	}
	q := query.Load().(url.Values)
	if q["temperature_unit"][0] != "fahrenheit" || q["wind_speed_unit"][0] != "mph" || q["latitude"][0] != "39.73915" {
		t.Errorf("Denver's query %v", q)
	}
	if _, err := c.Forecast(context.Background(), Denver, false); err != nil {
		t.Fatal(err)
	}
	if q := query.Load().(url.Values); q["temperature_unit"] != nil || q["wind_speed_unit"] != nil {
		t.Errorf("metric query %v; want Open-Meteo's metric default", q)
	}
}

func TestCache(t *testing.T) {
	start := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, start, &requests, &query)
	c := NewCache(srv.client(), nil)
	clock := start.Add(30 * time.Minute)
	c.now = func() time.Time { return clock }

	if s := c.Snapshot(Denver, true); !s.Loading || len(s.Hours) != 0 {
		t.Errorf("first: %+v", s)
	}
	c.fetches.Wait()
	s := c.Snapshot(Denver, true)
	if s.Loading || s.Err != nil || len(s.Hours) != 2 || !s.Fetched.Equal(clock) {
		t.Errorf("fetched: %+v", s)
	}
	if requests.Load() != 1 {
		t.Errorf("%d requests; want 1, the forecast reused", requests.Load())
	}
	// With the air quality of the hours that have one.
	if h := s.Hours; !h[0].HasAQI || h[0].AQI != 30 || h[1].HasAQI || s.AQIErr != nil {
		t.Errorf("air quality: %+v, %v", h, s.AQIErr)
	}
	// The hour under way is the first of the next; one past is not.
	if next := s.Next(clock, 24); len(next) != 2 || !next[0].Time.Equal(start) {
		t.Errorf("next at 15:30: %+v", next)
	}
	if next := s.Next(start.Add(2*time.Hour), 1); len(next) != 1 || !next[0].Time.Equal(start.Add(2*time.Hour)) {
		t.Errorf("next at 17:00: %+v", next)
	}

	// Once stale, it is read again, the old one shown meanwhile.
	clock = clock.Add(cacheTTL)
	if s := c.Snapshot(Denver, true); len(s.Hours) != 2 || s.Loading {
		t.Errorf("stale: %+v", s)
	}
	c.fetches.Wait()
	if requests.Load() != 2 {
		t.Errorf("%d requests; want 2", requests.Load())
	}

	// The air quality failing keeps the forecast new, and the air quality
	// read before, saying why.
	srv.aqiDown.Store(true)
	srv.aqiFirst.Store(99)
	clock = clock.Add(cacheTTL)
	c.Snapshot(Denver, true)
	c.fetches.Wait()
	if s := c.Snapshot(Denver, true); s.Err != nil || s.AQIErr == nil || !s.Fetched.Equal(clock) || s.Hours[0].AQI != 30 || !s.Hours[0].HasAQI {
		t.Errorf("air quality down: %+v", s)
	}
	srv.aqiDown.Store(false)
	clock = clock.Add(cacheTTL)
	c.Snapshot(Denver, true)
	c.fetches.Wait()
	if s := c.Snapshot(Denver, true); s.AQIErr != nil || s.Hours[0].AQI != 99 {
		t.Errorf("air quality back: %+v", s)
	}

	// A failed read keeps the forecast, saying why.
	srv.Close()
	clock = clock.Add(cacheTTL)
	c.Snapshot(Denver, true)
	c.fetches.Wait()
	if s := c.Snapshot(Denver, true); s.Err == nil || len(s.Hours) != 2 {
		t.Errorf("failed: %+v", s)
	}

	// A place unused for long is dropped.
	clock = clock.Add(cacheIdle + time.Minute)
	other := Place{Name: "Elsewhere", Country: "FR"}
	c.Snapshot(other, false)
	c.fetches.Wait()
	c.mu.Lock()
	_, kept := c.entries[cacheKey{Denver, true}]
	c.mu.Unlock()
	if kept {
		t.Error("Denver kept, unused")
	}
}

func TestCacheUnits(t *testing.T) {
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, time.Now().Truncate(time.Hour), &requests, &query)
	c := NewCache(srv.client(), nil)

	// The same place in the other units is a forecast of its own.
	c.Snapshot(Denver, true)
	c.fetches.Wait()
	if s := c.Snapshot(Denver, false); !s.Loading {
		t.Errorf("metric Denver: %+v; want it read afresh", s)
	}
	c.fetches.Wait()
	if q := query.Load().(url.Values); requests.Load() != 2 || q["temperature_unit"] != nil {
		t.Errorf("%d requests, last %v", requests.Load(), q)
	}
	if s := c.Snapshot(Denver, true); s.Loading || requests.Load() != 2 {
		t.Errorf("imperial Denver read again: %+v, %d requests", s, requests.Load())
	}
}

func TestAirQuality(t *testing.T) {
	start := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, start, &requests, &query)
	aqi, err := srv.client().AirQuality(context.Background(), Denver)
	if err != nil || len(aqi) != 2 || aqi[start.Unix()] != 30 || aqi[start.Add(time.Hour).Unix()] != 120 {
		t.Errorf("%v, %v", aqi, err)
	}
	srv.aqiDown.Store(true)
	if _, err := srv.client().AirQuality(context.Background(), Denver); err == nil || !strings.Contains(err.Error(), "air quality down") {
		t.Errorf("down: %v", err)
	}
}

func TestAirNow(t *testing.T) {
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, time.Now(), &requests, &query)
	c := srv.client()
	ctx := context.Background()

	// The worst pollutant, in the hour ending 10:00 MST, MST being UTC-7.
	obs, ok, err := c.AirNow(ctx, "SECRET-KEY", Denver)
	want := Observation{AQI: 61, Area: "Denver", Time: time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC)}
	if err != nil || !ok || obs.AQI != want.AQI || obs.Area != want.Area || !obs.Time.Equal(want.Time) {
		t.Errorf("%+v, %v, %v; want %+v", obs, ok, err, want)
	}
	if srv.airNowKey.Load() != "SECRET-KEY" {
		t.Errorf("key sent %v", srv.airNowKey.Load())
	}
	// A zone not known leaves the time unknown.
	srv.airNow.Store(`[{"dateObserved":"2026-10-11","hourObserved":"10:00","localTimeZone":"XYZ","reportingAreaName":"Somewhere","nowcastAQI":12}]`)
	if obs, ok, _ := c.AirNow(ctx, "k", Denver); !ok || !obs.Time.IsZero() || obs.Area != "Somewhere" {
		t.Errorf("unknown zone: %+v, %v", obs, ok)
	}
	// No monitor near is none.
	srv.airNow.Store(`[]`)
	if _, ok, err := c.AirNow(ctx, "k", Denver); ok || err != nil {
		t.Errorf("none: %v, %v", ok, err)
	}
	// A refused key says so, without the key.
	srv.airNow.Store("")
	_, _, err = c.AirNow(ctx, "SECRET-KEY", Denver)
	if !errors.Is(err, ErrBadKey) || !strings.Contains(err.Error(), "Invalid API key") || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("refused: %v", err)
	}
	// So too a failure to reach it, whose error would hold the URL.
	srv.Close()
	if _, _, err := c.AirNow(ctx, "SECRET-KEY", Denver); err == nil || strings.Contains(err.Error(), "SECRET") || !strings.HasPrefix(err.Error(), "airnow: ") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestCacheAirNow(t *testing.T) {
	// Observed from 08:00 MST, 15:00 UTC; the forecast from 18:00 UTC, its
	// first hour 30.
	start := time.Date(2026, 10, 11, 18, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	var query atomic.Value
	srv := forecastServer(t, start, &requests, &query)
	srv.airNow.Store(strings.ReplaceAll(denverAirNow, `"hourObserved":"10:00"`, `"hourObserved":"09:00"`))
	key := "k"
	c := NewCache(srv.client(), func() string { return key })
	clock := start.Add(-30 * time.Minute)
	c.now = func() time.Time { return clock }
	read := func(p Place) Snapshot {
		c.Snapshot(p, true)
		c.fetches.Wait()
		return c.Snapshot(p, true)
	}

	s := read(Denver)
	if s.Observed == nil || s.Observed.AQI != 61 || s.ObservedErr != nil {
		t.Fatalf("observed %+v, %v", s.Observed, s.ObservedErr)
	}
	// Measured stands for now for observedFor after its hour began, then
	// the forecast for the hour under way does.
	if aqi, obs, ok := s.AQINow(clock); aqi != 61 || obs == nil || !ok {
		t.Errorf("now: %d, %+v, %v", aqi, obs, ok)
	}
	if aqi, obs, ok := s.AQINow(observed.Add(observedFor)); aqi != 30 || obs != nil || !ok {
		t.Errorf("observation old: %d, %+v, %v; want the forecast's", aqi, obs, ok)
	}

	// A failed read keeps the one read before, saying why.
	srv.airNow.Store("")
	clock = clock.Add(cacheTTL)
	if s := read(Denver); s.Observed == nil || s.Observed.AQI != 61 || !errors.Is(s.ObservedErr, ErrBadKey) {
		t.Errorf("refused: %+v, %v", s.Observed, s.ObservedErr)
	}

	// Outside AirNow's countries, or without a key, AirNow is not asked.
	srv.airNow.Store(denverAirNow)
	reads := srv.airNowReads.Load()
	if s := read(Place{Name: "Paris", Country: "FR"}); s.Observed != nil || srv.airNowReads.Load() != reads {
		t.Errorf("Paris: %+v, %d reads", s.Observed, srv.airNowReads.Load()-reads)
	}
	key = ""
	clock = clock.Add(cacheTTL)
	if s := read(Denver); s.Observed != nil || srv.airNowReads.Load() != reads {
		t.Errorf("no key: %+v, %d reads", s.Observed, srv.airNowReads.Load()-reads)
	}
	if aqi, obs, ok := (Snapshot{}).AQINow(clock); ok || obs != nil || aqi != 0 {
		t.Errorf("none: %d, %+v, %v", aqi, obs, ok)
	}
}

func TestAirNowHour(t *testing.T) {
	for _, c := range []struct {
		date, hour, zone string
		want             time.Time
	}{
		{"2026-09-09", "17:00", "PDT", time.Date(2026, 9, 9, 23, 0, 0, 0, time.UTC)},   // 16:00 PDT
		{" 2026-01-10 ", "01:00", "EST", time.Date(2026, 1, 10, 5, 0, 0, 0, time.UTC)}, // midnight EST
		{"2026-01-10", "00:00", "EST", time.Date(2026, 1, 10, 4, 0, 0, 0, time.UTC)},   // 23:00 EST the day before
		{"2026-01-10", "17:00", "XYZ", time.Time{}},
		{"2026-01-10", "17:30", "EST", time.Time{}},
		{"2026-01-10", "5 PM", "EST", time.Time{}},
		{"Jan 10", "17:00", "EST", time.Time{}},
	} {
		if got := airNowHour(c.date, c.hour, c.zone); !got.Equal(c.want) {
			t.Errorf("%q %q %q: %v, want %v", c.date, c.hour, c.zone, got, c.want)
		}
	}
}

func TestAQICategory(t *testing.T) {
	for aqi, want := range map[int]string{
		0: "Good", 50: "Good", 51: "Moderate", 100: "Moderate", 101: "Unhealthy (sens.)", 150: "Unhealthy (sens.)",
		151: "Unhealthy", 200: "Unhealthy", 201: "Very unhealthy", 300: "Very unhealthy", 301: "Hazardous", 500: "Hazardous",
	} {
		if got := AQICategory(aqi).String(); got != want {
			t.Errorf("%d: %q, want %q", aqi, got, want)
		}
	}
}

func TestWorst(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 11, h, 0, 0, 0, time.UTC) }
	hours := []Hour{{Time: at(1), AQI: 40, HasAQI: true}, {Time: at(2), AQI: 99}, {Time: at(3), AQI: 60, HasAQI: true}, {Time: at(4), AQI: 60, HasAQI: true}}
	if w, ok := Worst(hours); !ok || !w.Time.Equal(at(3)) {
		t.Errorf("worst %+v, %v; want the first 60, the 99 having none", w, ok)
	}
	if _, ok := Worst(hours[1:2]); ok {
		t.Error("worst of none")
	}
}

func TestDescribe(t *testing.T) {
	for _, code := range []int{0, 1, 2, 3, 45, 48, 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 71, 73, 75, 77, 80, 81, 82, 85, 86, 95, 96, 99} {
		if d := Describe(code); strings.HasPrefix(d, "Code") || len(d) > DescribeWidth {
			t.Errorf("%d: %q", code, d)
		}
	}
	if d := Describe(42); d != "Code 42" {
		t.Errorf("42: %q", d)
	}
}
