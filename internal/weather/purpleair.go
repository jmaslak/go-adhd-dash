// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package weather

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// PurpleAirURL is PurpleAir's API of one sensor's latest data, by its
// index, which follows.
const PurpleAirURL = "https://api.purpleair.com/v1/sensors/"

// PurpleAirSensor is a PurpleAir sensor, and the keys to read it.
type PurpleAirSensor struct {
	APIKey  string // a PurpleAir API read key
	Index   int    // the sensor's sensor_index
	ReadKey string // the sensor's own read key, for a private one; "" for none
}

// PurpleAirReading is the air quality a PurpleAir sensor last measured.
type PurpleAirReading struct {
	// Name is the sensor's, as on PurpleAir's map.
	Name string

	// PM25 is the fine particles, in µg/m³, as corrected by the EPA's
	// formula for PurpleAir sensors (see epaCorrected); AQI is the US air
	// quality index it gives (see PM25AQI).
	PM25 float64
	AQI  int

	// Time is when PurpleAir last heard from the sensor.
	Time time.Time

	// Indoor is set for a sensor registered as inside, which the EPA's
	// correction is not made for.
	Indoor bool
}

// ErrNoReading is returned, wrapped, when a sensor answers without what its
// air quality index needs.
var ErrNoReading = errors.New("the sensor has no reading to use")

// purpleAirFields are the sensor data fields asked for: PurpleAir charges
// for each, so no more than these.
const purpleAirFields = "name,last_seen,location_type,humidity,pm2.5_cf_1_a,pm2.5_cf_1_b,channel_flags"

// PurpleAir reads s's latest data from PurpleAir, as the air quality it
// measured: the fine particles of its channels not downgraded (their
// channel_flags), averaged, corrected by the EPA's formula with its
// humidity. An error wrapping ErrBadKey is a key PurpleAir refuses,
// ErrNoSensor no such sensor (or a private one without its read key),
// ErrBadReadKey a read key not the sensor's, and ErrNoReading data that will
// not do.
func (c *Client) PurpleAir(ctx context.Context, s PurpleAirSensor) (PurpleAirReading, error) {
	q := url.Values{"fields": {purpleAirFields}}
	if s.ReadKey != "" {
		q.Set("read_key", s.ReadKey)
	}
	var body struct {
		Sensor struct {
			Name         string   `json:"name"`
			LastSeen     int64    `json:"last_seen"`
			LocationType int      `json:"location_type"`
			Humidity     *float64 `json:"humidity"`
			A            *float64 `json:"pm2.5_cf_1_a"`
			B            *float64 `json:"pm2.5_cf_1_b"`
			ChannelFlags int      `json:"channel_flags"`
		} `json:"sensor"`
	}
	base := cmp.Or(c.PurpleAirURL, PurpleAirURL) + strconv.Itoa(s.Index)
	if err := c.get(ctx, "purpleair", base, q, http.Header{"X-Api-Key": {s.APIKey}}, &body); err != nil {
		return PurpleAirReading{}, err
	}
	b := body.Sensor
	r := PurpleAirReading{Name: b.Name, Time: time.Unix(b.LastSeen, 0), Indoor: b.LocationType == 1}

	// channel_flags: 1 is A downgraded, 2 B, 3 both. A sensor with one
	// channel has no B.
	var sum float64
	var n int
	if b.A != nil && b.ChannelFlags != 1 && b.ChannelFlags != 3 {
		sum, n = sum+*b.A, n+1
	}
	if b.B != nil && b.ChannelFlags != 2 && b.ChannelFlags != 3 {
		sum, n = sum+*b.B, n+1
	}
	switch {
	case n == 0:
		return r, fmt.Errorf("purpleair: %w: no channel with fine particles, or all downgraded", ErrNoReading)
	case b.Humidity == nil:
		return r, fmt.Errorf("purpleair: %w: no humidity, which the EPA's correction needs", ErrNoReading)
	}
	r.PM25 = epaCorrected(sum/float64(n), *b.Humidity)
	r.AQI = PM25AQI(r.PM25)
	return r, nil
}

// epaCorrected is a PurpleAir sensor's fine particles, x (pm2.5_cf_1, in
// µg/m³), at relative humidity rh (%), as the EPA corrects them for its
// Fire and Smoke Map: its US-wide correction (Barkjohn et al., 2021), with
// its 2023 extension for wildfire smoke, blending linearly between the
// pieces. Never below zero.
func epaCorrected(x, rh float64) float64 {
	var pm float64
	switch {
	case x < 30:
		pm = 0.524*x - 0.0862*rh + 5.75
	case x < 50:
		w := x/20 - 1.5
		pm = (0.786*w+0.524*(1-w))*x - 0.0862*rh + 5.75
	case x < 210:
		pm = 0.786*x - 0.0862*rh + 5.75
	case x < 260:
		w := x/50 - 4.2
		pm = (0.69*w+0.786*(1-w))*x - 0.0862*rh*(1-w) + 2.966*w + 5.75*(1-w) + 8.84e-4*x*x*w
	default:
		pm = 2.966 + 0.69*x + 8.84e-4*x*x
	}
	return max(pm, 0)
}

