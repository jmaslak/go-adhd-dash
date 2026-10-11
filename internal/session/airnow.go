// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
)

// The admin's screen for the AirNow API key, which the weather's air
// quality is measured through: what AirNow is, then the key's field.
const (
	airNowKeyField = "airnowkey"
	airNowKeyLabel = "API key ===>"
	airNowKeyMax   = 64 // AirNow's keys are UUIDs, 36 characters

	// airNowCheckTimeout bounds checking a key typed with AirNow.
	airNowCheckTimeout = 15 * time.Second
)

// airNowText explains the screen.
var airNowText = []string{
	"AirNow (airnow.gov), run by the EPA, measures the air quality at monitors",
	"in the United States, Puerto Rico, Canada and Mexico. With its API key set,",
	"the weather screen and the dashboard show the air quality measured there",
	"now; without, or elsewhere, as forecast by Open-Meteo. Get a key, free, at",
	"https://docs.airnowapi.org/ (Request an AirNow API Account).",
}

// AirNowChecker checks an AirNow API key with AirNow (see weather.Cache).
type AirNowChecker interface {
	CheckAirNowKey(ctx context.Context, key string) error
}

// airNowKeyState is one session's place on the AirNow API key screen.
type airNowKeyState struct {
	// typed is what was typed when it could not be saved, drawn again; nil
	// for the key kept.
	typed *string

	// leaveArmed is set by PF3 with a change typed: a second PF3 leaves.
	leaveArmed bool

	message string
	isError bool
}

// buildAirNowKey renders the AirNow API key screen for the key kept, ""
// for none, and where the cursor goes: the key's field.
func buildAirNowKey(rows, cols int, now time.Time, key string, loadErr error, k *airNowKeyState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "AIRNOW API KEY", now)
	header := line{{Content: "AirNow API key", Color: go3270.Turquoise, Intense: true}}
	switch {
	case loadErr != nil:
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	case key == "":
		header = append(header, go3270.Field{Content: "none set: the air quality is forecast", Color: go3270.Yellow})
	default:
		header = append(header, go3270.Field{Content: "set: the air quality is measured", Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	for i, t := range airNowText {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t, Color: go3270.Green}})...)
	}
	if k.typed != nil {
		key = *k.typed
	}
	row, col := 5+len(airNowText), len(airNowKeyLabel)+1
	screen = append(screen,
		go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: airNowKeyLabel},
		go3270.Field{
			Row: row, Col: col, Write: true, Name: airNowKeyField, Content: cutRunes(key, cols-col-2),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: row, Col: cols - 1},
	)
	screen = append(screen, placeLine(row+1, cols, line{{Content: "Blank the key to remove it.", Color: go3270.Blue}})...)
	screen = appendMessageRows(screen, rows, cols, k.message, k.isError,
		"Type AirNow's API key, then press Enter; it is checked with AirNow.", "PF3=Back Enter=Save")
	return screen, row, col + 1
}

// handle acts on a key on the AirNow API key screen, returning whether to
// leave for the admin menu: Enter saves the key typed, once check (nil for
// none) finds AirNow does not refuse it, and blank removes it; PF3 leaves,
// a second time with a change typed.
func (k *airNowKeyState) handle(resp go3270.Response, store *users.Store, check AirNowChecker, logf func(string, ...any)) (leave bool) {
	k.message, k.isError = "", false
	armed := k.leaveArmed
	k.leaveArmed, k.typed = false, nil

	current, err := store.AirNowKey()
	if err != nil {
		k.message, k.isError = "Could not read the key: "+err.Error(), true
		return false
	}
	raw := resp.Values[airNowKeyField]
	typed := strings.TrimSpace(raw)
	keep := func() { k.typed = &raw }

	switch resp.AID {
	case go3270.AIDPF3:
		if typed != current && !armed {
			keep()
			k.leaveArmed = true
			k.message, k.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}

	switch {
	case typed == current:
		return false
	case typed == "":
		if err := store.SetAirNowKey(""); err != nil {
			k.message, k.isError = "Could not save: "+err.Error(), true
			return false
		}
		logf("removed the AirNow API key")
		k.message = "Removed the AirNow API key; the air quality is forecast now."
		return false
	case utf8.RuneCountInString(typed) > airNowKeyMax || strings.ContainsFunc(typed, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		keep()
		k.message, k.isError = fmt.Sprintf("That is not an AirNow API key: at most %d characters, and no spaces.", airNowKeyMax), true
		return false
	}

	// Checked before it is saved, so that a key mistyped is not kept; one
	// that cannot be checked, AirNow being out of reach, is kept anyway.
	unchecked := ""
	if check != nil {
		ctx, cancel := context.WithTimeout(context.Background(), airNowCheckTimeout)
		err := check.CheckAirNowKey(ctx, typed)
		cancel()
		switch {
		case errors.Is(err, weather.ErrBadKey):
			// Just AirNow's reason, after the error's prefixes.
			keep()
			msg := err.Error()
			k.message, k.isError = "AirNow refused the key: "+msg[strings.LastIndex(msg, ": ")+2:], true
			return false
		case err != nil:
			unchecked = err.Error()
		}
	}
	if err := store.SetAirNowKey(typed); err != nil {
		keep()
		k.message, k.isError = "Could not save: "+err.Error(), true
		return false
	}
	if unchecked != "" {
		logf("set the AirNow API key, unchecked: %s", unchecked)
		k.message = "Saved, but AirNow could not be asked whether it takes it: " + unchecked
		return false
	}
	logf("set the AirNow API key")
	k.message = "Saved; AirNow takes it."
	return false
}
