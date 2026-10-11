// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// The weather screen (the weather command): the forecast for each of the
// next 24 hours, at the user's location, in two columns of 12 so that it
// fits on a 24 by 80 screen.
const (
	weatherHours     = 24
	weatherAQIRow    = 3 // the air quality now, then the worst forecast
	weatherHeadRow   = 6 // the columns' headings
	weatherFirstRow  = weatherHeadRow + 1
	weatherPerColumn = weatherHours / 2

	// A column is the time and its attribute byte, then the rest and its
	// (with a sky of at most 13 characters, 39 wide), then two clear
	// columns, the second the next column's first attribute byte, so 41:
	// two fit in 80 columns, the second running to the last.
	weatherTimeWidth   = 9 // "Mon 15:00"
	weatherColumnWidth = 41

	weatherPrompt = "Weather data by Open-Meteo.com. Enter redraws; PF3 goes back."
)

// placeOf is where the weather prefs ask for is: the place found for the
// location typed, or Denver.
func placeOf(prefs users.Preferences) weather.Place {
	if l := prefs.Location; l != nil {
		return weather.Place{Name: l.Name, Latitude: l.Latitude, Longitude: l.Longitude, Country: l.Country}
	}
	return weather.Denver
}

// imperialFor reports whether the weather prefs ask for is in degrees
// Fahrenheit and miles per hour: as chosen, or else as usual at the place.
func imperialFor(prefs users.Preferences) bool {
	switch prefs.Units {
	case "F":
		return true
	case "C":
		return false
	}
	return placeOf(prefs).Imperial()
}

// buildWeather renders the forecast snap for place from the hour under way
// at now, in imperial units or not (see imperialFor), and where the cursor
// goes.
func buildWeather(rows, cols int, now time.Time, place weather.Place, imperial bool, snap weather.Snapshot) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "WEATHER", now)
	heading := line{{Content: "Weather for " + place.Name, Color: go3270.Turquoise, Intense: true}}
	hours := snap.Next(now, weatherHours)
	// What went wrong is said on the message row, which has room for it.
	problem := ""
	switch {
	case snap.Loading:
		heading = append(heading, go3270.Field{Content: "(reading the forecast...)", Color: go3270.Blue})
	case snap.Fetched.IsZero():
		if snap.Err != nil {
			problem = "Could not read the forecast: " + snap.Err.Error()
		}
	default:
		heading = append(heading, go3270.Field{Content: "(as of " + snap.Fetched.In(now.Location()).Format("15:04") + ")", Color: go3270.Blue})
		if snap.Err != nil {
			problem = "Could not read it again: " + snap.Err.Error()
		}
	}
	screen = append(screen, placeLine(2, cols, heading)...)
	current, worst := aqiLines(snap, hours, now)
	screen = append(screen, placeLine(weatherAQIRow, cols, current)...)
	screen = append(screen, placeLine(weatherAQIRow+1, cols, worst)...)
	switch {
	case problem != "":
	case errors.Is(snap.ObservedErr, weather.ErrBadKey):
		problem = "AirNow refused its API key; an admin can change it (ADMIN, option 11)."
	case snap.ObservedErr != nil:
		problem = "Could not read AirNow: " + snap.ObservedErr.Error()
	case snap.AQIErr != nil:
		problem = "Could not read the air quality: " + snap.AQIErr.Error()
	}

	temp, wind := "°C", "km/h"
	if imperial {
		temp, wind = "°F", "mph"
	}
	if len(hours) == 0 && !snap.Loading && !snap.Fetched.IsZero() {
		screen = append(screen, placeLine(weatherHeadRow, cols, line{{Content: "The forecast has no hours from now.", Color: go3270.Yellow}})...)
	}
	for i, h := range hours {
		col := (i / weatherPerColumn) * weatherColumnWidth
		row := weatherFirstRow + i%weatherPerColumn
		if i%weatherPerColumn == 0 {
			// A field is underlined to the next attribute byte, so an empty
			// field ends the heading's just after its text, "Sky" padded to
			// the widest sky. In the second column, that is the last column,
			// and the next row's first field ends it instead.
			screen = append(screen, placeLineAt(weatherHeadRow, col, cols, line{{
				Content: fmt.Sprintf("%-*s %4s %4s %4s %-*s", weatherTimeWidth, "Time", temp, "Prec", wind, weather.DescribeWidth, "Sky"),
				Color:   go3270.Blue, Highlighting: go3270.Underscore,
			}, {}})...)
		}
		if row >= rows-2 {
			continue
		}
		screen = append(screen, placeLineAt(row, col, cols, line{
			{Content: h.Time.In(now.Location()).Format("Mon 15:04"), Color: go3270.Turquoise},
			{Content: fmt.Sprintf("%4s %4s %4s %s", wholeDegrees(h.Temp), fmt.Sprintf("%d%%", h.Precip), wholeNumber(h.Wind), weather.Describe(h.Code)), Color: go3270.Green},
		})...)
	}

	screen = appendMessageRows(screen, rows, cols, problem, problem != "", weatherPrompt, "PF3=Back Enter=Redraw")
	return screen, rows - 1, 0
}