// pm25Breakpoints are the EPA's breakpoints of fine particles (µg/m³, from
// its 2024 revision) and the air quality index each range runs between.
var pm25Breakpoints = []struct{ lo, hi float64 }{
	{0.0, 9.0}, {9.1, 35.4}, {35.5, 55.4}, {55.5, 125.4}, {125.5, 225.4}, {225.5, 325.4},
}
var aqiBreakpoints = []struct{ lo, hi int }{
	{0, 50}, {51, 100}, {101, 150}, {151, 200}, {201, 300}, {301, 500},
}

// PM25AQI is the US air quality index of fine particles at pm µg/m³, as the
// EPA reckons it: the concentration cut to a tenth, then between the
// breakpoints of its range; 500 beyond the last.
func PM25AQI(pm float64) int {
	c := math.Floor(pm*10) / 10
	for i, b := range pm25Breakpoints {
		if c <= b.hi {
			a := aqiBreakpoints[i]
			return int(math.Round(float64(a.hi-a.lo)/(b.hi-b.lo)*(max(c, b.lo)-b.lo) + float64(a.lo)))
		}
	}
	return 500
}

// PurpleAirSnapshot is a sensor's reading as last read.
type PurpleAirSnapshot struct {
	// Reading is the reading, nil if none has been read.
	Reading *PurpleAirReading

	// Fetched is when it was last read, zero if never; Err why the last
	// read failed, nil if it worked, the reading before kept; Loading is
	// set while the first is under way.
	Fetched time.Time
	Err     error
	Loading bool
}

// PurpleAirCache keeps the reading of each PurpleAir sensor asked for,
// reading it again in the background when it is older than cacheTTL, as
// Cache does forecasts; a sensor not asked for in cacheIdle is dropped. It
// is safe for concurrent use.
type PurpleAirCache struct {
	client *Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[PurpleAirSensor]*purpleAirEntry

	fetches sync.WaitGroup // for tests to wait on
}

type purpleAirEntry struct {
	snap       PurpleAirSnapshot
	next       time.Time
	refreshing bool
	lastUsed   time.Time
}

// NewPurpleAirCache returns an empty cache reading sensors with client.
func NewPurpleAirCache(client *Client) *PurpleAirCache {
	return &PurpleAirCache{client: client, now: time.Now, entries: map[PurpleAirSensor]*purpleAirEntry{}}
}

// Snapshot returns s's reading, beginning a read in the background if it
// is due one.
func (c *PurpleAirCache) Snapshot(s PurpleAirSensor) PurpleAirSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.entries {
		if k != s && !e.refreshing && now.Sub(e.lastUsed) > cacheIdle {
			delete(c.entries, k)
		}
	}
	e, ok := c.entries[s]
	if !ok {
		e = &purpleAirEntry{}
		c.entries[s] = e
	}
	e.lastUsed = now
	if !e.refreshing && !now.Before(e.next) {
		e.refreshing = true
		c.fetches.Add(1)
		go c.fetch(s, e)
	}
	snap := e.snap
	snap.Loading = e.refreshing && snap.Fetched.IsZero()
	return snap
}

// fetch reads s into e.
func (c *PurpleAirCache) fetch(s PurpleAirSensor, e *purpleAirEntry) {
	defer c.fetches.Done()
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	r, err := c.client.PurpleAir(ctx, s)

	c.mu.Lock()
	defer c.mu.Unlock()
	e.refreshing = false
	now := c.now()
	if err != nil {
		e.snap.Err, e.next = err, now.Add(cacheRetry)
		return
	}
	e.snap = PurpleAirSnapshot{Reading: &r, Fetched: now}
	e.next = now.Add(cacheTTL)
}

// Check reads s at once, uncached, to see whether it can be read: for
// saving a user's sensor.
func (c *PurpleAirCache) Check(ctx context.Context, s PurpleAirSensor) (PurpleAirReading, error) {
	return c.client.PurpleAir(ctx, s)
}

// purpleAirFreshFor is how long after PurpleAir last heard from a sensor its
// reading still stands for the air quality now; sensors report every two
// minutes.
const purpleAirFreshFor = time.Hour

// Current is s's reading, if there is one, and it is recent at now (see
// purpleAirFreshFor).
func (s PurpleAirSnapshot) Current(now time.Time) (PurpleAirReading, bool) {
	if s.Reading == nil || now.Sub(s.Reading.Time) >= purpleAirFreshFor {
		return PurpleAirReading{}, false
	}
	return *s.Reading, true
}
