package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskArchiver archives tasks for the task screen. *tasks.Archiver is the
// real one.
type TaskArchiver interface {
	// Check reports why the tasks cannot all be archived, before anything
	// is changed.
	Check(ts []tasks.Task) error

	// Archive closes one task, and its Trello card if it has one.
	Archive(ctx context.Context, t tasks.Task) error
}

// archiveTimeout bounds archiving one task, most of which is Trello.
const archiveTimeout = time.Minute

// Task screen layout: the heading, the column headings, then one task per
// row down to the row above the message row. Each task that can be archived
// has a one-character input field before it for its mark.
const (
	taskHeaderRow   = 2
	taskColumnRow   = 3
	taskFirstRow    = 4
	taskMarkField   = "mark:"
	taskTitleCol    = 2
	taskMarkMessage = "Type X beside each task to archive, then press PF6."
)

// taskPageState is one session's place on the task screen: the page shown,
// the tasks marked for archiving, and those awaiting confirmation.
type taskPageState struct {
	page int

	// marked holds the IDs, in decimal, of the tasks marked for archiving.
	// They are kept by ID because numbers shift when tasks are archived.
	marked map[string]bool

	// confirming are the tasks shown on the confirmation screen, awaiting
	// PF4.
	confirming []tasks.Task

	// message reports the last key's outcome; isError colors it.
	message string
	isError bool
}

// taskMarkName is the name of the input field marking t.
func taskMarkName(t tasks.Task) string {
	return taskMarkField + t.ID.String()
}

// taskRows is how many tasks fit on one page of the task screen.
func taskRows(rows int) int {
	return max(rows-2-taskFirstRow, 1)
}

// buildTaskList renders one page of every open task, each with its mark
// field, and where the cursor goes: the first mark field. page is clamped to
// the pages that exist, and the page shown is returned with the page count.
func buildTaskList(rows, cols int, now time.Time, all []tasks.Task, readErr error, tp *taskPageState) (screen go3270.Screen, shownPage, totalPages, cursorRow, cursorCol int) {
	screen = titleFields(cols, "ALL TASKS", now, false)

	perPage := taskRows(rows)
	totalPages = max((len(all)+perPage-1)/perPage, 1)
	shownPage = min(max(tp.page, 0), totalPages-1)
	start := shownPage * perPage
	pageTasks := all[start:min(start+perPage, len(all))]

	header := line{{Content: "TASKS", Color: go3270.Turquoise, Intense: true}}
	switch {
	case readErr != nil:
		header = append(header, go3270.Field{Content: readErr.Error(), Color: go3270.Red})
	default:
		info := fmt.Sprintf("%d open", len(all))
		if totalPages > 1 {
			info += fmt.Sprintf(", page %d/%d", shownPage+1, totalPages)
		}
		if n := len(tp.marked); n > 0 {
			info += fmt.Sprintf(", %d marked", n)
		}
		header = append(header, go3270.Field{Content: info, Color: go3270.Blue})
	}
	screen = append(screen, placeLine(taskHeaderRow, cols, header)...)
	// Aligned with a task row: the mark in column 1, the number right
	// aligned in columns 3 to 6, and the tags and title from column 8. A
	// field's highlighting runs on to the next attribute byte, so a plain
	// one after the headings keeps the underline from running on to the
	// first task row. With no tasks there is nothing to head.
	if len(all) > 0 {
		const headings = "S  Num Task"
		screen = append(screen,
			go3270.Field{Row: taskColumnRow, Col: 0, Content: headings, Color: go3270.Turquoise, Highlighting: go3270.Underscore},
			go3270.Field{Row: taskColumnRow, Col: 1 + len(headings)},
		)
	}

	cursorRow, cursorCol = rows-1, 0
	for i, t := range pageTasks {
		row := taskFirstRow + i
		if t.ID == nil {
			// Without an ID the task cannot be found again once numbers
			// shift, so it cannot be marked.
			screen = append(screen, go3270.Field{Row: row, Col: 0, Content: " "})
		} else {
			mark := ""
			if tp.marked[t.ID.String()] {
				mark = "X"
			}
			screen = append(screen, go3270.Field{
				Row: row, Col: 0, Write: true, Name: taskMarkName(t), Content: mark,
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			})
			if cursorRow == rows-1 {
				cursorRow, cursorCol = row, 1
			}
		}
		l := taskLine(t)
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, l)...)
	}

	message, color := tp.message, go3270.Red
	if !tp.isError {
		color = go3270.Green
		if message == "" {
			message, color = taskMarkMessage, go3270.Blue
		}
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: tp.isError}})...)
	screen = append(screen, go3270.Field{
		Row: rows - 1, Col: 0, Color: go3270.Blue,
		Content: truncate("PF3=Back PF6=Archive marked PF7=Up PF8=Down Enter=Keep marks", cols-1),
	})
	return screen, shownPage, totalPages, cursorRow, cursorCol
}

