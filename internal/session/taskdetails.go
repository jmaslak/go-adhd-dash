package session

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskDetailer reads what a task's Trello card holds beyond its title.
// *tasks.Cache is the real one.
type TaskDetailer interface {
	Details(ctx context.Context, cardID string) (tasks.Details, error)
}

// The task details screen (PF2 on a task screen): a task's card, to look
// at only: its title, list, due date, labels, notes, checklists and
// comments, a page at a time, from the heading down to above the message
// row.
const detailsFirstRow = 2

// taskDetailsState is one session's task details screen.
type taskDetailsState struct {
	task    tasks.Task
	details tasks.Details
	err     error // why the details could not be read
	page    int
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

// detailLines are the lines of the details screen, each fitting width
// columns, at now: long text wrapped, indented under its heading.
func (d *taskDetailsState) detailLines(width int, now time.Time) []line {
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
	text(fmt.Sprintf("Task %d: %s", t.Number, cmp.Or(det.Title, t.Title)), 0, go3270.White, true)
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
		due := line{{Content: "Due " + det.Due.Local().Format("Mon Jan 2 15:04"), Color: go3270.Yellow}}
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
			add(go3270.Field{Content: "  " + c.Author + ", " + c.Date.Local().Format("Mon Jan 2 15:04") + ":", Color: go3270.Blue})
			text(c.Text, 4, go3270.Green, false)
		}
	}
	return out
}

// buildTaskDetails renders a page of the task details, and where the
// cursor goes: the bottom row, as there is nothing to type.
func buildTaskDetails(rows, cols int, now time.Time, d *taskDetailsState) go3270.Screen {
	screen := titleFields(cols, "TASK DETAILS", now)
	lines := d.detailLines(cols-1, now)
	shown, totalPages, start, end := pageRange(len(lines), detailRows(rows), d.page)
	d.page = shown
	for i, l := range lines[start:end] {
		screen = append(screen, placeLine(detailsFirstRow+i, cols, l)...)
	}
	message := "Only to look at. PF3 goes back to the tasks."
	if totalPages > 1 {
		message = fmt.Sprintf("Page %d of %d. PF7 and PF8 page; PF3 goes back to the tasks.", shown+1, totalPages)
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: go3270.Blue}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF7=Up PF8=Down", cols-1)})
}

// handle acts on a key on the task details, returning whether to go back
// to the task screen: PF3 does; PF7 and PF8 page.
func (d *taskDetailsState) handle(resp go3270.Response) (back bool) {
	switch resp.AID {
	case go3270.AIDPF3:
		return true
	case go3270.AIDPF7:
		d.page = max(d.page-1, 0)
	case go3270.AIDPF8:
		d.page++ // the next draw keeps it to the pages there are
	}
	return false
}
