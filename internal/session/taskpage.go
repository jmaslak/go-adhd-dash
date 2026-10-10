// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// TaskArchiver archives tasks for the task screen. *tasks.Cache is the real
// one.
type TaskArchiver interface {
	// Archive closes one task's Trello card.
	Archive(ctx context.Context, t tasks.Task) error
}

// TaskSource is what the task screen shows and changes: the user's tasks,
// a *tasks.Cache, or the cards on one Trello list, a *listView.
type TaskSource interface {
	// Snapshot is the tasks to show.
	Snapshot() tasks.Snapshot

	TaskArchiver
	TaskMover

	// Rename changes one task's title.
	Rename(ctx context.Context, t tasks.Task, title string) error

	// Reposition moves one task within its list: to "top", "bottom", or a
	// position as Trello numbers them.
	Reposition(ctx context.Context, t tasks.Task, pos string) error
}

// taskChangeTimeout bounds one change to a task, most of which is Trello.
const taskChangeTimeout = time.Minute

// Task screen layout: the heading, the column headings, then one task per
// row down to the row above the message row. Each task has a
// one-character input field before it for its mark, then its tags and
// title, only to look at (PF1's details screen renames it).
const (
	taskHeaderRow   = 2
	taskColumnRow   = 3
	taskFirstRow    = 4
	taskMarkField   = "mark:"
	taskTitleCol    = 2
	taskMarkMessage = "Mark tasks with X: PF5 moves them, PF6 completes. PF1 shows one, to rename it."
)

// taskPageState is one session's place on a task screen: which tasks it
// shows (view), the page shown, the tasks marked, and those awaiting
// confirmation.
type taskPageState struct {
	// view is the Trello list shown, picked with PF9; nil for the user's
	// tasks.
	view *listView

	page int

	// marked holds the card IDs of the tasks marked, to move or archive.
	// They are kept by card because numbers shift when tasks are archived.
	marked map[string]bool

	// rowCards is the card on each row, from the last screen drawn.
	rowCards map[int]string

	// confirming are the tasks shown on the confirmation for archiving,
	// awaiting PF4.
	confirming []tasks.Task

	// moving are the tasks marked when PF5 was pressed, for the move-task
	// screen.
	moving []tasks.Task

	// detailsOf is the task whose details PF1 asked for.
	detailsOf tasks.Task

	// follow is the card the cursor goes to, once moved up or down.
	follow string

	// message reports the last key's outcome; isError colors it.
	message string
	isError bool
}

// source is what tp shows and changes: the Trello list it shows, or own,
// the user's tasks (see Config.ownTasks); nil when own is.
func (tp *taskPageState) source(own TaskSource) TaskSource {
	if tp.view != nil {
		return tp.view
	}
	return own
}

// adder adds tasks for tp's add-task screen: to the Trello list it shows,
// or to the lists of the user's tasks, through backend.
func (tp *taskPageState) adder(backend TaskBackend) TaskAdder {
	switch {
	case tp.view != nil:
		return listAdder{tp.view}
	case backend == nil:
		return nil
	}
	return backend
}

// taskMarkName is the name of the input field marking t.
func taskMarkName(t tasks.Task) string {
	return taskMarkField + t.CardID
}

// taskRows is how many tasks fit on one page of the task screen.
func taskRows(rows int) int {
	return max(rows-2-taskFirstRow, 1)
}

// taskPrefix is t's tags, each in brackets; "" for none.
func taskPrefix(t tasks.Task) string {
	if len(t.Tags) == 0 {
		return ""
	}
	return "[" + strings.Join(t.Tags, "] [") + "]"
}

// taskName is t's title quoted, cut to fit in a message.
func taskName(t tasks.Task) string {
	return strconv.Quote(cutRunes(t.Title, 40))
}

