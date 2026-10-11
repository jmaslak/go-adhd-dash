// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package weather

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakePurpleAir is PurpleAir's sensor API as a test server, for sensor 123
// (with read key "rk" when private) and the API key "good", answering
// with sensor's JSON; reads counts the reads.
type fakePurpleAir struct {
	*httptest.Server
	sensor  atomic.Value
	private atomic.Bool
	reads   atomic.Int32
	fields  atomic.Value
}

func newFakePurpleAir(t *testing.T) *fakePurpleAir {
	t.Helper()
	f := &fakePurpleAir{}
	f.sensor.Store(`{"name":"Back yard","last_seen":1791734400,"location_type":0,"humidity":40,"pm2.5_cf_1_a":10,"pm2.5_cf_1_b":12,"channel_flags":0}`)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reads.Add(1)
		f.fields.Store(r.URL.Query().Get("fields"))
		fail := func(status int, kind, why string) {
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"api_version":"V1.2.3","time_stamp":1791741983,"error":%q,"description":%q}`, kind, why)
		}
		switch {
		case r.Header.Get("X-API-Key") != "good":
			fail(http.StatusForbidden, "ApiKeyInvalidError", "The provided api_key was not valid.")
		case r.URL.Path != "/v1/sensors/123":
			fail(http.StatusNotFound, "NotFoundError", "Cannot find a sensor with the provided parameters.")
		case f.private.Load() && r.URL.Query().Get("read_key") == "":
			fail(http.StatusNotFound, "NotFoundError", "Cannot find a sensor with the provided parameters.")
		case f.private.Load() && r.URL.Query().Get("read_key") != "rk":
			fail(http.StatusBadRequest, "InvalidDataReadKeyError", "An invalid sensor data read_key was provided.")
		default:
			_, _ = fmt.Fprintf(w, `{"api_version":"V1.2.3","time_stamp":1791741983,"data_time_stamp":1791741980,"sensor":%s}`, f.sensor.Load())
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePurpleAir) client() *Client { return &Client{PurpleAirURL: f.URL + "/v1/sensors/"} }

func TestPurpleAir(t *testing.T) {
	f := newFakePurpleAir(t)
	c := f.client()
	ctx := context.Background()
	good := PurpleAirSensor{APIKey: "good", Index: 123}

	// Both channels averaged, 11 µg/m³ at 40% corrected to 8.066, AQI 44.
	r, err := c.PurpleAir(ctx, good)
	if err != nil || r.Name != "Back yard" || math.Abs(r.PM25-8.066) > 1e-9 || r.AQI != 44 || r.Indoor || !r.Time.Equal(time.Unix(1791734400, 0)) {
		t.Errorf("%+v, %v", r, err)
	}
	if got := f.fields.Load(); got != purpleAirFields {
		t.Errorf("fields %v", got)
	}

	// A downgraded channel is left out; with both, or none, there is
	// nothing to go on; nor without humidity.
	for _, c2 := range []struct {
		sensor string
		pm     float64 // as corrected; -1 for none
	}{
		{`{"humidity":40,"pm2.5_cf_1_a":10,"pm2.5_cf_1_b":50,"channel_flags":2}`, 0.524*10 - 0.0862*40 + 5.75},
		{`{"humidity":40,"pm2.5_cf_1_a":50,"pm2.5_cf_1_b":10,"channel_flags":1}`, 0.524*10 - 0.0862*40 + 5.75},
		{`{"humidity":40,"pm2.5_cf_1_a":10,"channel_flags":0,"location_type":1}`, 0.524*10 - 0.0862*40 + 5.75},
		{`{"humidity":40,"pm2.5_cf_1_a":10,"pm2.5_cf_1_b":12,"channel_flags":3}`, -1},
		{`{"humidity":40,"channel_flags":0}`, -1},
		{`{"humidity":null,"pm2.5_cf_1_a":10,"pm2.5_cf_1_b":12,"channel_flags":0}`, -1},
	} {
		f.sensor.Store(c2.sensor)
		r, err := c.PurpleAir(ctx, good)
		if c2.pm < 0 {
			if !errors.Is(err, ErrNoReading) {
				t.Errorf("%s: %+v, %v; want no reading", c2.sensor, r, err)
			}
			continue
		}
		if err != nil || math.Abs(r.PM25-c2.pm) > 1e-9 {
			t.Errorf("%s: %+v, %v; want %v", c2.sensor, r, err, c2.pm)
		}
		if strings.Contains(c2.sensor, `"location_type":1`) && !r.Indoor {
			t.Errorf("%s: not indoor", c2.sensor)
		}
	}

	// Refusals, each its own error, none with the key.
	f.private.Store(true)
	for _, c2 := range []struct {
		s    PurpleAirSensor
		want error
	}{
		{PurpleAirSensor{APIKey: "bad-secret", Index: 123}, ErrBadKey},
		{PurpleAirSensor{APIKey: "good", Index: 999}, ErrNoSensor},
		{PurpleAirSensor{APIKey: "good", Index: 123}, ErrNoSensor},
		{PurpleAirSensor{APIKey: "good", Index: 123, ReadKey: "wrong"}, ErrBadReadKey},
	} {
		if _, err := c.PurpleAir(ctx, c2.s); !errors.Is(err, c2.want) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%+v: %v; want %v", c2.s, err, c2.want)
		}
	}
	f.sensor.Store(`{"name":"Mine","humidity":40,"pm2.5_cf_1_a":10,"pm2.5_cf_1_b":12,"channel_flags":0}`)
	if r, err := c.PurpleAir(ctx, PurpleAirSensor{APIKey: "good", Index: 123, ReadKey: "rk"}); err != nil || r.Name != "Mine" {
		t.Errorf("private with its read key: %+v, %v", r, err)
	}
}

func TestEPACorrected(t *testing.T) {
	// Each piece, and that they meet where they change.
	for _, c := range []struct{ x, rh, want float64 }{
		{10, 40, 7.542},
		{100, 40, 80.902},
		{300, 40, 2.966 + 0.69*300 + 8.84e-4*300*300},
		{0, 100, 0}, // never below zero
	} {
		if got := epaCorrected(c.x, c.rh); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v at %v%%: %v, want %v", c.x, c.rh, got, c.want)
		}
	}
	for _, x := range []float64{30, 50, 210, 260} {
		if lo, hi := epaCorrected(x-1e-9, 40), epaCorrected(x, 40); math.Abs(lo-hi) > 1e-6 {
			t.Errorf("at %v: %v below, %v at", x, lo, hi)
		}
	}
}

func TestPM25AQI(t *testing.T) {
	for pm, want := range map[float64]int{
		0: 0, 9.0: 50, 9.09: 50, 9.1: 51, 35.4: 100, 35.5: 101, 55.4: 150, 55.5: 151,
		125.4: 200, 125.5: 201, 225.4: 300, 225.5: 301, 325.4: 500, 400: 500, 20: 71,
	} {
		if got := PM25AQI(pm); got != want {
			t.Errorf("%v µg/m³: %d, want %d", pm, got, want)
		}
	}
}

func TestPurpleAirCache(t *testing.T) {
	f := newFakePurpleAir(t)
	c := NewPurpleAirCache(f.client())
	clock := time.Unix(1791734400, 0).Add(10 * time.Minute)
	c.now = func() time.Time { return clock }
	s := PurpleAirSensor{APIKey: "good", Index: 123}
	read := func() PurpleAirSnapshot {
		c.Snapshot(s)
		c.fetches.Wait()
		return c.Snapshot(s)
	}

	if snap := c.Snapshot(s); !snap.Loading {
		t.Errorf("first: %+v", snap)
	}
	c.fetches.Wait()
	snap := c.Snapshot(s)
	if snap.Reading == nil || snap.Reading.AQI != 44 || f.reads.Load() != 1 {
		t.Fatalf("read: %+v, %d reads", snap, f.reads.Load())
	}
	// Kept for 30 minutes, then read again.
	clock = clock.Add(cacheTTL - time.Second)
	if read(); f.reads.Load() != 1 {
		t.Errorf("%d reads before 30 minutes", f.reads.Load())
	}
	clock = clock.Add(time.Second)
	if read(); f.reads.Load() != 2 {
		t.Errorf("%d reads after 30 minutes", f.reads.Load())
	}
	// A failed read keeps the reading, saying why.
	f.sensor.Store(`{"humidity":null,"pm2.5_cf_1_a":10,"channel_flags":0}`)
	clock = clock.Add(cacheTTL)
	if snap := read(); snap.Reading == nil || !errors.Is(snap.Err, ErrNoReading) {
		t.Errorf("failed: %+v", snap)
	}

	// Current while the sensor has reported in the last hour.
	at := time.Unix(1791734400, 0)
	if r, ok := snap.Current(at.Add(59 * time.Minute)); !ok || r.AQI != 44 {
		t.Errorf("current: %+v, %v", r, ok)
	}
	if _, ok := snap.Current(at.Add(time.Hour)); ok {
		t.Error("an hour old, still current")
	}
	if _, ok := (PurpleAirSnapshot{}).Current(at); ok {
		t.Error("none, current")
	}
}
