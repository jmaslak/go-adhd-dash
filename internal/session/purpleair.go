// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// The PurpleAir screen (option 6 on the settings screen): a user's own
// PurpleAir sensor, whose air quality is shown as now's, by its API key,
// index and, for a private one, read key.
const (
	paKeyField     = "pakey"
	paSensorField  = "pasensor"
	paReadKeyField = "pareadkey"
	paLabelWidth   = 14 // "API key ===>", its attribute byte, and the field's
	paFirstRow     = 10
	paKeyMax       = 64 // PurpleAir's API keys are UUIDs, 36 characters
	paSensorWidth  = 10
	paReadKeyWidth = 32 // a sensor's read key is usually 16 characters

	// paCheckTimeout bounds reading a sensor typed, to check it.
	paCheckTimeout = 15 * time.Second
)

// paText explains the screen.
var paText = []string{
	"Your own PurpleAir sensor's air quality, read every 30 minutes, is shown as",
	"the air quality now on the weather screen and the dashboard, in place of",
	"AirNow's or the forecast. Get an API key (a READ key) at develop.purpleair.com;",
	"PurpleAir charges points for each read. The sensor's index is the number in",
	"its map link (select=12345). A private sensor also needs its own read key.",
}

// PurpleAirChecker reads a PurpleAir sensor at once, to check it (see
// weather.PurpleAirCache).
type PurpleAirChecker interface {
	Check(ctx context.Context, s weather.PurpleAirSensor) (weather.PurpleAirReading, error)
}

// purpleAirState is one session's place on the PurpleAir screen.
type purpleAirState struct {
	// typed is what was typed when it could not be saved, drawn again.
	typed map[string]string

	// leaveArmed is set by PF3 with a change typed: a second PF3 leaves.
	leaveArmed bool

	message string
	isError bool
}

// purpleAirOf is the PurpleAir sensor of the user with id, in store, and
// whether they have one.
func purpleAirOf(store *users.Store, id int) (weather.PurpleAirSensor, bool) {
	if store == nil {
		return weather.PurpleAirSensor{}, false
	}
	list, _, err := store.Load()
	if err != nil {
		return weather.PurpleAirSensor{}, false
	}
	i := slices.IndexFunc(list, func(u users.User) bool { return u.ID == id })
	if i < 0 || list[i].PurpleAir == nil {
		return weather.PurpleAirSensor{}, false
	}
	p := list[i].PurpleAir
	return weather.PurpleAirSensor{APIKey: p.APIKey, Index: p.Sensor, ReadKey: p.ReadKey}, true
}

// buildPurpleAir renders the PurpleAir screen for the user called name,
// whose sensor is current (nil for none), and where the cursor goes: the
// API key's field.
func buildPurpleAir(rows, cols int, now time.Time, name string, current *users.PurpleAir, p *purpleAirState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "PURPLEAIR", now)
	header := line{
		{Content: "Your PurpleAir sensor", Color: go3270.Turquoise, Intense: true},
		{Content: "(" + name + ")", Color: go3270.Blue},
	}
	if current == nil {
		header = append(header, go3270.Field{Content: "none set", Color: go3270.Yellow})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	for i, t := range paText {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t, Color: go3270.Green}})...)
	}

	var stored users.PurpleAir
	if current != nil {
		stored = *current
	}
	sensor := ""
	if stored.Sensor != 0 {
		sensor = strconv.Itoa(stored.Sensor)
	}
	value := func(field, v string) string {
		if t, ok := p.typed[field]; ok {
			return t
		}
		return v
	}
	for i, f := range []struct {
		label, field, value, hint string
		width                     int
	}{
		{"API key ===>", paKeyField, stored.APIKey, "", cols - paLabelWidth - 2},
		{"Sensor ====>", paSensorField, sensor, "its index, a number", paSensorWidth},
		{"Read key ==>", paReadKeyField, stored.ReadKey, "for a private sensor only", paReadKeyWidth},
	} {
		row := paFirstRow + i
		end := go3270.Field{Row: row, Col: paLabelWidth + f.width, Autoskip: true, Color: go3270.Blue, Content: f.hint}
		if f.hint != "" {
			end.Content = truncate(" "+f.hint, cols-paLabelWidth-f.width-1)
		}
		screen = append(screen,
			go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: f.label},
			go3270.Field{
				Row: row, Col: paLabelWidth - 1, Write: true, Name: f.field, Content: cutRunes(value(f.field, f.value), f.width),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			end,
		)
	}
	screen = append(screen, placeLine(paFirstRow+4, cols, line{{Content: "Blank them all to remove it.", Color: go3270.Blue}})...)
	screen = appendMessageRows(screen, rows, cols, p.message, p.isError,
		"Type the key and the sensor, then press Enter; the sensor is read to check them.", "PF3=Back Enter=Save")
	return screen, paFirstRow, paLabelWidth
}

