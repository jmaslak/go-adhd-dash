package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// ListSource gives the Trello lists a list picker offers. *tasks.Cache is
// the real one.
type ListSource interface {
	// Boards are every open board the user's token can see, with their
	// open lists.
	Boards(ctx context.Context) ([]tasks.Board, error)

	// Destinations are the lists tasks are read from, with their tags.
	Destinations() ([]tasks.Destination, error)
}

// listPickTimeout bounds reading the boards.
const listPickTimeout = time.Minute

// listSelField starts the name of a list's selection field.
const listSelField = "dest:"

// listPicker is a page of every open list on every board, laid out as the
// task list is, each with a selection field, for picking one: where to move
// tasks to, or which list to view.
type listPicker struct {
	// dests are every open list on every board, in Trello's order, each
	// with its tag if its tasks are read; loadErr is why there are none.
	dests   []tasks.Destination
	loadErr error

	page   int
	picked string // the list ID selected; "" for none
}

// loadListPicker reads the lists to pick from src.
func loadListPicker(src ListSource) listPicker {
	var p listPicker
	ctx, cancel := context.WithTimeout(context.Background(), listPickTimeout)
	defer cancel()
	boards, err := src.Boards(ctx)
	if err != nil {
		p.loadErr = err
		return p
	}
	shown, _ := src.Destinations() // only for their tags
	for _, b := range boards {
		for _, l := range b.Lists {
			d := tasks.Destination{BoardID: b.ID, Board: b.Name, ListID: l.ID, List: l.Name}
			if i := slices.IndexFunc(shown, func(s tasks.Destination) bool { return s.ListID == l.ID }); i >= 0 {
				d.Tag = shown[i].Tag
			}
			p.dests = append(p.dests, d)
		}
	}
	if len(p.dests) == 0 {
		p.loadErr = errors.New("there are no open Trello lists")
	}
	return p
}

// listSelName is the name of d's selection field.
func listSelName(d tasks.Destination) string { return listSelField + d.ListID }

// pickedDest is the list picked, and whether there is one.
func (p *listPicker) pickedDest() (tasks.Destination, bool) {
	i := slices.IndexFunc(p.dests, func(d tasks.Destination) bool { return d.ListID == p.picked })
	if i < 0 {
		return tasks.Destination{}, false
	}
	return p.dests[i], true
}

// build renders the page of lists below the title row, headed by heading
// and the page, and where the cursor goes: the first list's selection
// field, or with none, the bottom row. The page is clamped to those there
// are.
func (p *listPicker) build(rows, cols int, heading string) (screen go3270.Screen, cursorRow, cursorCol int) {
	perPage := taskRows(rows)
	totalPages := max((len(p.dests)+perPage-1)/perPage, 1)
	p.page = min(max(p.page, 0), totalPages-1)
	start := p.page * perPage
	pageDests := p.dests[start:min(start+perPage, len(p.dests))]

	header := line{{Content: heading, Color: go3270.Turquoise, Intense: true}}
	if totalPages > 1 {
		header = append(header, go3270.Field{Content: fmt.Sprintf("page %d/%d", p.page+1, totalPages), Color: go3270.Blue})
	}
	screen = append(screen, placeLine(taskHeaderRow, cols, header)...)
	if p.loadErr != nil {
		screen = append(screen, placeLine(taskFirstRow, cols, line{{Content: "Could not read your Trello boards: " + p.loadErr.Error(), Color: go3270.Red, Intense: true}})...)
	}
	if len(pageDests) > 0 {
		// As on the task list, underlined to the first row's attribute byte.
		const headings = "S Board / List [tag, if its tasks are shown]"
		screen = append(screen, go3270.Field{
			Row: taskColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
		})
	}

	cursorRow, cursorCol = rows-1, 0
	for i, d := range pageDests {
		row := taskFirstRow + i
		sel := ""
		if d.ListID == p.picked {
			sel = "S"
		}
		screen = append(screen, go3270.Field{
			Row: row, Col: 0, Write: true, Name: listSelName(d), Content: sel,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		})
		if i == 0 {
			cursorRow, cursorCol = row, 1
		}
		l := line{{Content: d.String(), Color: go3270.Green, Autoskip: true}}
		if d.Tag != "" {
			l = append(l, go3270.Field{Content: "[" + d.Tag + "]", Color: go3270.Turquoise})
		}
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, l)...)
	}
	return screen, cursorRow, cursorCol
}

// handle takes the selection typed on the page just shown, and turns the
// page for PF7 and PF8, reporting more than one list selected. A selection
// on another page is kept unless one is typed on this one.
func (p *listPicker) handle(resp go3270.Response) (bad string) {
	var typed []string
	for _, d := range p.dests {
		v, ok := resp.Values[listSelName(d)]
		switch {
		case !ok:
		case strings.TrimSpace(v) != "":
			typed = append(typed, d.ListID)
		case d.ListID == p.picked:
			p.picked = "" // blanked
		}
	}
	switch len(typed) {
	case 0:
	case 1:
		p.picked = typed[0]
	default:
		return "Select only one list."
	}
	switch resp.AID {
	case go3270.AIDPF7:
		p.page--
	case go3270.AIDPF8:
		p.page++
	}
	return ""
}