// buildTaskList renders one page of the tasks in snap, each with its mark
// field and its title, and where the cursor goes: the first
// mark field, or the task followed. page is clamped to the pages that
// exist, and the page shown is returned with the page count.
func buildTaskList(rows, cols int, now time.Time, snap tasks.Snapshot, tp *taskPageState) (screen go3270.Screen, shownPage, totalPages, cursorRow, cursorCol int) {
	all := snap.Tasks
	perPage := taskRows(rows)
	totalPages = max((len(all)+perPage-1)/perPage, 1)
	if i := slices.IndexFunc(all, func(t tasks.Task) bool { return t.CardID == tp.follow }); i >= 0 {
		tp.page = i / perPage
	}
	shownPage = min(max(tp.page, 0), totalPages-1)
	start := shownPage * perPage
	pageTasks := all[start:min(start+perPage, len(all))]

	var header line
	if tp.view == nil {
		screen = titleFields(cols, "ALL TASKS", now)
		header = line{{Content: "TASKS", Color: go3270.Turquoise, Intense: true}}
	} else {
		screen = titleFields(cols, "TRELLO LIST", now)
		header = line{{Content: tp.view.dest.String(), Color: go3270.Turquoise, Intense: true}}
	}
	status, ok := tasksStatus(snap.Err, snap.Fetched.In(now.Location()), snap.Loading)
	if ok {
		// Recurring tasks not yet due are listed, to edit, but not open.
		info := fmt.Sprintf("%d open", len(all)-countFunc(all, notYetDue))
		if totalPages > 1 {
			info += fmt.Sprintf(", page %d/%d", shownPage+1, totalPages)
		}
		if n := len(tp.marked); n > 0 {
			info += fmt.Sprintf(", %d marked", n)
		}
		header = append(header, go3270.Field{Content: info, Color: go3270.Blue})
	}
	if tp.view != nil {
		if tag := tp.view.dest.Tag; tag == "" {
			header = append(header, go3270.Field{Content: "(not on your task list)", Color: go3270.Blue})
		} else {
			header = append(header, go3270.Field{Content: "(your tasks tagged [" + tag + "])", Color: go3270.Blue})
		}
	}
	if status.Content != "" {
		header = append(header, status)
	}
	screen = append(screen, placeLine(taskHeaderRow, cols, header)...)
	// Aligned with a task row: the mark in column 1, and the tags and
	// title from column 3, underlined to the end of the row. A field's highlighting runs on to
	// the next attribute byte, which is the first task row's, at its start;
	// with no tasks there is none, and nothing to head, so no headings.
	if len(pageTasks) > 0 {
		const headings = "S Task"
		screen = append(screen, go3270.Field{
			Row: taskColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
		})
	}

	tp.rowCards = map[int]string{}
	cursorRow, cursorCol = rows-1, 0
	for i, t := range pageTasks {
		row := taskFirstRow + i
		tp.rowCards[row] = t.CardID
		mark := ""
		if tp.marked[t.CardID] {
			mark = "X"
		}
		screen = append(screen, go3270.Field{
			Row: row, Col: 0, Write: true, Name: taskMarkName(t), Content: mark,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		})
		if cursorRow == rows-1 || t.CardID == tp.follow {
			cursorRow, cursorCol = row, 1
		}
		// With tags, they come first, then the title in a field of its
		// own; with none, the title alone. Both are autoskip, so that the
		// cursor goes on from a mark, once typed, to the next task's.
		prefix := taskPrefix(t)
		titleCol := taskTitleCol
		if prefix != "" {
			titleCol += 1 + len(prefix)
		}
		width := cols - 1 - (titleCol + 1)
		if width < 1 {
			// No room for the title: the prefix alone, cut to fit.
			screen = append(screen, placeLineAt(row, taskTitleCol, cols, line{{Content: prefix, Color: go3270.Turquoise, Autoskip: true}})...)
			continue
		}
		if prefix != "" {
			// A recurring task past due has them, with when it was due, in
			// red, as the dashboard has when.
			tags := go3270.Field{Row: row, Col: taskTitleCol, Content: prefix, Color: go3270.Turquoise, Autoskip: true}
			if r, ok := recurringOf(t); ok && pastDue(r, now) {
				tags.Color, tags.Intense = go3270.Red, true
			}
			screen = append(screen, tags)
		}
		// A recurring task is colored by when it is due, as on the
		// dashboard.
		color, intense := go3270.Green, true
		if r, ok := recurringOf(t); ok {
			color, intense = recurringColor(r, now)
		}
		screen = append(screen, go3270.Field{
			Row: row, Col: titleCol, Content: truncate(t.Title, width),
			Color: color, Intense: intense, Autoskip: true,
		})
	}
	tp.follow = ""

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
		Content: truncate(taskListHelp(tp.view == nil), cols-1),
	})
	return screen, shownPage, totalPages, cursorRow, cursorCol
}

// taskListHelp is the task screen's help row: with PF2, for adding a
// recurring task, on the user's own tasks.
func taskListHelp(own bool) string {
	if own {
		return "PF1=Details PF2=Recur PF3=Back PF4=Add PF7=Up PF8=Dn PF9=Lists PF10/11=Reorder"
	}
	return "PF1=Details PF3=Back PF4=Add PF7=Up PF8=Dn PF9=Lists PF10/11=Reorder"
}

// countFunc is how many of ts f is true of.
func countFunc(ts []tasks.Task, f func(tasks.Task) bool) int {
	n := 0
	for _, t := range ts {
		if f(t) {
			n++
		}
	}
	return n
}

