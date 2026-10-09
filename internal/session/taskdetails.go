package session

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskDetailer reads what a task's Trello card holds beyond its title.
// *tasks.Cache is the real one.
type TaskDetailer interface {
	Details(ctx context.Context, cardID string) (tasks.Details, error)
}

// The task details screen (PF1 on a task screen): a task's card: its
// title, typed over to rename it, then, only to look at, its list, due
// date, labels, notes, checklists and comments, a page at a time, from the
// heading down to above the message row. The title is an input field of
// as many rows as it needs, up to detailsTitleMaxRows, on the first page.
const (
	detailsFirstRow     = 2
	detailsTitleField   = "dtitle"
	detailsTitleMaxRows = 4
)

// taskDetailsState is one session's task details screen.
type taskDetailsState struct {
	task    tasks.Task
	details tasks.Details
	err     error // why the details could not be read
	page    int

	// shown is the title as drawn, and typed the title typed over it and
	// not yet saved, drawn again in its place.
	shown string
	typed *string

	// leaveArmed is set by PF3 with a title not saved: a second PF3 leaves
	// without saving it.
	leaveArmed bool

	// message reports the last key's outcome; isError colors it.
	message string
	isError bool
}

// startDetails reads t's details through detailer.
func startDetails(detailer TaskDetailer, t tasks.Task) taskDetailsState {
	d := taskDetailsState{task: t}
	if detailer == nil {
		d.err = fmt.Errorf("task details are not available")
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
	defer cancel()
	d.details, d.err = detailer.Details(ctx, t.CardID)
	return d
}

// detailRows is how many lines of details fit on one page.
func detailRows(rows int) int {
	return max(rows-3-detailsFirstRow, 1)
}

// title is the task's title: as its card has it, or as listed.
func (d *taskDetailsState) title() string {
	return cmp.Or(d.details.Title, d.task.Title)
}

// titleRows is how many rows the title field takes on a screen cols wide
// with perPage lines to a page: enough for the title and room to type
// more, within detailsTitleMaxRows and half a page.
func (d *taskDetailsState) titleRows(cols, perPage int) int {
	need := (utf8.RuneCountInString(d.title())+2)/cols + 1
	return max(min(need, detailsTitleMaxRows, perPage/2), 1)
}

// detailLines are the lines of the details screen, each fitting width
// columns, at now: long text wrapped, indented under its heading. The first
// titleRows are nil, for the title field.
func (d *taskDetailsState) detailLines(width, titleRows int, now time.Time) []line {
	var out []line
	add := func(f ...go3270.Field) { out = append(out, line(f)) }
	blank := func() { add(go3270.Field{}) }
	// text adds s, wrapped, indent columns in, its lines each one field.
	text := func(s string, indent int, color go3270.Color, intense bool) {
		for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
			for _, l := range wrapText(para, max(width-indent, 1)) {
				add(go3270.Field{Content: strings.Repeat(" ", indent) + l, Color: color, Intense: intense})
			}
		}
	}
	heading := func(s string) { add(go3270.Field{Content: s, Color: go3270.Turquoise, Intense: true}) }

	t, det := d.task, d.details
	out = make([]line, titleRows)
	on := line{{Content: "On " + t.Dest.String(), Color: go3270.Blue}}
	if t.Dest.Tag != "" {
		on = append(on, go3270.Field{Content: "[" + t.Dest.Tag + "]", Color: go3270.Turquoise})
	}
	add(on...)
	if d.err != nil {
		blank()
		text("Could not read the details: "+d.err.Error(), 0, go3270.Red, true)
		return out
	}
	if !det.Due.IsZero() {
		due := line{{Content: "Due " + det.Due.In(now.Location()).Format("Mon Jan 2 15:04"), Color: go3270.Yellow}}
		switch {
		case det.DueComplete:
			due = append(due, go3270.Field{Content: "(complete)", Color: go3270.Green})
		case det.Due.Before(now):
			due = append(due, go3270.Field{Content: "(overdue)", Color: go3270.Red, Intense: true})
		}
		add(due...)
	}
	if len(det.Labels) > 0 {
		text("Labels: "+strings.Join(det.Labels, ", "), 0, go3270.Pink, false)
	}

	blank()
	heading("Notes")
	if strings.TrimSpace(det.Notes) == "" {
		add(go3270.Field{Content: "  No notes.", Color: go3270.Blue})
	} else {
		text(strings.TrimRight(det.Notes, "\n"), 2, go3270.Green, false)
	}

	for _, cl := range det.Checklists {
		done := 0
		for _, it := range cl.Items {
			if it.Done {
				done++
			}
		}
		blank()
		heading(fmt.Sprintf("Checklist: %s (%d of %d done)", cl.Name, done, len(cl.Items)))
		for _, it := range cl.Items {
			mark, color := "[ ] ", go3270.Green
			if it.Done {
				mark, color = "[X] ", go3270.Blue
			}
			lines := wrapText(it.Name, max(width-6, 1))
			for i, l := range lines {
				prefix := "      "
				if i == 0 {
					prefix = "  " + mark
				}
				add(go3270.Field{Content: prefix + l, Color: color})
			}
		}
	}

	if len(det.Comments) > 0 {
		blank()
		heading("Comments, newest first")
		for _, c := range det.Comments {
			add(go3270.Field{Content: "  " + c.Author + ", " + c.Date.In(now.Location()).Format("Mon Jan 2 15:04") + ":", Color: go3270.Blue})
			text(c.Text, 4, go3270.Green, false)
		}
	}
	return out
}

