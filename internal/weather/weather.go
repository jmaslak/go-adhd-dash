// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package weather reads hourly forecasts, with the air quality, and finds
// the places they are for, from Open-Meteo (open-meteo.com), which needs
// no API key; and, given an AirNow API key, the air quality measured now
// in the United States, from AirNow (airnow.gov). A forecast is cached for
// each place, shared by every session looking at it, so that each does not
// call Open-Meteo or AirNow on its own.
package weather

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Open-Meteo's forecast, air quality and geocoding APIs.
const (
	ForecastURL   = "https://api.open-meteo.com/v1/forecast"
	AirQualityURL = "https://air-quality-api.open-meteo.com/v1/air-quality"
	GeocodeURL    = "https://geocoding-api.open-meteo.com/v1/search"
)

// AirNow's API of the air quality observed now near a point or ZIP code,
// which replaced observation/latLong/current/, retired on October 1, 2026.
const AirNowURL = "https://www.airnowapi.org/aq/observation/current/ziplatLong"

// Place is where a forecast is for.
type Place struct {
	// Name is the place as found, such as "Denver, Colorado, United
	// States".
	Name string

	Latitude, Longitude float64

	// Country is the place's ISO 3166 country code, such as "US", which
	// decides the forecast's units.
	Country string
}

// Imperial reports whether p's forecast is usually in degrees Fahrenheit
// and miles per hour, as in the United States, rather than degrees Celsius
// and kilometers per hour.
func (p Place) Imperial() bool { return p.Country == "US" }

// DefaultLocation is the location used until a user types another.
const DefaultLocation = "Denver, CO, USA"

// Denver is the place DefaultLocation finds.
var Denver = Place{Name: "Denver, Colorado, United States", Latitude: 39.73915, Longitude: -104.9847, Country: "US"}

// Hour is the forecast for one hour.
type Hour struct {
	Time time.Time

	// Temp is the temperature, and Wind the wind speed, in the units
	// asked for: degrees Fahrenheit and miles per hour, or degrees
	// Celsius and kilometers per hour.
	Temp float64
	Wind float64

	// Precip is the chance of rain or snow, in percent.
	Precip int

	// Code is the WMO weather code: what the sky is doing (see Describe).
	Code int

	// AQI is the US air quality index, if HasAQI: 0 to 500, as forecast
	// by Copernicus's atmosphere model, not measured (see AQICategory).
	AQI    int
	HasAQI bool
}

// Client reads from Open-Meteo.
type Client struct {
	// ForecastURL, AirQualityURL, GeocodeURL and AirNowURL are the APIs'
	// addresses; "" for Open-Meteo's and AirNow's own. Tests replace them.
	ForecastURL   string
	AirQualityURL string
	GeocodeURL    string
	AirNowURL     string

	// HTTP makes the requests; nil for one with a 30 second timeout.
	HTTP *http.Client
}

// forecastHours is how many hours a forecast holds: a day from the hour
// under way, with room to stay a day ahead while it is cached.
const forecastHours = 26

// get reads base with query into out, as JSON.
// ErrBadKey is returned, wrapped, when an API refuses the key it was
// given.
var ErrBadKey = errors.New("the API key was refused")