// buildTaskConfirm renders the confirmation of archiving the tasks marked.
func buildTaskConfirm(rows, cols int, now time.Time, tp *taskPageState) go3270.Screen {
	ts := tp.confirming
	screen := titleFields(cols, "COMPLETE TASKS", now)
	screen = append(screen, placeLine(taskHeaderRow, cols, line{{
		Content: "Complete " + theseTasks(len(ts)) + "?", Color: go3270.Yellow, Intense: true,
	}})...)
	lines := make([]line, len(ts))
	for i, t := range ts {
		lines[i] = taskLine(t)
	}
	screen = appendConfirmList(screen, rows, cols, lines)
	note := "Their Trello cards will be marked complete and taken off their lists."
	switch n := countFunc(ts, isRecurring); {
	case n == len(ts):
		note = "They will be marked complete now, and come due again as scheduled."
	case n > 0:
		note = "Trello cards will be taken off their lists; recurring tasks come due again."
	}
	screen = append(screen, placeLine(rows-3, cols, line{{Content: note, Color: go3270.Turquoise}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to complete them, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Complete", cols-1)})
}

// theseTasks is "this task", or "these n tasks".
func theseTasks(n int) string {
	if n == 1 {
		return "this task"
	}
	return "these " + countText(n, "task", 0, 1)
}

// appendConfirmList adds lines to a confirmation, from below its heading to
// above the note row, with how many more there are if they do not fit.
func appendConfirmList(screen go3270.Screen, rows, cols int, lines []line) go3270.Screen {
	limit := max(rows-3-(taskHeaderRow+2), 1)
	shown := lines
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := taskHeaderRow + 2
	for _, l := range shown {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, l)...)
		row++
	}
	if len(shown) < len(lines) {
		screen = append(screen, placeLineAt(row, taskTitleCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(lines)-len(shown)), Color: go3270.Blue}})...)
	}
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
		v, ok := values[taskMarkName(t)]
		if !ok {
			continue
		}
		switch strings.ToUpper(v) {
		case "":
			delete(tp.marked, t.CardID)
		case "X":
			tp.marked[t.CardID] = true
		default:
			return fmt.Sprintf("%s: type X to mark it, not %q.", taskName(t), v)
		}
	}
	return ""
}

