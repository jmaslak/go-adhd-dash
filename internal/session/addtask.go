package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskAdder adds tasks for the add-task screen. *tasks.Cache is the real
// one.
type TaskAdder interface {
	// Destinations are the Trello lists a task can be added to.
	Destinations() ([]tasks.Destination, error)

	// Add adds a task as a Trello card, returning the task's number.
	Add(ctx context.Context, title string, d tasks.Destination) (int, error)
}

// Add-task screen layout: the title field, the field for the board's number
// below it, then the numbered boards (a Trello board and one of its lists)
// to pick from, down to the message and help rows.
const (
	atTitleRow      = 3
	atTitleLabel    = "Title ===>"
	atBoardRow      = 5
	atBoardsRow     = 7 // the boards' heading
	atBoardLabel    = "Board ===>"
	atBoardWidth    = 3
	atBoardNumWidth = 4 // a board's number, with its attribute byte

	atTitleField = "title"
	atBoardField = "board"

	atPrompt = "Type the title and the board's number, then press Enter."
)

// addTaskTimeout bounds adding one task, most of which is Trello.
const addTaskTimeout = time.Minute

// addTaskState is one session's add-task screen.
type addTaskState struct {
	// dests are the boards as last shown, by number less one.
	dests []tasks.Destination

	// title and board are what was typed, drawn again when it could not
	// be added.
	title, board string

	message string
	isError bool

	// cursorOnBoard puts the cursor on the board's field, which is what
	// the message is about.
	cursorOnBoard bool
}

// buildAddTask renders the add-task screen, and where the cursor goes: the
// title, unless the board is what needs fixing.
func buildAddTask(rows, cols int, now time.Time, adder TaskAdder, a *addTaskState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "ADD TASK", now, false)
	screen = append(screen, placeLine(1, cols, line{{Content: "Added as a card at the bottom of the Trello list picked.", Color: go3270.Blue}})...)

	titleCol := len(atTitleLabel) + 1
	screen = append(screen,
		go3270.Field{Row: atTitleRow, Col: 0, Color: go3270.Turquoise, Content: atTitleLabel},
		go3270.Field{
			Row: atTitleRow, Col: titleCol, Write: true, Name: atTitleField, Content: cutRunes(a.title, cols-titleCol-2),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the title field at the edge of the screen.
		go3270.Field{Row: atTitleRow, Col: cols - 1},
	)

	a.dests = nil
	var loadErr error
	if adder == nil {
		loadErr = errors.New("adding tasks is not available")
	} else {
		a.dests, loadErr = adder.Destinations()
	}
	boardRow, boardCol := atBoardRow, len(atBoardLabel)+1
	switch {
	case loadErr != nil:
		screen = append(screen, placeLine(atBoardsRow, cols, line{{Content: loadErr.Error(), Color: go3270.Red, Intense: true}})...)
	case len(a.dests) == 0:
		screen = append(screen, placeLine(atBoardsRow, cols, line{{
			Content: "No Trello lists are configured (trello: tasks: in ~/.task.yaml).", Color: go3270.Red, Intense: true,
		}})...)
	default:
		heading := fmt.Sprintf("%*s  %s", atBoardNumWidth-1, "No.", "Board / List [tag]")
		screen = append(screen, go3270.Field{
			Row: atBoardsRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: heading + strings.Repeat(" ", max(cols-1-len(heading), 0)),
		})
		// The boards run from below their heading to above the message
		// row, less a blank row.
		limit := max(rows-3-(atBoardsRow+1), 1)
		shown := a.dests
		if len(shown) > limit {
			shown = shown[:limit-1]
		}
		for i, d := range shown {
			screen = append(screen, placeLine(atBoardsRow+1+i, cols, line{
				{Content: fmt.Sprintf("%*d", atBoardNumWidth-1, i+1), Color: go3270.White, Intense: true},
				{Content: d.String(), Color: go3270.Green},
				{Content: "[" + d.Tag + "]", Color: go3270.Turquoise},
			})...)
		}
		if len(shown) < len(a.dests) {
			screen = append(screen, placeLine(atBoardsRow+1+len(shown), cols, line{{
				Content: fmt.Sprintf("     ... and %d more, %d to %d", len(a.dests)-len(shown), len(shown)+1, len(a.dests)), Color: go3270.Blue,
			}})...)
		}
	}

	board := a.board
	if board == "" && len(a.dests) == 1 {
		board = "1"
	}
	screen = append(screen,
		go3270.Field{Row: boardRow, Col: 0, Color: go3270.Turquoise, Content: atBoardLabel},
		go3270.Field{
			Row: boardRow, Col: boardCol, Write: true, Name: atBoardField, Content: board,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the board's field.
		go3270.Field{Row: boardRow, Col: boardCol + 1 + atBoardWidth},
	)

	message, color := a.message, go3270.Red
	if message == "" {
		message, color = atPrompt, go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: a.isError}})...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Cancel Enter=Add", cols-1)})

	if a.cursorOnBoard {
		return screen, boardRow, boardCol + 1
	}
	return screen, atTitleRow, titleCol + 1
}

// handle acts on a key pressed on the add-task screen, reporting whether to
// go back to the task screen, and if a task was added, what to say there. PF3
// leaves; Enter adds the task typed to the board picked, staying with what
// was typed, and why, when it cannot be added.
func (a *addTaskState) handle(resp go3270.Response, adder TaskAdder) (leave bool, added string) {
	a.message, a.isError, a.cursorOnBoard = "", false, false
	if resp.AID == go3270.AIDPF3 {
		return true, ""
	}
	if resp.AID != go3270.AIDEnter {
		return false, ""
	}
	a.title, a.board = resp.Values[atTitleField], resp.Values[atBoardField]
	title := strings.TrimSpace(a.title)
	n, err := strconv.Atoi(strings.TrimSpace(a.board))
	switch {
	case adder == nil || len(a.dests) == 0:
		a.message, a.isError = "There is no board to add a task to.", true
		return false, ""
	case title == "":
		a.message, a.isError = "Type the task's title.", true
		return false, ""
	case err != nil || n < 1 || n > len(a.dests):
		a.message, a.isError, a.cursorOnBoard = fmt.Sprintf("Type a board's number, 1 to %d.", len(a.dests)), true, true
		return false, ""
	}

	d := a.dests[n-1]
	ctx, cancel := context.WithTimeout(context.Background(), addTaskTimeout)
	defer cancel()
	number, err := adder.Add(ctx, title, d)
	if err != nil {
		a.message, a.isError = "Could not add: "+err.Error(), true
		return false, ""
	}
	return true, fmt.Sprintf("Added task %d to %s.", number, d)
}
