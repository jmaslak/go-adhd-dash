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
// one-character input field before it for its mark, then its number and
// tags, then its title, typed over to rename it.
const (
	taskHeaderRow   = 2
	taskColumnRow   = 3
	taskFirstRow    = 4
	taskMarkField   = "mark:"
	taskTitleField  = "title:"
	taskTitleCol    = 2
	taskMarkMessage = "Mark tasks with X: PF5 moves them, PF6 archives. Type over a title to rename."
)

// taskPageState is one session's place on a task screen: which tasks it
// shows (view), the page shown, the tasks marked, titles typed over, and
// those awaiting confirmation.
type taskPageState struct {
	// view is the Trello list shown, picked with PF9; nil for the user's
	// tasks.
	view *listView

	page int

	// marked holds the card IDs of the tasks marked, to move or archive.
	// They are kept by card because numbers shift when tasks are archived.
	marked map[string]bool

	// shown are the titles as drawn, by their fields' names, and rowCards
	// the card on each row, from the last screen drawn.
	shown    map[string]string
	rowCards map[int]string

	// typed are titles typed over and not yet saved, by field name, drawn
	// again in place of the tasks' own.
	typed map[string]string

	// confirming are the tasks shown on the confirmation for archiving,
	// and renaming those on the confirmation for renaming, awaiting PF4.
	confirming []tasks.Task
	renaming   []taskRename

	// moving are the tasks marked when PF5 was pressed, for the move-task
	// screen.
	moving []tasks.Task

	// follow is the card the cursor goes to, once moved up or down.
	follow string

	// message reports the last key's outcome; isError colors it.
	message string
	isError bool
}

// source is what tp shows and changes: the Trello list it shows, or
// backend, the user's tasks; nil when backend is.
func (tp *taskPageState) source(backend TaskBackend) TaskSource {
	switch {
	case tp.view != nil:
		return tp.view
	case backend == nil:
		return nil
	}
	return backend
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

// taskRename is a task, and the title typed over its own.
type taskRename struct {
	task  tasks.Task
	title string
}

// taskMarkName is the name of the input field marking t.
func taskMarkName(t tasks.Task) string {
	return taskMarkField + t.CardID
}

// taskTitleName is the name of the input field holding t's title.
func taskTitleName(t tasks.Task) string {
	return taskTitleField + t.CardID
}

// taskRows is how many tasks fit on one page of the task screen.
func taskRows(rows int) int {
	return max(rows-2-taskFirstRow, 1)
}

// taskPrefix is t's number, right aligned, and its tags.
func taskPrefix(t tasks.Task) string {
	prefix := fmt.Sprintf("%4d", t.Number)
	if len(t.Tags) > 0 {
		prefix += " [" + strings.Join(t.Tags, "] [") + "]"
	}
	return prefix
}

// buildTaskList renders one page of the tasks in snap, each with its mark
// field and its title to type over, and where the cursor goes: the first
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
	status, ok := tasksStatus(snap.Err, snap.Fetched, snap.Loading)
	if ok {
		info := fmt.Sprintf("%d open", len(all))
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
	// Aligned with a task row: the mark in column 1, the number right
	// aligned in columns 3 to 6, and the tags and title from column 8,
	// underlined to the end of the row. A field's highlighting runs on to
	// the next attribute byte, which is the first task row's, at its start;
	// with no tasks there is none, and nothing to head, so no headings.
	if len(pageTasks) > 0 {
		const headings = "S  Num Task"
		screen = append(screen, go3270.Field{
			Row: taskColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
		})
	}

	tp.shown, tp.rowCards = map[string]string{}, map[int]string{}
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
		prefix := taskPrefix(t)
		titleCol := taskTitleCol + 1 + len(prefix)
		width := cols - 1 - (titleCol + 1)
		if width < 1 {
			// No room for the title: the prefix alone, cut to fit.
			screen = append(screen, placeLineAt(row, taskTitleCol, cols, line{{Content: prefix, Color: go3270.Turquoise, Autoskip: true}})...)
			continue
		}
		name := taskTitleName(t)
		tp.shown[name] = cutRunes(t.Title, width)
		title := tp.shown[name]
		if v, ok := tp.typed[name]; ok {
			title = cutRunes(v, width)
		}
		screen = append(screen,
			// Autoskip sends the cursor on from the mark, once typed, to
			// the title, rather than into the number.
			go3270.Field{Row: row, Col: taskTitleCol, Content: prefix, Color: go3270.Turquoise, Autoskip: true},
			go3270.Field{
				Row: row, Col: titleCol, Write: true, Name: name, Content: title,
				Color: go3270.Green, Intense: true, Highlighting: go3270.Underscore,
			},
			// Ends the title field at the edge of the screen.
			go3270.Field{Row: row, Col: cols - 1},
		)
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
		Content: truncate("PF3=Back PF4=Add PF5=Move PF6=Archive PF7=Up PF8=Dn PF9=Lists PF10/11=Reorder", cols-1),
	})
	return screen, shownPage, totalPages, cursorRow, cursorCol
}