// markedTasks are the open tasks marked, in task order. Marks on tasks no
// longer open are dropped.
func (tp *taskPageState) markedTasks(all []tasks.Task) []tasks.Task {
	var out []tasks.Task
	open := map[string]bool{}
	for _, t := range all {
		open[t.CardID] = true
		if tp.marked[t.CardID] {
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

// taskListAction is where a key on the task list goes next.
type taskListAction int

const (
	taskListStay      taskListAction = iota // the task list again
	taskListConfirm                         // the confirmation for archiving
	taskListAdd                             // the add-task screen
	taskListMove                            // the move-task screen, for tp.moving
	taskListBrowse                          // the Trello lists, to pick one to show
	taskListDetails                         // the details of tp.detailsOf, or for a recurring task, its screen
	taskListRecurring                       // the recurring task screen, to add one
	taskListLeave                           // back: the dashboard, or from a list, the lists
)

// handleList acts on a key pressed on the task list, returning where to go
// next. The marks typed are kept whatever the key, except PF3.
func (tp *taskPageState) handleList(resp go3270.Response, all []tasks.Task, totalPages int, src TaskSource, logf func(string, ...any)) taskListAction {
	tp.message, tp.isError = "", false
	if resp.AID == go3270.AIDPF3 {
		return taskListLeave
	}
	if bad := tp.recordMarks(resp.Values, all); bad != "" {
		tp.message, tp.isError = bad, true
		return taskListStay
	}

	switch resp.AID {
	case go3270.AIDPF4:
		return taskListAdd
	case go3270.AIDPF5, go3270.AIDPF6:
		marked := tp.markedTasks(all)
		switch {
		case len(marked) == 0 && resp.AID == go3270.AIDPF5:
			tp.message, tp.isError = "Nothing is marked. Type X beside tasks to move, then press PF5.", true
		case len(marked) == 0:
			tp.message, tp.isError = "Nothing is marked. Type X beside tasks to complete, then press PF6.", true
		case src == nil:
			tp.message, tp.isError = "Changing tasks is not available.", true
		case resp.AID == go3270.AIDPF5 && slices.ContainsFunc(marked, isRecurring):
			tp.message, tp.isError = "Recurring tasks cannot be moved to Trello; unmark them first.", true
		case resp.AID == go3270.AIDPF5:
			tp.moving = marked
			return taskListMove
		default:
			tp.confirming = marked
			return taskListConfirm
		}
	case go3270.AIDPF7:
		tp.page = max(tp.page-1, 0)
	case go3270.AIDPF8:
		tp.page = min(tp.page+1, totalPages-1)
	case go3270.AIDPF1:
		if src == nil {
			tp.message, tp.isError = "Task details are not available.", true
			break
		}
		t, bad := tp.target(resp, all, "to see its details", "PF1")
		if bad != "" {
			tp.message, tp.isError = bad, true
			break
		}
		tp.detailsOf = t
		return taskListDetails
	case go3270.AIDPF9:
		if src == nil {
			tp.message, tp.isError = "Viewing other lists is not available.", true
			break
		}
		return taskListBrowse
	case go3270.AIDPF10, go3270.AIDPF11:
		tp.reorder(resp, all, src, logf)
	case go3270.AIDPF2:
		if tp.view != nil {
			tp.message, tp.isError = "Recurring tasks are added from your own tasks: PF3 goes back to them.", true
			break
		}
		return taskListRecurring
	}
	return taskListStay
}

// reorder moves the task marked, or with none marked, the one the cursor
// is on, up one place in its list for PF10, or down one for PF11. Recurring
// tasks stay in the order they come due.
func (tp *taskPageState) reorder(resp go3270.Response, all []tasks.Task, src TaskSource, logf func(string, ...any)) {
	fail := func(msg string) { tp.message, tp.isError = msg, true }
	if src == nil {
		fail("Changing tasks is not available.")
		return
	}
	t, bad := tp.target(resp, all, "to move up or down", "PF10 or PF11")
	if bad != "" {
		fail(bad)
		return
	}

	if isRecurring(t) {
		fail("Recurring tasks are listed in the order they come due.")
		return
	}

	// Its list's tasks, in order.
	var list []tasks.Task
	for _, x := range all {
		if x.Dest.ListID == t.Dest.ListID {
			list = append(list, x)
		}
	}
	i := slices.IndexFunc(list, func(x tasks.Task) bool { return x.CardID == t.CardID })
	up := resp.AID == go3270.AIDPF10
	var pos, where string
	switch {
	case up && i == 0:
		fail(fmt.Sprintf("%s is already at the top of its list.", taskName(t)))
		return
	case !up && i == len(list)-1:
		fail(fmt.Sprintf("%s is already at the bottom of its list.", taskName(t)))
		return
	case up && i == 1:
		pos, where = "top", "up"
	case up:
		pos, where = midpoint(list[i-2].Pos, list[i-1].Pos), "up"
	case i == len(list)-2:
		pos, where = "bottom", "down"
	default:
		pos, where = midpoint(list[i+1].Pos, list[i+2].Pos), "down"
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
	defer cancel()
	if err := src.Reposition(ctx, t, pos); err != nil {
		logf("moving task %q %s: %v", t.Title, where, err)
		fail(fmt.Sprintf("%s was not moved: %v", taskName(t), err))
		return
	}
	logf("moved task %q %s", t.Title, where)
	tp.follow = t.CardID
	tp.message = fmt.Sprintf("Moved %q %s.", t.Title, where)
}

// target is the task a key acts on: the one marked, or with none marked,
// the one the cursor is on; or why there is none, for doing what with
// which key.
func (tp *taskPageState) target(resp go3270.Response, all []tasks.Task, doing, key string) (t tasks.Task, bad string) {
	switch marked := tp.markedTasks(all); {
	case len(marked) == 1:
		return marked[0], ""
	case len(marked) > 1:
		return t, "Mark only one task " + doing + ", or none and put the cursor on it."
	}
	i := slices.IndexFunc(all, func(x tasks.Task) bool { return x.CardID == tp.rowCards[resp.Row] })
	if i < 0 {
		return t, "Mark a task with X, or put the cursor on one, then press " + key + "."
	}
	return all[i], ""
}

// midpoint is the Trello position halfway between a and b.
func midpoint(a, b float64) string {
	return strconv.FormatFloat((a+b)/2, 'f', -1, 64)
}

// handleConfirm acts on a key pressed on the confirmation of archiving,
// returning whether to go back to the task list. Only PF4 does it; PF3 goes
// back with the marks kept; any other key leaves the confirmation up.
// Archiving stops at the first task that fails, which stays marked.
func (tp *taskPageState) handleConfirm(resp go3270.Response, src TaskSource, logf func(string, ...any)) (back bool) {
	switch resp.AID {
	case go3270.AIDPF3:
		tp.confirming = nil
		return true
	case go3270.AIDPF4:
	default:
		return false
	}

	n := len(tp.confirming)
	done := 0
	tp.message, tp.isError = "", false
	for _, t := range tp.confirming {
		ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
		err := src.Archive(ctx, t)
		cancel()
		if err != nil {
			logf("completing task %q: %v", t.Title, err)
			tp.message = fmt.Sprintf("Completed %d of %d. %s was not completed: %v", done, n, taskName(t), err)
			tp.isError = true
			break
		}
		logf("completed task %q", t.Title)
		delete(tp.marked, t.CardID)
		done++
	}
	if !tp.isError {
		tp.message = fmt.Sprintf("Completed %s.", countText(done, "task", 0, 1))
	}
	tp.confirming = nil
	return true
}