// titleWeather is the fields showing the weather now, from v, in the
// title row just left of the clock (see titleFields), two spaces before
// it, and at least one after a title titleWidth long: the temperature, the
// chance of rain or snow, and the sky, as "72°F 10% Partly cloudy", then,
// above 50, past Good, the air quality index, as "AQI 52". The weather is blue,
// dimmer than the white title and clock beside it (white not intensified
// looks no dimmer on some terminals), and the index the color of its
// category (see aqiColor); on a title row tinted for the busy state, they
// are part of the one tinted field (see tintTitle), like the rest. Short
// of room, the sky is cut first, so that the index shows whole while it
// can. There are none with no weather, or too little room to say anything
// useful.
func titleWeather(cols, titleWidth int, v view) []go3270.Field {
	if v.Weather == nil {
		return nil
	}
	h := v.Weather
	unit := "C"
	if v.Imperial {
		unit = "F"
	}
	_, clockCol := titleClock(cols, titleWidth, v.Now)
	// The first attribute byte goes after a clear column past the title,
	// and the text ends a clear column before the clock's attribute byte,
	// which shows as the second space.
	first, last := titleWidth+2, clockCol-2
	room := last - first // the text's columns, after the attribute byte

	var aqi *go3270.Field
	if h.HasAQI && h.AQI > titleAQIAbove {
		color, intense := aqiColor(h.AQI)
		aqi = &go3270.Field{Row: titleRow, Color: color, Intense: intense, Content: fmt.Sprintf("AQI %d", h.AQI)}
		if n := len(aqi.Content); n > room {
			aqi = nil
		} else {
			room -= n + 1 // and its attribute byte, between it and the weather
		}
	}
	text := fmt.Sprintf("%s%s %d%%", wholeDegrees(h.Temp), unit, h.Precip)
	if sky := room - utf8.RuneCountInString(text) - 1; sky > 0 {
		text += " " + truncate(weather.Describe(h.Code), sky)
	}
	var out []go3270.Field
	if room >= len("72°F") {
		text = truncate(text, room)
		out = append(out, go3270.Field{Row: titleRow, Color: go3270.Blue, Content: text})
	}
	if aqi != nil {
		out = append(out, *aqi)
	}
	// Placed from the right, each after the one before it.
	col := last + 1
	for i := len(out) - 1; i >= 0; i-- {
		col -= 1 + utf8.RuneCountInString(out[i].Content)
		out[i].Col = col
	}
	return out
}

// titleAQIAbove is the air quality index the dashboard's title row shows
// those above: the top of Good, so that it shows once the air is worse.
const titleAQIAbove = 50

// aqiColor is the color the air quality index aqi is shown in, by its
// category: green when good, yellow when moderate, red when unhealthy (for
// sensitive groups, then intensified for everyone), pink when very
// unhealthy, then intensified when hazardous. The EPA's orange, for
// sensitive groups, is red, as the 3270 has no orange.
func aqiColor(aqi int) (color go3270.Color, intense bool) {
	switch weather.AQICategory(aqi) {
	case weather.AQIGood:
		return go3270.Green, false
	case weather.AQIModerate:
		return go3270.Yellow, false
	case weather.AQISensitive:
		return go3270.Red, false
	case weather.AQIUnhealthy:
		return go3270.Red, true
	case weather.AQIVeryUnhealthy:
		return go3270.Pink, false
	}
	return go3270.Pink, true
}

// aqiLines are the weather page's air quality rows for snap, at now:
// the index now, with where it is from, as "Air quality now: 39 Good,
// measured 09:00 by AirNow, Denver" (the area last, to be cut short
// first) or "..., forecast by Open-Meteo" (see weather.Snapshot.AQINow); then the worst of hours, the hours shown,
// with when, as "Worst forecast, next 24 hours: 58 Moderate, Sun 15:00".
// Each index has its category, in its color, and times are in now's zone.
// Either is nil with no index.
func aqiLines(snap weather.Snapshot, hours []weather.Hour, now time.Time) (current, worst line) {
	label := func(s string) go3270.Field { return go3270.Field{Content: s, Color: go3270.Turquoise} }
	value := func(aqi int, comma bool) go3270.Field {
		color, intense := aqiColor(aqi)
		text := fmt.Sprintf("%d %s", aqi, weather.AQICategory(aqi))
		if comma {
			text += ","
		}
		return go3270.Field{Content: text, Color: color, Intense: intense}
	}
	if aqi, obs, ok := snap.AQINow(now); ok {
		source := "forecast by Open-Meteo"
		if obs != nil {
			source = "measured by AirNow, " + obs.Area
			if !obs.Time.IsZero() {
				source = "measured " + obs.Time.In(now.Location()).Format("15:04") + " by AirNow, " + obs.Area
			}
		}
		current = line{label("Air quality now:"), value(aqi, true), {Content: source, Color: go3270.Blue}}
	}
	if w, ok := weather.Worst(hours); ok {
		worst = line{label("Worst forecast, next 24 hours:"), value(w.AQI, true), label(w.Time.In(now.Location()).Format("Mon 15:04"))}
	}
	return current, worst
}

// wholeDegrees is temperature t rounded to a whole degree, with the degree
// sign; never "-0".
func wholeDegrees(t float64) string { return wholeNumber(t) + "°" }

// wholeNumber is x rounded to a whole number; never "-0".
func wholeNumber(x float64) string {
	r := math.Round(x)
	if r == 0 {
		r = 0 // drops the sign of -0
	}
	return fmt.Sprintf("%.0f", r)
}