// buildArchiveConfirm renders the confirmation for archiving ts.
func buildArchiveConfirm(rows, cols int, now time.Time, ts []tasks.Task) go3270.Screen {
	screen := titleFields(cols, "ARCHIVE TASKS", now, false)

	noun := "tasks"
	if len(ts) == 1 {
		noun = "task"
	}
	screen = append(screen, placeLine(taskHeaderRow, cols, line{{
		Content: fmt.Sprintf("Archive these %d %s?", len(ts), noun), Color: go3270.Yellow, Intense: true,
	}})...)

	// The list runs from below the heading to above the Trello note.
	limit := max(rows-3-(taskHeaderRow+2), 1)
	shown := ts
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := taskHeaderRow + 2
	for _, t := range shown {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, taskLine(t))...)
		row++
	}
	if len(shown) < len(ts) {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(ts)-len(shown)), Color: go3270.Blue}})...)
	}

	trello := 0
	for _, t := range ts {
		if t.TrelloID != "" {
			trello++
		}
	}
	if trello > 0 {
		screen = append(screen, placeLine(rows-3, cols, line{{
			Content: fmt.Sprintf("%d of them mirror Trello cards, which will be marked done and archived.", trello), Color: go3270.Turquoise,
		}})...)
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to archive, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Archive", cols-1)})
	return screen
}

// recordMarks takes the marks typed on the page just shown into tp.marked,
// reporting the first entry that is neither X nor blank. Tasks on other
// pages keep their marks.
func (tp *taskPageState) recordMarks(values map[string]string, all []tasks.Task) string {
	if tp.marked == nil {
		tp.marked = map[string]bool{}
	}
	for _, t := range all {
		if t.ID == nil {
			continue
		}
		v, ok := values[taskMarkName(t)]
		if !ok {
			continue
		}
		switch strings.ToUpper(v) {
		case "":
			delete(tp.marked, t.ID.String())
		case "X":
			tp.marked[t.ID.String()] = true
		default:
			return fmt.Sprintf("Task %d: type X to mark it for archiving, not %q.", t.Number, v)
		}
	}
	return ""
}

// markedTasks are the open tasks marked for archiving, in task order. Marks
// on tasks no longer open are dropped.
func (tp *taskPageState) markedTasks(all []tasks.Task) []tasks.Task {
	var out []tasks.Task
	open := map[string]bool{}
	for _, t := range all {
		if t.ID == nil {
			continue
		}
		open[t.ID.String()] = true
		if tp.marked[t.ID.String()] {
			out = append(out, t)
		}
	}
	for id := range tp.marked {
		if !open[id] {
			delete(tp.marked, id)
		}
	}
	return out
}

// handleList acts on a key pressed on the task list, returning whether to
// move on to the confirmation screen, or leave the task screen altogether.
func (tp *taskPageState) handleList(resp go3270.Response, all []tasks.Task, totalPages int, archiver TaskArchiver) (confirm, leave bool) {
	tp.message, tp.isError = "", false
	if resp.AID == go3270.AIDPF3 {
		return false, true
	}
	if bad := tp.recordMarks(resp.Values, all); bad != "" {
		tp.message, tp.isError = bad, true
		return false, false
	}

	switch resp.AID {
	case go3270.AIDPF7:
		tp.page = max(tp.page-1, 0)
	case go3270.AIDPF8:
		tp.page = min(tp.page+1, totalPages-1)
	case go3270.AIDPF6:
		marked := tp.markedTasks(all)
		switch {
		case len(marked) == 0:
			tp.message, tp.isError = "Nothing is marked. "+taskMarkMessage, true
		case archiver == nil:
			tp.message, tp.isError = "Archiving is not available.", true
		default:
			if err := archiver.Check(marked); err != nil {
				tp.message, tp.isError = "Cannot archive: "+err.Error(), true
				return false, false
			}
			tp.confirming = marked
			return true, false
		}
	}
	return false, false
}

// handleConfirm acts on a key pressed on the confirmation screen, returning
// whether to go back to the task list. Only PF4 archives; PF3 goes back with
// the marks kept, and any other key leaves the confirmation up. Archiving
// stops at the first task that fails, which stays marked.
func (tp *taskPageState) handleConfirm(resp go3270.Response, archiver TaskArchiver, logf func(string, ...any)) (back bool) {
	switch resp.AID {
	case go3270.AIDPF3:
		tp.confirming = nil
		return true
	case go3270.AIDPF4:
	default:
		return false
	}

	done := 0
	tp.message, tp.isError = "", false
	for _, t := range tp.confirming {
		ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
		err := archiver.Archive(ctx, t)
		cancel()
		if err != nil {
			logf("archiving task %d %q: %v", t.Number, t.Title, err)
			tp.message = fmt.Sprintf("Archived %d of %d. Task %d, %q, was not archived: %v", done, len(tp.confirming), t.Number, t.Title, err)
			tp.isError = true
			break
		}
		logf("archived task %d %q", t.Number, t.Title)
		delete(tp.marked, t.ID.String())
		done++
	}
	if !tp.isError {
		noun := "tasks"
		if done == 1 {
			noun = "task"
		}
		tp.message = fmt.Sprintf("Archived %d %s.", done, noun)
	}
	tp.confirming = nil
	return true
}
