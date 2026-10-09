package session

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The preferences screen (option 4 on the settings screen): a user's
// general preferences, one per row, each a field with what it
// does beside it: a Y or N, or the name of a time zone; then the calendar
// screen's clocks, a time zone and a name on each row.
const (
	prefNoFlashField = "prefnoflash"
	prefZoneField    = "prefzone"
	prefFirstRow     = 4
	prefZoneRow      = prefFirstRow + 2
	prefLabelWidth   = 21 // "Avoid flashing ===>", and the attribute byte after it
	prefZoneWidth    = 40 // the longest IANA zone name is 32
	prefPrompt       = "Make your changes, then press Enter to save."

	// The clocks: a heading, the columns' headings, then a row for each,
	// its zone field and then its name field.
	prefClocksRow       = prefZoneRow + 3
	prefClockFirstRow   = prefClocksRow + 2
	prefClockZoneField  = "prefczone" // and the clock's number, from 1
	prefClockNameField  = "prefcname"
	prefClockZoneWidth  = 32
	prefClockNameCol    = prefLabelWidth + prefClockZoneWidth + 2 // its attribute byte the column before
	prefClockLabelWidth = 19                                      // "Clock 10 =========>"

	// zoneListRow is the time zone list's first row.
	zoneListRow = 4
)

// clockField is the name of clock n's field (from 1) whose name starts
// with prefix.
func clockField(prefix string, n int) string { return prefix + strconv.Itoa(n) }

// preferencesState is one session's preferences screen.
type preferencesState struct {
	typed map[string]string // what was typed, drawn again when it will not do

	// listing is set while the time zone list (PF1 on a time zone) shows,
	// at page of pages; a page of -1 is the one listing the zone typed in
	// zoneField, found when it is drawn. back puts the cursor back on
	// zoneField once the list is left.
	listing     bool
	page, pages int
	zoneField   string
	back        bool

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
	stored := storedPrefs(prefs)
	value := func(field string) string {
		if v, ok := p.typed[field]; ok {
			return v
		}
		return stored[field]
	}
	if p.listing {
		return buildZoneList(rows, cols, now, strings.TrimSpace(value(p.zoneField)), p)
	}
	screen = titleFields(cols, "PREFERENCES", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "Your preferences", Color: go3270.Turquoise, Intense: true},
		{Content: "(" + name + ")", Color: go3270.Blue},
	})...)
	input := func(row, col int, field string, width int) go3270.Field {
		return go3270.Field{
			Row: row, Col: col - 1, Write: true, Name: field, Content: cutRunes(value(field), width),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		}
	}
	hint := func(row int, text string, color go3270.Color) go3270.Field {
		return go3270.Field{Row: row, Col: prefLabelWidth, Color: color, Content: truncate(text, cols-prefLabelWidth-1)}
	}
	screen = append(screen,
		go3270.Field{Row: prefFirstRow, Col: 0, Color: go3270.Turquoise, Content: "Avoid flashing ===>"},
		input(prefFirstRow, prefLabelWidth, prefNoFlashField, 1),
		go3270.Field{Row: prefFirstRow, Col: prefLabelWidth + 1, Autoskip: true,
			Color: go3270.Green, Content: "Y: nothing flashes; the timer shows DONE steadily."},
		go3270.Field{Row: prefZoneRow, Col: 0, Color: go3270.Turquoise, Content: "Time zone ========>"},
		input(prefZoneRow, prefLabelWidth, prefZoneField, prefZoneWidth),
		go3270.Field{Row: prefZoneRow, Col: prefLabelWidth + prefZoneWidth, Autoskip: true},
		hint(prefZoneRow+1, "Times are shown in "+zoneText(locationOf(prefs), now)+".", go3270.Blue),
		hint(prefZoneRow+2, "Blank for the server's: "+zoneText(time.Local, now)+".", go3270.Green),
	)

	screen = append(screen,
		go3270.Field{Row: prefClocksRow, Col: 0, Color: go3270.Turquoise, Intense: true, Content: "Calendar clocks"},
		go3270.Field{Row: prefClocksRow, Col: prefLabelWidth - 1, Color: go3270.Green,
			Content: truncate(fmt.Sprintf("Up to %d, in order. Blank them all for none.", users.MaxClocks), cols-prefLabelWidth)},
		go3270.Field{Row: prefClocksRow + 1, Col: prefLabelWidth - 1, Color: go3270.Blue, Content: "Time zone (PF1 on one lists them)"},
		go3270.Field{Row: prefClocksRow + 1, Col: prefClockNameCol - 1, Color: go3270.Blue,
			Content: truncate("Name; blank for the city", cols-prefClockNameCol)},
	)
	for n := 1; n <= users.MaxClocks; n++ {
		row := prefClockFirstRow + n - 1
		if row > rows-3 {
			break
		}
		label := fmt.Sprintf("Clock %d ", n)
		label += strings.Repeat("=", prefClockLabelWidth-1-len(label)) + ">"
		screen = append(screen,
			go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: label},
			input(row, prefLabelWidth, clockField(prefClockZoneField, n), prefClockZoneWidth),
			go3270.Field{Row: row, Col: prefLabelWidth + prefClockZoneWidth, Autoskip: true},
			input(row, prefClockNameCol, clockField(prefClockNameField, n), clockNameWidth),
			go3270.Field{Row: row, Col: prefClockNameCol + clockNameWidth, Autoskip: true},
		)
	}

	screen = appendMessageRows(screen, rows, cols, p.message, p.isError, prefPrompt, "PF1=List zones PF3=Back Enter=Save")
	if p.back {
		if row, ok := zoneFieldRow(p.zoneField); ok {
			return screen, row, prefLabelWidth
		}
	}
	return screen, prefFirstRow, prefLabelWidth
}

