package session

import (
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// browsePrompt says how to pick a list to show.
const browsePrompt = "Type any character beside a list to show, then press Enter."

// browseState is one session's Trello lists screen (PF9 on a task screen),
// for picking a list whose cards to show as a task screen.
type browseState struct {
	listPicker

	message string
	isError bool
}

// startBrowse begins the Trello lists screen, reading the lists from src.
func startBrowse(src ListSource) browseState {
	return browseState{listPicker: loadListPicker(src)}
}

// buildBrowse renders the Trello lists screen, and where the cursor goes.
func buildBrowse(rows, cols int, now time.Time, b *browseState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "TRELLO LISTS", now)
	lists, cursorRow, cursorCol := b.build(rows, cols, "Show which list?")
	screen = append(screen, lists...)

	message, color := b.message, go3270.Red
	if message == "" {
		message, color = browsePrompt, go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: b.isError}})...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF7=Up PF8=Down Enter=Show the list selected", cols-1)})
	return screen, cursorRow, cursorCol
}

// handle acts on a key on the Trello lists screen, returning whether to go
// back (PF3), or the list to show (Enter, with one selected).
func (b *browseState) handle(resp go3270.Response) (back bool, show tasks.Destination, ok bool) {
	b.message, b.isError = "", false
	if resp.AID == go3270.AIDPF3 {
		return true, tasks.Destination{}, false
	}
	if bad := b.listPicker.handle(resp); bad != "" {
		b.message, b.isError = bad, true
		return false, tasks.Destination{}, false
	}
	if resp.AID != go3270.AIDEnter {
		return false, tasks.Destination{}, false
	}
	d, ok := b.pickedDest()
	if !ok {
		b.message, b.isError = browsePrompt, true
	}
	return false, d, ok
}
