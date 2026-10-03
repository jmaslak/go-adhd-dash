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

// The preferences screen (option 4 on the settings screen): a user's
// choices of how the screens behave, one per row, each a Y or N field with
// what it does beside it.
const (
	prefNoFlashField = "prefnoflash"
	prefFirstRow     = 4
	prefLabelWidth   = 21 // "Avoid flashing ===>", and the attribute byte after it
	prefPrompt       = "Type Y or N, then press Enter to save."
)

// preferencesState is one session's preferences screen.
type preferencesState struct {
	typed map[string]string // what was typed, drawn again when it will not do

	message string
	isError bool
}

// prefsOf is the preferences of the user with id, as saved in store; none
// set when there is no store, or no such user.
func prefsOf(store *users.Store, id int) users.Preferences {
	if store == nil {
		return users.Preferences{}
	}
	list, _, err := store.Load()
	if err != nil {
		return users.Preferences{}
	}
	if i := slices.IndexFunc(list, func(u users.User) bool { return u.ID == id }); i >= 0 {
		return list[i].Preferences
	}
	return users.Preferences{}
}

// buildPreferences renders the preferences screen for the user called
// name, whose preferences are prefs, and where the cursor goes.
func buildPreferences(rows, cols int, now time.Time, name string, prefs users.Preferences, p *preferencesState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "PREFERENCES", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "Your preferences", Color: go3270.Turquoise, Intense: true},
		{Content: "(" + name + ")", Color: go3270.Blue},
	})...)
	value := yesNo(prefs.NoFlash)
	if v, ok := p.typed[prefNoFlashField]; ok {
		value = v
	}
	screen = append(screen,
		go3270.Field{Row: prefFirstRow, Col: 0, Color: go3270.Turquoise, Content: "Avoid flashing ===>"},
		go3270.Field{
			Row: prefFirstRow, Col: prefLabelWidth - 1, Write: true, Name: prefNoFlashField, Content: value,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: prefFirstRow, Col: prefLabelWidth + 1, Autoskip: true,
			Color: go3270.Green, Content: "Y: nothing flashes; the timer shows DONE steadily."},
	)
	screen = appendMessageRows(screen, rows, cols, p.message, p.isError, prefPrompt, "PF3=Back Enter=Save")
	return screen, prefFirstRow, prefLabelWidth
}

// handle acts on a key on the preferences screen for the user u, returning
// whether to go back to the settings screen, and what to say there. Enter
// saves what was typed; PF3 goes back without saving.
func (p *preferencesState) handle(resp go3270.Response, store *users.Store, u *users.User, logf func(string, ...any)) (back bool, said string) {
	p.message, p.isError = "", false
	switch resp.AID {
	case go3270.AIDPF3:
		return true, ""
	case go3270.AIDEnter:
	default:
		return false, ""
	}
	p.typed = map[string]string{prefNoFlashField: resp.Values[prefNoFlashField]}
	var noFlash bool
	switch v := strings.ToUpper(strings.TrimSpace(resp.Values[prefNoFlashField])); v {
	case "Y":
		noFlash = true
	case "N", "":
	default:
		p.message, p.isError = fmt.Sprintf("Type Y or N to avoid flashing, not %q.", v), true
		return false, ""
	}
	changed := false
	errGone := errors.New("your user has been removed")
	err := store.Update(func(list *[]users.User, _ func() int) error {
		i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == u.ID })
		if i < 0 {
			return errGone
		}
		changed = (*list)[i].Preferences.NoFlash != noFlash
		(*list)[i].Preferences.NoFlash = noFlash
		return nil
	})
	if err != nil {
		p.message, p.isError = "Could not save: "+err.Error()+".", true
		return false, ""
	}
	p.typed = nil
	if !changed {
		return true, "Your preferences are unchanged."
	}
	logf("%q set avoid flashing to %v", u.Name, noFlash)
	return true, "Your preferences are saved."
}