// buildTaskDetails renders a page of the task details, and where the
// cursor goes: the title, on the first page; otherwise the bottom row, as
// there is nothing to type.
func buildTaskDetails(rows, cols int, now time.Time, d *taskDetailsState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "TASK DETAILS", now)
	perPage := detailRows(rows)
	titleRows := d.titleRows(cols, perPage)
	lines := d.detailLines(cols-1, titleRows, now)
	shown, totalPages, start, end := pageRange(len(lines), perPage, d.page)
	d.page = shown
	for i, l := range lines[start:end] {
		if l != nil {
			screen = append(screen, placeLine(detailsFirstRow+i, cols, l)...)
		}
	}
	cursorRow, cursorCol = rows-1, 0
	if shown == 0 {
		// From the first row's second column to the last's next to last,
		// where the field ending it is.
		width := titleRows*cols - 2
		d.shown = cutRunes(d.title(), width)
		title := d.shown
		if d.typed != nil {
			title = cutRunes(*d.typed, width)
		}
		last := detailsFirstRow + titleRows - 1
		screen = append(screen,
			go3270.Field{
				Row: detailsFirstRow, Col: 0, Write: true, Name: detailsTitleField, Content: title,
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: last, Col: cols - 1},
		)
		cursorRow, cursorCol = detailsFirstRow, 1
	}

	message, color := d.message, go3270.Red
	if !d.isError {
		color = go3270.Green
		if message == "" {
			color = go3270.Blue
			switch {
			case totalPages == 1:
				message = "Type over the title and press Enter to rename. PF3 goes back to the tasks."
			case shown == 0:
				message = fmt.Sprintf("Page 1 of %d. Type over the title and press Enter to rename; PF8 pages.", totalPages)
			default:
				message = fmt.Sprintf("Page %d of %d. PF7 and PF8 page; PF3 goes back to the tasks.", shown+1, totalPages)
			}
		}
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: d.isError}})...)
	help := "PF3=Back Enter=Save"
	if totalPages > 1 {
		help += " PF7=Up PF8=Down"
	}
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)})
	return screen, cursorRow, cursorCol
}

// handle acts on a key on the task details, returning whether to go back
// to the task screen. Enter renames the task to the title typed over, if
// any, through src; PF7 and PF8 page, keeping it typed; PF3 goes back, a
// second time with a title typed and not saved, which is then dropped.
func (d *taskDetailsState) handle(resp go3270.Response, src TaskSource, logf func(string, ...any)) (back bool) {
	d.message, d.isError = "", false
	armed := d.leaveArmed
	d.leaveArmed = false

	// What is typed: sent back from the first page, or kept from it.
	if v, sent := resp.Values[detailsTitleField]; sent {
		d.typed = &v
		if strings.TrimSpace(v) == strings.TrimSpace(d.shown) {
			d.typed = nil
		}
	}

	switch resp.AID {
	case go3270.AIDPF3:
		if d.typed != nil && !armed {
			d.leaveArmed = true
			d.message, d.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		d.typed = nil
		return true
	case go3270.AIDPF7:
		d.page = max(d.page-1, 0)
	case go3270.AIDPF8:
		d.page++ // the next draw keeps it to the pages there are
	case go3270.AIDEnter:
		d.rename(src, logf)
	}
	return false
}

// rename renames the task to the title typed over it, if any, keeping it
// typed if it cannot.
func (d *taskDetailsState) rename(src TaskSource, logf func(string, ...any)) {
	if d.typed == nil {
		return
	}
	fail := func(msg string) { d.message, d.isError = msg, true }
	title := strings.TrimSpace(*d.typed)
	switch {
	case title == "":
		fail("A title cannot be blank (to finish a task, complete it).")
		return
	case src == nil:
		fail("Renaming is not available.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
	defer cancel()
	if err := src.Rename(ctx, d.task, title); err != nil {
		logf("renaming task %q: %v", d.task.Title, err)
		fail(fmt.Sprintf("%s was not renamed: %v", taskName(d.task), err))
		return
	}
	logf("renamed task %q to %q", d.task.Title, title)
	d.task.Title, d.details.Title, d.typed = title, title, nil
	d.message = fmt.Sprintf("Renamed to %q.", title)
}