// handle acts on a key on the PurpleAir screen for the user u, returning
// whether to go back to the settings screen. Enter saves what was typed,
// once check (nil for none) finds PurpleAir does not refuse it, and with
// every field blank removes the sensor; PF3 leaves, a second time with a
// change typed. Neither key is logged.
func (p *purpleAirState) handle(resp go3270.Response, store *users.Store, u *users.User, check PurpleAirChecker, logf func(string, ...any)) (back bool) {
	p.message, p.isError = "", false
	armed := p.leaveArmed
	p.leaveArmed, p.typed = false, nil

	var current users.PurpleAir
	if s, ok := purpleAirOf(store, u.ID); ok {
		current = users.PurpleAir{APIKey: s.APIKey, Sensor: s.Index, ReadKey: s.ReadKey}
	}
	key := strings.TrimSpace(resp.Values[paKeyField])
	sensorText := strings.TrimSpace(resp.Values[paSensorField])
	readKey := strings.TrimSpace(resp.Values[paReadKeyField])
	keep := func() {
		p.typed = map[string]string{paKeyField: resp.Values[paKeyField], paSensorField: resp.Values[paSensorField], paReadKeyField: resp.Values[paReadKeyField]}
	}
	currentSensor := ""
	if current.Sensor != 0 {
		currentSensor = strconv.Itoa(current.Sensor)
	}
	changed := key != current.APIKey || sensorText != currentSensor || readKey != current.ReadKey

	switch resp.AID {
	case go3270.AIDPF3:
		if changed && !armed {
			keep()
			p.leaveArmed = true
			p.message, p.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}
	if !changed {
		return false
	}
	set := func(pa *users.PurpleAir) error {
		return store.Update(func(list *[]users.User, _ func() int) error {
			i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == u.ID })
			if i < 0 {
				return errors.New("your user has been removed")
			}
			(*list)[i].PurpleAir = pa
			return nil
		})
	}

	if key == "" && sensorText == "" && readKey == "" {
		if err := set(nil); err != nil {
			p.message, p.isError = "Could not save: "+err.Error()+".", true
			return false
		}
		logf("%q removed their PurpleAir sensor", u.Name)
		p.message = "Removed your PurpleAir sensor; the air quality now is AirNow's or the forecast's."
		return false
	}
	sensor, err := strconv.Atoi(sensorText)
	notKey := func(s string) bool {
		return utf8.RuneCountInString(s) > paKeyMax || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	}
	switch {
	case key == "":
		p.message = "Type your PurpleAir API key too."
	case err != nil || sensor <= 0:
		p.message = fmt.Sprintf("The sensor is its index, a number such as 12345, not %q.", sensorText)
	case notKey(key) || notKey(readKey):
		p.message = fmt.Sprintf("That is not a PurpleAir key: at most %d characters, and no spaces.", paKeyMax)
	}
	if p.message != "" {
		keep()
		p.isError = true
		return false
	}

	// Read before it is saved, so that a key or sensor mistyped is not
	// kept; one that cannot be checked, PurpleAir being out of reach, or
	// whose reading will not do, is kept anyway, saying so.
	said := ""
	if check != nil {
		ctx, cancel := context.WithTimeout(context.Background(), paCheckTimeout)
		r, err := check.Check(ctx, weather.PurpleAirSensor{APIKey: key, Index: sensor, ReadKey: readKey})
		cancel()
		switch {
		case errors.Is(err, weather.ErrBadKey):
			p.message = "PurpleAir refused the API key: " + purpleAirReason(err)
		case errors.Is(err, weather.ErrNoSensor):
			p.message = fmt.Sprintf("PurpleAir has no sensor %d; a private one needs its read key.", sensor)
		case errors.Is(err, weather.ErrBadReadKey):
			p.message = fmt.Sprintf("PurpleAir refused the read key for sensor %d.", sensor)
		case errors.Is(err, weather.ErrNoReading):
			said = "Saved, but its reading will not do: " + purpleAirReason(err)
		case err != nil:
			said = "Saved, but PurpleAir could not be asked: " + err.Error()
		default:
			said = fmt.Sprintf("Saved: %s, now %d %s.", r.Name, r.AQI, weather.AQICategory(r.AQI))
		}
		if p.message != "" {
			keep()
			p.isError = true
			return false
		}
	}
	if err := set(&users.PurpleAir{APIKey: key, Sensor: sensor, ReadKey: readKey}); err != nil {
		keep()
		p.message, p.isError = "Could not save: "+err.Error()+".", true
		return false
	}
	logf("%q set their PurpleAir sensor to %d", u.Name, sensor)
	p.message = cmp.Or(said, "Saved.")
	return false
}

// purpleAirReason is err's last part: PurpleAir's own reason, without the
// error's prefixes.
func purpleAirReason(err error) string {
	msg := err.Error()
	return msg[strings.LastIndex(msg, ": ")+2:]
}