// storedPrefs is what prefs puts in the preferences screen's fields: the
// clocks shown, which are the usual ones until the user chooses others.
func storedPrefs(prefs users.Preferences) map[string]string {
	out := map[string]string{prefNoFlashField: yesNo(prefs.NoFlash), prefZoneField: prefs.TimeZone}
	for i, c := range clocksOf(prefs) {
		out[clockField(prefClockZoneField, i+1)] = c.Zone
		out[clockField(prefClockNameField, i+1)] = c.Name
	}
	return out
}

// zoneFieldRow is the row of the time zone field called name, and whether
// there is one.
func zoneFieldRow(name string) (row int, ok bool) {
	if name == prefZoneField {
		return prefZoneRow, true
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(name, prefClockZoneField)); err == nil && n >= 1 && n <= users.MaxClocks && strings.HasPrefix(name, prefClockZoneField) {
		return prefClockFirstRow + n - 1, true
	}
	return 0, false
}

// zoneFieldAt is the name of the time zone field at row, col, as the
// cursor reports it, and whether there is one there.
func zoneFieldAt(row, col int) (string, bool) {
	width := prefClockZoneWidth
	name := prefZoneField
	switch {
	case row == prefZoneRow:
		width = prefZoneWidth
	case row >= prefClockFirstRow && row < prefClockFirstRow+users.MaxClocks:
		name = clockField(prefClockZoneField, row-prefClockFirstRow+1)
	default:
		return "", false
	}
	if col < prefLabelWidth-1 || col > prefLabelWidth+width {
		return "", false
	}
	return name, true
}

// buildZoneList renders the time zone list, in columns, a page at a time,
// with zone, the one typed, picked out, and where the cursor goes.
func buildZoneList(rows, cols int, now time.Time, zone string, p *preferencesState) (screen go3270.Screen, cursorRow, cursorCol int) {
	perColumn := max(rows-2-zoneListRow-1, 1) // a blank row above the message rows
	pages := zonePages(perColumn, cols-1)
	if p.page < 0 {
		p.page = zonePageOf(pages, zone)
	}
	p.page, p.pages = min(p.page, len(pages)-1), len(pages)
	page := pages[p.page]

	screen = titleFields(cols, "TIME ZONES", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "Time zones", Color: go3270.Turquoise, Intense: true},
		{Content: fmt.Sprintf("(page %d of %d; type one on the preferences screen)", p.page+1, p.pages), Color: go3270.Blue},
	})...)
	col, i := 0, page.first
	for _, w := range page.widths {
		for row := zoneListRow; row < zoneListRow+perColumn && i < page.first+page.count; row++ {
			color, intense := go3270.Green, false
			if zoneNames[i] == zone {
				color, intense = go3270.White, true
			}
			screen = append(screen, placeLineAt(row, col, cols, line{{Content: zoneNames[i], Color: color, Intense: intense}})...)
			i++
		}
		col += w + zoneColumnGap
	}

	prompt := "PF7 and PF8 page; PF3 goes back."
	switch {
	case p.pages == 1:
		prompt = "PF3 goes back."
	case p.page == 0:
		prompt = "PF8 for more; PF3 goes back."
	}
	help := "PF3=Back"
	if p.pages > 1 {
		help += " PF7=Up PF8=Down"
	}
	screen = appendMessageRows(screen, rows, cols, p.message, p.isError, prompt, help)
	return screen, rows - 1, 0
}