// buildTaskConfirm renders the confirmation of archiving the tasks marked,
// or renaming those typed over.
func buildTaskConfirm(rows, cols int, now time.Time, tp *taskPageState) go3270.Screen {
	if len(tp.renaming) > 0 {
		return buildRenameConfirm(rows, cols, now, tp.renaming)
	}
	return buildArchiveConfirm(rows, cols, now, tp.confirming)
}

// buildArchiveConfirm renders the confirmation for archiving ts.
func buildArchiveConfirm(rows, cols int, now time.Time, ts []tasks.Task) go3270.Screen {
	screen := titleFields(cols, "ARCHIVE TASKS", now)
	screen = append(screen, placeLine(taskHeaderRow, cols, line{{
		Content: "Archive " + theseTasks(len(ts)) + "?", Color: go3270.Yellow, Intense: true,
	}})...)
	lines := make([]line, len(ts))
	for i, t := range ts {
		lines[i] = taskLine(t)
	}
	screen = appendConfirmList(screen, rows, cols, lines)
	screen = append(screen, placeLine(rows-3, cols, line{{
		Content: "Their Trello cards will be marked done and archived.", Color: go3270.Turquoise,
	}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to archive, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Archive", cols-1)})
}

// buildRenameConfirm renders the confirmation for renaming tasks.
func buildRenameConfirm(rows, cols int, now time.Time, rs []taskRename) go3270.Screen {
	screen := titleFields(cols, "RENAME TASKS", now)
	screen = append(screen, placeLine(taskHeaderRow, cols, line{{
		Content: "Rename " + theseTasks(len(rs)) + "?", Color: go3270.Yellow, Intense: true,
	}})...)
	lines := make([]line, len(rs))
	for i, r := range rs {
		lines[i] = line{
			{Content: fmt.Sprintf("%4d", r.task.Number), Color: go3270.Turquoise},
			{Content: r.task.Title, Color: go3270.Green},
			{Content: "to", Color: go3270.White},
			{Content: r.title, Color: go3270.Green, Intense: true},
		}
	}
	screen = appendConfirmList(screen, rows, cols, lines)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to rename, PF3 to change what was typed, or PF12 to discard it.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Rename PF12=Discard", cols-1)})
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
			return fmt.Sprintf("Task %d: type X to mark it, not %q.", t.Number, v)
		}
	}
	return ""
}

// typedRenames are the titles typed over on the page just shown, in task
// order, reporting a title blanked.
func (tp *taskPageState) typedRenames(values map[string]string, all []tasks.Task) (renames []taskRename, typed map[string]string, bad string) {
	typed = map[string]string{}
	for _, t := range all {
		name := taskTitleName(t)
		shown, drawn := tp.shown[name]
		v, ok := values[name]
		if !drawn || !ok || strings.TrimSpace(v) == strings.TrimSpace(shown) {
			continue
		}
		typed[name] = v
		if strings.TrimSpace(v) == "" {
			bad = fmt.Sprintf("Task %d: a title cannot be blank (to finish a task, archive it).", t.Number)
			continue
		}
		renames = append(renames, taskRename{task: t, title: strings.TrimSpace(v)})
	}
	return renames, typed, bad
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
	taskListStay    taskListAction = iota // the task list again
	taskListConfirm                       // the confirmation for archiving or renaming
	taskListAdd                           // the add-task screen
	taskListMove                          // the move-task screen, for tp.moving
	taskListBrowse                        // the Trello lists, to pick one to show
	taskListLeave                         // back: the dashboard, or from a list, the lists
)

// handleList acts on a key pressed on the task list, returning where to go
// next. The marks typed are kept whatever the key, except PF3. Titles
// typed over, whatever the key, ask first to confirm renaming them.
func (tp *taskPageState) handleList(resp go3270.Response, all []tasks.Task, totalPages int, src TaskSource, logf func(string, ...any)) taskListAction {
	tp.message, tp.isError = "", false
	renames, typed, bad := tp.typedRenames(resp.Values, all)
	if bad != "" {
		tp.typed, tp.message, tp.isError = typed, bad, true
		return taskListStay
	}
	if len(renames) > 0 {
		if src == nil {
			tp.typed, tp.message, tp.isError = typed, "Renaming is not available.", true
			return taskListStay
		}
		tp.typed, tp.renaming = typed, renames
		if msg := tp.recordMarks(resp.Values, all); msg != "" {
			tp.renaming, tp.message, tp.isError = nil, msg, true
			return taskListStay
		}
		return taskListConfirm
	}
	tp.typed = nil
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
			tp.message, tp.isError = "Nothing is marked. Type X beside tasks to archive, then press PF6.", true
		case src == nil:
			tp.message, tp.isError = "Changing tasks is not available.", true
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
	case go3270.AIDPF9:
		if src == nil {
			tp.message, tp.isError = "Viewing other lists is not available.", true
			break
		}
		return taskListBrowse
	case go3270.AIDPF10, go3270.AIDPF11:
		tp.reorder(resp, all, src, logf)
	}
	return taskListStay
}