// get reads base with query from service, named in errors, into out, as
// JSON.
func (c *Client) get(ctx context.Context, service, base string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if ue, ok := errors.AsType[*url.Error](err); ok {
		// Without the request's URL, which is long, says nothing a user can
		// act on, and for AirNow holds the API key.
		return fmt.Errorf("%s: %w", service, ue.Err)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		// Open-Meteo says what is wrong as {"error": true, "reason": "..."},
		// AirNow as {"WebServiceError": [{"Message": "..."}]}.
		var e struct {
			Reason          string
			WebServiceError []struct{ Message string }
		}
		why := resp.Status
		if json.NewDecoder(resp.Body).Decode(&e) == nil {
			switch {
			case e.Reason != "":
				why = e.Reason
			case len(e.WebServiceError) > 0 && e.WebServiceError[0].Message != "":
				why = e.WebServiceError[0].Message
			}
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%s: %w: %s", service, ErrBadKey, why)
		}
		return fmt.Errorf("%s: %s", service, why)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Forecast reads the hourly forecast for p, from the hour under way, in
// degrees Fahrenheit and miles per hour if imperial, else in degrees
// Celsius and kilometers per hour.
func (c *Client) Forecast(ctx context.Context, p Place, imperial bool) ([]Hour, error) {
	q := url.Values{
		"latitude":       {strconv.FormatFloat(p.Latitude, 'f', -1, 64)},
		"longitude":      {strconv.FormatFloat(p.Longitude, 'f', -1, 64)},
		"hourly":         {"temperature_2m,precipitation_probability,weather_code,wind_speed_10m"},
		"timeformat":     {"unixtime"},
		"forecast_hours": {strconv.Itoa(forecastHours)},
	}
	if imperial {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	var body struct {
		Hourly struct {
			Time   []int64    `json:"time"`
			Temp   []*float64 `json:"temperature_2m"`
			Precip []*int     `json:"precipitation_probability"`
			Code   []*int     `json:"weather_code"`
			Wind   []*float64 `json:"wind_speed_10m"`
		} `json:"hourly"`
	}
	if err := c.get(ctx, "open-meteo", cmp.Or(c.ForecastURL, ForecastURL), q, &body); err != nil {
		return nil, err
	}
	h := body.Hourly
	n := len(h.Time)
	if len(h.Temp) != n || len(h.Precip) != n || len(h.Code) != n || len(h.Wind) != n {
		return nil, errors.New("open-meteo: the forecast's hours do not line up")
	}
	out := make([]Hour, 0, n)
	for i, t := range h.Time {
		// An hour missing any of its values is dropped, rather than shown
		// as zero.
		if h.Temp[i] == nil || h.Precip[i] == nil || h.Code[i] == nil || h.Wind[i] == nil {
			continue
		}
		out = append(out, Hour{Time: time.Unix(t, 0), Temp: *h.Temp[i], Precip: *h.Precip[i], Code: *h.Code[i], Wind: *h.Wind[i]})
	}
	return out, nil
}

// AirQuality reads the hourly US air quality index at p, from the hour
// under way, by the hour's start, as Unix time. Hours without one are left
// out.
func (c *Client) AirQuality(ctx context.Context, p Place) (map[int64]int, error) {
	q := url.Values{
		"latitude":       {strconv.FormatFloat(p.Latitude, 'f', -1, 64)},
		"longitude":      {strconv.FormatFloat(p.Longitude, 'f', -1, 64)},
		"hourly":         {"us_aqi"},
		"timeformat":     {"unixtime"},
		"forecast_hours": {strconv.Itoa(forecastHours)},
	}
	var body struct {
		Hourly struct {
			Time []int64 `json:"time"`
			AQI  []*int  `json:"us_aqi"`
		} `json:"hourly"`
	}
	if err := c.get(ctx, "open-meteo", cmp.Or(c.AirQualityURL, AirQualityURL), q, &body); err != nil {
		return nil, err
	}
	if len(body.Hourly.AQI) != len(body.Hourly.Time) {
		return nil, errors.New("open-meteo: the air quality's hours do not line up")
	}
	out := map[int64]int{}
	for i, t := range body.Hourly.Time {
		if a := body.Hourly.AQI[i]; a != nil {
			out[t] = *a
		}
	}
	return out, nil
}

// Observation is the air quality measured near a place, by AirNow.
type Observation struct {
	// AQI is the US air quality index, by the EPA's NowCast: the worst of
	// the pollutants measured.
	AQI int

	// Area is the AirNow reporting area it is for, such as "Denver".
	Area string

	// Time is the start of the hour observed; zero if its time zone is
	// not one known (see airNowZones).
	Time time.Time
}

// AirNowCovers reports whether AirNow measures the air quality at p: in
// the United States, Puerto Rico, Canada and Mexico.
func AirNowCovers(p Place) bool {
	return slices.Contains([]string{"US", "PR", "CA", "MX"}, p.Country)
}

// airNowZones are the offsets from UTC of the time zones AirNow says its
// observations' hours are in.
var airNowZones = map[string]int{
	"EST": -5, "EDT": -4, "CST": -6, "CDT": -5, "MST": -7, "MDT": -6, "PST": -8, "PDT": -7,
	"AKST": -9, "AKDT": -8, "HST": -10, "AST": -4, "ADT": -3, "NST": -3, "NDT": -2,
}

// AirNow reads the air quality observed now near p from AirNow, with key:
// the worst pollutant's index, each pollutant's from the monitor nearest p
// within the search radius AirNow sets for the reporting area. ok is false
// when none has one.
func (c *Client) AirNow(ctx context.Context, key string, p Place) (obs Observation, ok bool, err error) {
	q := url.Values{
		"format":    {"application/json"},
		"latitude":  {strconv.FormatFloat(p.Latitude, 'f', -1, 64)},
		"longitude": {strconv.FormatFloat(p.Longitude, 'f', -1, 64)},
		"api_key":   {key},
	}
	var body []struct {
		DateObserved      string `json:"dateObserved"`
		HourObserved      string `json:"hourObserved"` // "17:00", the hour's end
		LocalTimeZone     string `json:"localTimeZone"`
		ReportingAreaName string `json:"reportingAreaName"`
		NowcastAQI        *int   `json:"nowcastAQI"`
	}
	if err := c.get(ctx, "airnow", cmp.Or(c.AirNowURL, AirNowURL), q, &body); err != nil {
		return Observation{}, false, err
	}
	for _, b := range body {
		if b.NowcastAQI == nil || *b.NowcastAQI < 0 || (ok && *b.NowcastAQI <= obs.AQI) {
			continue
		}
		obs, ok = Observation{AQI: *b.NowcastAQI, Area: b.ReportingAreaName, Time: airNowHour(b.DateObserved, b.HourObserved, b.LocalTimeZone)}, true
	}
	return obs, ok, nil
}

// airNowHour is the start of the hour AirNow labels by its end, as hour
// ("17:00" for 16:00 to 17:00) on date ("2026-09-09"), local to zone
// ("PDT"); zero if any is not as expected.
func airNowHour(date, hour, zone string) time.Time {
	off, known := airNowZones[zone]
	end, err := time.Parse("15:04", hour)
	if !known || err != nil || end.Minute() != 0 {
		return time.Time{}
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), time.FixedZone(zone, off*3600))
	if err != nil {
		return time.Time{}
	}
	return day.Add(time.Duration(end.Hour()-1) * time.Hour)
}

// AQILevel is one of the EPA's categories of the US air quality index.
type AQILevel int

// The EPA's categories, from best to worst.
const (
	AQIGood AQILevel = iota
	AQIModerate
	AQISensitive // unhealthy for sensitive groups
	AQIUnhealthy
	AQIVeryUnhealthy
	AQIHazardous
)

// AQICategory is the EPA's category of the US air quality index aqi.
func AQICategory(aqi int) AQILevel {
	switch {
	case aqi <= 50:
		return AQIGood
	case aqi <= 100:
		return AQIModerate
	case aqi <= 150:
		return AQISensitive
	case aqi <= 200:
		return AQIUnhealthy
	case aqi <= 300:
		return AQIVeryUnhealthy
	}
	return AQIHazardous
}

// String is the category's name, short enough to fit beside the weather:
// "Unhealthy (sens.)" for unhealthy for sensitive groups.
func (l AQILevel) String() string {
	switch l {
	case AQIGood:
		return "Good"
	case AQIModerate:
		return "Moderate"
	case AQISensitive:
		return "Unhealthy (sens.)"
	case AQIUnhealthy:
		return "Unhealthy"
	case AQIVeryUnhealthy:
		return "Very unhealthy"
	}
	return "Hazardous"
}

// ErrNotFound is returned by Geocode for a location no place matches.
var ErrNotFound = errors.New("no such place")

// Geocode finds the place location names: a city or town, optionally
// followed, after commas, by its state, province or region (by name, or for
// the United States, its two-letter abbreviation) and its country (by name
// or code), as "Denver, CO, USA" or "Paris, France". Of the places matching,
// the most likely, as Open-Meteo orders them, is chosen.
func (c *Client) Geocode(ctx context.Context, location string) (Place, error) {
	parts := strings.Split(location, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	name, qualifiers := parts[0], slices.DeleteFunc(parts[1:], func(s string) bool { return s == "" })
	if name == "" {
		return Place{}, ErrNotFound
	}
	q := url.Values{"name": {name}, "count": {"100"}, "language": {"en"}, "format": {"json"}}
	var body struct {
		Results []geoResult `json:"results"`
	}
	if err := c.get(ctx, "open-meteo", cmp.Or(c.GeocodeURL, GeocodeURL), q, &body); err != nil {
		return Place{}, err
	}
	for _, r := range body.Results {
		if r.matches(qualifiers) {
			return r.place(), nil
		}
	}
	return Place{}, ErrNotFound
}

// geoResult is one place Open-Meteo's geocoding finds.
type geoResult struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	CountryCode string  `json:"country_code"`
	Country     string  `json:"country"`
	Admin1      string  `json:"admin1"`
	Admin2      string  `json:"admin2"`
}

// matches reports whether every qualifier names r's region (state,
// province), county, or country.
func (r geoResult) matches(qualifiers []string) bool {
	names := []string{r.Admin1, r.Admin2, r.Country, r.CountryCode}
	if r.CountryCode == "US" {
		names = append(names, usStates[r.Admin1], "USA", "United States of America", "America")
	}
	if r.CountryCode == "GB" {
		names = append(names, "UK")
	}
	for _, q := range qualifiers {
		if !slices.ContainsFunc(names, func(n string) bool { return n != "" && strings.EqualFold(n, q) }) {
			return false
		}
	}
	return true
}

// place is r as a Place, named by its name, region and country.
func (r geoResult) place() Place {
	var parts []string
	for _, s := range []string{r.Name, r.Admin1, r.Country} {
		if s != "" && !slices.Contains(parts, s) {
			parts = append(parts, s)
		}
	}
	return Place{Name: strings.Join(parts, ", "), Latitude: r.Latitude, Longitude: r.Longitude, Country: r.CountryCode}
}

// usStates are the postal abbreviations of the states, and of DC, by name.
var usStates = map[string]string{
	"Alabama": "AL", "Alaska": "AK", "Arizona": "AZ", "Arkansas": "AR", "California": "CA",
	"Colorado": "CO", "Connecticut": "CT", "Delaware": "DE", "District of Columbia": "DC",
	"Washington, D.C.": "DC", "Florida": "FL", "Georgia": "GA", "Hawaii": "HI", "Idaho": "ID",
	"Illinois": "IL", "Indiana": "IN", "Iowa": "IA", "Kansas": "KS", "Kentucky": "KY",
	"Louisiana": "LA", "Maine": "ME", "Maryland": "MD", "Massachusetts": "MA", "Michigan": "MI",
	"Minnesota": "MN", "Mississippi": "MS", "Missouri": "MO", "Montana": "MT", "Nebraska": "NE",
	"Nevada": "NV", "New Hampshire": "NH", "New Jersey": "NJ", "New Mexico": "NM", "New York": "NY",
	"North Carolina": "NC", "North Dakota": "ND", "Ohio": "OH", "Oklahoma": "OK", "Oregon": "OR",
	"Pennsylvania": "PA", "Rhode Island": "RI", "South Carolina": "SC", "South Dakota": "SD",
	"Tennessee": "TN", "Texas": "TX", "Utah": "UT", "Vermont": "VT", "Virginia": "VA",
	"Washington": "WA", "West Virginia": "WV", "Wisconsin": "WI", "Wyoming": "WY",
}

// DescribeWidth is the most characters Describe says the sky in, so that
// the weather screen's two columns fit in 80.
const DescribeWidth = 13

// Describe is what WMO weather code says the sky is doing, in at most
// DescribeWidth characters.
func Describe(code int) string {
	switch code {
	case 0:
		return "Clear"
	case 1:
		return "Mostly clear"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45:
		return "Fog"
	case 48:
		return "Freezing fog"
	case 51:
		return "Light drizzle"
	case 53:
		return "Drizzle"
	case 55:
		return "Heavy drizzle"
	case 56, 57:
		return "Frz. drizzle"
	case 61:
		return "Light rain"
	case 63:
		return "Rain"
	case 65:
		return "Heavy rain"
	case 66, 67:
		return "Freezing rain"
	case 71:
		return "Light snow"
	case 73:
		return "Snow"
	case 75:
		return "Heavy snow"
	case 77:
		return "Snow grains"
	case 80:
		return "Light showers"
	case 81:
		return "Showers"
	case 82:
		return "Heavy showers"
	case 85:
		return "Snow showers"
	case 86:
		return "Hvy snow shwr"
	case 95:
		return "Thunderstorms"
	case 96, 99:
		return "T-storm, hail"
	}
	return fmt.Sprintf("Code %d", code)
}

// How long a forecast is used before it is read again, how long after a
// failed read it is tried again, how long one read may take, and how long
// a place's forecast is kept unused.
const (
	cacheTTL     = 30 * time.Minute
	cacheRetry   = time.Minute
	fetchTimeout = 30 * time.Second
	cacheIdle    = time.Hour
)

// Snapshot is a place's forecast as last read.
type Snapshot struct {
	Hours []Hour

	// Fetched is when the forecast was last read, zero if never.
	Fetched time.Time

	// Err is why the last read failed, nil if it worked. The forecast from
	// the read before is kept.
	Err error

	// AQIErr is why the air quality could not be read with the forecast,
	// nil if it was. The hours keep the air quality read before, for the
	// hours it covered.
	AQIErr error

	// Observed is the air quality AirNow measured, nil with none: without
	// an AirNow key, outside AirNow's countries, or with no monitor near.
	// ObservedErr is why it could not be read, the one read before kept.
	Observed    *Observation
	ObservedErr error

	// Loading is set while the first read is under way, with nothing yet
	// to show.
	Loading bool
}

// Cache keeps the forecast of each place asked for, in each of the units
// asked for, reading it again in
// the background when it is older than cacheTTL; the one it has is used
// meanwhile. A place not asked for in cacheIdle is dropped. It is safe for
// concurrent use.
type Cache struct {
	client    *Client
	airNowKey func() string
	now       func() time.Time

	mu      sync.Mutex
	entries map[cacheKey]*cacheEntry

	fetches sync.WaitGroup // for tests to wait on
}

// cacheKey is a place, and whether its forecast is in imperial units.
type cacheKey struct {
	place    Place
	imperial bool
}

type cacheEntry struct {
	snap       Snapshot
	next       time.Time // when to read again
	refreshing bool
	lastUsed   time.Time
}

// NewCache returns an empty cache reading forecasts with client, and the
// air quality measured with the AirNow key airNowKey returns at each read,
// "" for none; a nil airNowKey is none.
func NewCache(client *Client, airNowKey func() string) *Cache {
	if airNowKey == nil {
		airNowKey = func() string { return "" }
	}
	return &Cache{client: client, airNowKey: airNowKey, now: time.Now, entries: map[cacheKey]*cacheEntry{}}
}

// Snapshot returns p's forecast, in imperial units or not (see
// Client.Forecast), beginning a read in the background if it is due one.
func (c *Cache) Snapshot(p Place, imperial bool) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	key := cacheKey{p, imperial}
	for k, e := range c.entries {
		if k != key && !e.refreshing && now.Sub(e.lastUsed) > cacheIdle {
			delete(c.entries, k)
		}
	}
	e, ok := c.entries[key]
	if !ok {
		e = &cacheEntry{}
		c.entries[key] = e
	}
	e.lastUsed = now
	if !e.refreshing && !now.Before(e.next) {
		e.refreshing = true
		c.fetches.Add(1)
		go c.fetch(key, e)
	}
	s := e.snap
	s.Hours = slices.Clone(s.Hours)
	s.Loading = e.refreshing && s.Fetched.IsZero()
	return s
}

// fetch reads key's forecast into e.
func (c *Cache) fetch(key cacheKey, e *cacheEntry) {
	defer c.fetches.Done()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	hours, err := c.client.Forecast(ctx, key.place, key.imperial)
	var aqi map[int64]int
	var aqiErr, obsErr error
	var obs *Observation
	if err == nil {
		aqi, aqiErr = c.client.AirQuality(ctx, key.place)
		if k := c.airNowKey(); k != "" && AirNowCovers(key.place) {
			o, found, oerr := c.client.AirNow(ctx, k, key.place)
			switch {
			case oerr != nil:
				obsErr = oerr
			case found:
				obs = &o
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	e.refreshing = false
	now := c.now()
	if err != nil {
		e.snap.Err, e.next = err, now.Add(cacheRetry)
		return
	}
	// The air quality failing does not hold back the forecast; the hours
	// keep what was read of it before.
	if aqiErr != nil {
		aqi = map[int64]int{}
		for _, h := range e.snap.Hours {
			if h.HasAQI {
				aqi[h.Time.Unix()] = h.AQI
			}
		}
	}
	for i := range hours {
		hours[i].AQI, hours[i].HasAQI = aqi[hours[i].Time.Unix()]
	}
	if obsErr != nil {
		obs = e.snap.Observed // as for the forecast's air quality
	}
	e.snap = Snapshot{Hours: hours, Fetched: now, AQIErr: aqiErr, Observed: obs, ObservedErr: obsErr}
	e.next = now.Add(cacheTTL)
}

// observedFor is how long after its hour began an observation still stands
// for the air quality now: AirNow's come an hour or so after it, so this
// allows one or two missed.
const observedFor = 3 * time.Hour

// AQINow is the air quality index now, at now: measured, if AirNow has
// observed it within observedFor (or at a time not known), as obs; else as
// forecast for the hour under way, with obs nil. ok is false with neither.
func (s Snapshot) AQINow(now time.Time) (aqi int, obs *Observation, ok bool) {
	if o := s.Observed; o != nil && (o.Time.IsZero() || now.Sub(o.Time) < observedFor) {
		return o.AQI, o, true
	}
	if h := s.Next(now, 1); len(h) == 1 && h[0].HasAQI {
		return h[0].AQI, nil, true
	}
	return 0, nil, false
}

// Worst is the hour of hours with the highest air quality index, and
// whether any has one; of hours tied, the first.
func Worst(hours []Hour) (worst Hour, ok bool) {
	for _, h := range hours {
		if h.HasAQI && (!ok || h.AQI > worst.AQI) {
			worst, ok = h, true
		}
	}
	return worst, ok
}

// Next is the hours of s's forecast from the one under way at now, at most
// n of them.
func (s Snapshot) Next(now time.Time, n int) []Hour {
	i := 0
	for i < len(s.Hours) && !s.Hours[i].Time.Add(time.Hour).After(now) {
		i++
	}
	return s.Hours[i:min(len(s.Hours), i+n)]
}

// Geocode finds the place location names, as Client.Geocode does, with the
// cache's client. Places are not cached: they are looked up only when a
// user types a new location.
func (c *Cache) Geocode(ctx context.Context, location string) (Place, error) {
	return c.client.Geocode(ctx, location)
}

// CheckAirNowKey reads the air quality AirNow measured at Denver with key,
// to see whether AirNow takes it: an error wrapping ErrBadKey if it is
// refused, another if AirNow could not be asked.
func (c *Cache) CheckAirNowKey(ctx context.Context, key string) error {
	_, _, err := c.client.AirNow(ctx, key, Denver)
	return err
}
