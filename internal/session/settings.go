package session

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The settings screen (the SETTINGS command): a user's own settings, one
// option per row, each with how it stands, then the option field, as the
// admin menu is laid out.
const (
	settingsOptionField = "option"
	settingsFirstRow    = 4
)

// settingsOptions are the settings screen's options, by the number typed to
// choose one, with the command each runs.
var settingsOptions = []struct{ number, what, command string }{
	{"1", "Password: change it", "password"},
	{"2", "Google calendar: connect, choose calendars", "google"},
	{"3", "Trello: link, choose your task lists", "trello"},
	{"4", "Preferences: how the screens behave", "preferences"},
}

// settingsStatus is how each of the user with id's settings stands, by
// its option's command, from store: "" where there is nothing to say.
func settingsStatus(store *users.Store, id int) map[string]string {
	out := map[string]string{}
	if store == nil {
		return out
	}
	list, gclient, gerr := store.GoogleClient()
	_, tclient, terr := store.TrelloClient()
	if err := errors.Join(gerr, terr); err != nil {
		out["google"] = "unknown: " + err.Error()
		return out
	}
	i := slices.IndexFunc(list, func(u users.User) bool { return u.ID == id })
	if i < 0 {
		return out
	}
	u := list[i]
	switch g := u.Google; {
	case g == nil || g.RefreshToken == "":
		out["google"] = "not connected"
	case gclient == nil || g.ClientID != gclient.ClientID:
		out["google"] = "to connect again"
	default:
		out["google"] = "connected, " + countText(len(g.Calendars), "calendar", 0, 1) + " shown"
	}
	switch {
	case u.Trello == nil || u.Trello.Token == "":
		out["trello"] = "not linked"
	case !u.TrelloLinked(tclient):
		out["trello"] = "to link again"
	default:
		out["trello"] = "linked, " + countText(len(u.Trello.Lists), "list", 0, 1)
	}
	if u.Preferences.NoFlash {
		out["preferences"] = "no flashing"
	}
	return out
}

// buildSettings renders the settings screen for the user called name, each
// option with status's word on it, with message below the option field,
// red if isError, and where the cursor goes: the option field.
func buildSettings(rows, cols int, now time.Time, name string, status map[string]string, message string, isError bool) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "SETTINGS", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "YOUR SETTINGS", Color: go3270.Turquoise, Intense: true},
		{Content: "(" + name + ")", Color: go3270.Blue},
	})...)
	for i, o := range settingsOptions {
		l := line{
			{Content: fmt.Sprintf("%3s", o.number), Color: go3270.White, Intense: true},
			{Content: o.what, Color: go3270.Green},
		}
		if s := status[o.command]; s != "" {
			l = append(l, go3270.Field{Content: "(" + s + ")", Color: go3270.Blue})
		}
		screen = append(screen, placeLine(settingsFirstRow+i, cols, l)...)
	}

	optionRow, optionCol := rows-3, len(adminOptionLabel)+1
	screen = append(screen,
		go3270.Field{Row: optionRow, Col: 0, Color: go3270.Turquoise, Content: adminOptionLabel},
		go3270.Field{
			Row: optionRow, Col: optionCol, Write: true, Name: settingsOptionField,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the option field after a few columns.
		go3270.Field{Row: optionRow, Col: optionCol + 4},
	)
	switch {
	case message == "":
		screen = append(screen, placeLine(rows-2, cols, line{{Content: "Type an option's number and press Enter.", Color: go3270.Blue}})...)
	case isError:
		screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: go3270.Red, Intense: true}})...)
	default:
		screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: go3270.White, Intense: true}})...)
	}
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back Enter=Select", cols-1)})
	return screen, optionRow, optionCol + 1
}

// settingsChoice is what a key on the settings screen asks for: the command
// of the option chosen, leave for PF3, or "" to stay, with a message saying
// why when what was typed is not an option.
func settingsChoice(resp go3270.Response) (command string, leave bool, message string) {
	switch resp.AID {
	case go3270.AIDPF3:
		return "", true, ""
	case go3270.AIDEnter:
	default:
		return "", false, ""
	}
	typed := strings.TrimSpace(resp.Values[settingsOptionField])
	if typed == "" {
		return "", false, ""
	}
	for _, o := range settingsOptions {
		if o.number == typed {
			return o.command, false, ""
		}
	}
	return "", false, fmt.Sprintf("There is no option %q.", typed)
}