// reorder moves the task marked, or with none marked, the one the cursor
// is on, up one place in its list for PF10, or down one for PF11.
func (tp *taskPageState) reorder(resp go3270.Response, all []tasks.Task, src TaskSource, logf func(string, ...any)) {
	fail := func(msg string) { tp.message, tp.isError = msg, true }
	if src == nil {
		fail("Changing tasks is not available.")
		return
	}
	var t tasks.Task
	switch marked := tp.markedTasks(all); {
	case len(marked) == 1:
		t = marked[0]
	case len(marked) > 1:
		fail("Mark only one task to move up or down, or none and put the cursor on it.")
		return
	default:
		i := slices.IndexFunc(all, func(x tasks.Task) bool { return x.CardID == tp.rowCards[resp.Row] })
		if i < 0 {
			fail("Mark a task with X, or put the cursor on one, then press PF10 or PF11.")
			return
		}
		t = all[i]
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
		fail(fmt.Sprintf("Task %d is already at the top of its list.", t.Number))
		return
	case !up && i == len(list)-1:
		fail(fmt.Sprintf("Task %d is already at the bottom of its list.", t.Number))
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
		logf("moving task %d %q %s: %v", t.Number, t.Title, where, err)
		fail(fmt.Sprintf("Task %d was not moved: %v", t.Number, err))
		return
	}
	logf("moved task %d %q %s", t.Number, t.Title, where)
	tp.follow = t.CardID
	tp.message = fmt.Sprintf("Moved %q %s.", t.Title, where)
}

// midpoint is the Trello position halfway between a and b.
func midpoint(a, b float64) string {
	return strconv.FormatFloat((a+b)/2, 'f', -1, 64)
}

// handleConfirm acts on a key pressed on the confirmation of archiving or
// renaming, returning whether to go back to the task list. Only PF4 does
// it; PF3 goes back with the marks, or the titles typed, kept; for
// renaming, PF12 goes back discarding what was typed; any other key leaves
// the confirmation up. Archiving or renaming stops at the first task that
// fails, which stays marked, or typed.
func (tp *taskPageState) handleConfirm(resp go3270.Response, src TaskSource, logf func(string, ...any)) (back bool) {
	renaming := len(tp.renaming) > 0
	switch {
	case resp.AID == go3270.AIDPF3:
		tp.confirming, tp.renaming = nil, nil
		return true
	case resp.AID == go3270.AIDPF12 && renaming:
		tp.renaming, tp.typed = nil, nil
		tp.message, tp.isError = "Discarded the titles typed.", false
		return true
	case resp.AID != go3270.AIDPF4:
		return false
	}

	verb, past := "archive", "Archived"
	n := len(tp.confirming)
	if renaming {
		verb, past, n = "rename", "Renamed", len(tp.renaming)
	}
	done := 0
	tp.message, tp.isError = "", false
	for i := range n {
		var t tasks.Task
		ctx, cancel := context.WithTimeout(context.Background(), taskChangeTimeout)
		var err error
		if renaming {
			t = tp.renaming[i].task
			err = src.Rename(ctx, t, tp.renaming[i].title)
		} else {
			t = tp.confirming[i]
			err = src.Archive(ctx, t)
		}
		cancel()
		if err != nil {
			logf("%sing task %d %q: %v", strings.TrimSuffix(verb, "e"), t.Number, t.Title, err)
			tp.message = fmt.Sprintf("%s %d of %d. Task %d, %q, was not %sd: %v", past, done, n, t.Number, t.Title, verb, err)
			tp.isError = true
			break
		}
		if renaming {
			logf("renamed task %d %q to %q", t.Number, t.Title, tp.renaming[i].title)
			delete(tp.typed, taskTitleName(t))
		} else {
			logf("archived task %d %q", t.Number, t.Title)
			delete(tp.marked, t.CardID)
		}
		done++
	}
	if !tp.isError {
		tp.message = fmt.Sprintf("%s %s.", past, countText(done, "task", 0, 1))
	}
	tp.confirming, tp.renaming = nil, nil
	return true
}