// handle acts on a key on the preferences screen for the user u, returning
// whether to go back to the settings screen, and what to say there. Enter
// saves what was typed; PF3 goes back without saving; PF1 on a time zone
// lists them, keeping what was typed.
func (p *preferencesState) handle(resp go3270.Response, store *users.Store, u *users.User, logf func(string, ...any)) (back bool, said string) {
	p.message, p.isError = "", false
	if p.listing {
		p.handleZoneList(resp)
		return false, ""
	}
	p.back = false
	switch resp.AID {
	case go3270.AIDPF3:
		return true, ""
	case go3270.AIDPF1:
		p.keepTyped(resp.Values)
		field, ok := zoneFieldAt(resp.Row, resp.Col)
		if !ok {
			p.message = "PF1 lists the time zones when the cursor is on a time zone."
			return false, ""
		}
		p.listing, p.page, p.zoneField = true, -1, field
		return false, ""
	case go3270.AIDEnter:
	default:
		return false, ""
	}
	p.keepTyped(resp.Values)
	want, bad := parsePrefs(resp.Values)
	if bad != "" {
		p.message, p.isError = bad, true
		return false, ""
	}
	var was users.Preferences
	errGone := errors.New("your user has been removed")
	err := store.Update(func(list *[]users.User, _ func() int) error {
		i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == u.ID })
		if i < 0 {
			return errGone
		}
		prefs := &(*list)[i].Preferences
		was = *prefs
		prefs.NoFlash, prefs.TimeZone, prefs.Clocks = want.NoFlash, want.TimeZone, want.Clocks
		return nil
	})
	if err != nil {
		p.message, p.isError = "Could not save: "+err.Error()+".", true
		return false, ""
	}
	p.typed = nil
	sameClocks := slices.Equal(was.Clocks, want.Clocks) && (was.Clocks == nil) == (want.Clocks == nil)
	if was.NoFlash == want.NoFlash && was.TimeZone == want.TimeZone && sameClocks {
		return true, "Your preferences are unchanged."
	}
	if was.NoFlash != want.NoFlash {
		logf("%q set avoid flashing to %v", u.Name, want.NoFlash)
	}
	if was.TimeZone != want.TimeZone {
		logf("%q set time zone to %q", u.Name, want.TimeZone)
	}
	if !sameClocks {
		logf("%q set calendar clocks to %v", u.Name, clocksOf(want))
	}
	return true, "Your preferences are saved."
}

// keepTyped keeps what was typed in each of the screen's fields, to draw
// again.
func (p *preferencesState) keepTyped(values map[string]string) {
	p.typed = map[string]string{prefNoFlashField: values[prefNoFlashField], prefZoneField: values[prefZoneField]}
	for n := 1; n <= users.MaxClocks; n++ {
		for _, f := range []string{clockField(prefClockZoneField, n), clockField(prefClockNameField, n)} {
			p.typed[f] = values[f]
		}
	}
}

// parsePrefs is the preferences typed in values, or what is wrong with
// them. Clocks left blank are dropped, the rest kept in order; the usual
// clocks are kept as none chosen, so that they follow any change to them.
func parsePrefs(values map[string]string) (want users.Preferences, bad string) {
	switch v := strings.ToUpper(strings.TrimSpace(values[prefNoFlashField])); v {
	case "Y":
		want.NoFlash = true
	case "N", "":
	default:
		return want, fmt.Sprintf("Type Y or N to avoid flashing, not %q.", v)
	}
	want.TimeZone = strings.TrimSpace(values[prefZoneField])
	if _, err := loadZone(want.TimeZone); err != nil {
		return want, fmt.Sprintf("There is no time zone %q; type one such as America/Denver, or leave it blank.", want.TimeZone)
	}
	want.Clocks = []users.Clock{}
	for n := 1; n <= users.MaxClocks; n++ {
		c := users.Clock{
			Zone: strings.TrimSpace(values[clockField(prefClockZoneField, n)]),
			Name: cutRunes(strings.TrimSpace(values[clockField(prefClockNameField, n)]), clockNameWidth),
		}
		switch _, err := loadZone(c.Zone); {
		case c.Zone == "" && c.Name == "":
			continue
		case c.Zone == "":
			return want, fmt.Sprintf("Clock %d is named %q, but has no time zone.", n, c.Name)
		case err != nil:
			return want, fmt.Sprintf("Clock %d: there is no time zone %q; PF1 on it lists them.", n, c.Zone)
		}
		want.Clocks = append(want.Clocks, c)
	}
	if slices.Equal(want.Clocks, defaultClocks) {
		want.Clocks = nil
	}
	return want, ""
}

// handleZoneList acts on a key on the time zone list: PF7 and PF8 page;
// PF3 goes back to the preferences, as they were typed.
func (p *preferencesState) handleZoneList(resp go3270.Response) {
	switch resp.AID {
	case go3270.AIDPF3:
		p.listing, p.back = false, true
	case go3270.AIDPF7:
		p.page = max(p.page-1, 0)
	case go3270.AIDPF8:
		p.page = min(p.page+1, max(p.pages-1, 0))
	default:
		p.message = "PF7 and PF8 page; PF3 goes back to your preferences."
	}
}
