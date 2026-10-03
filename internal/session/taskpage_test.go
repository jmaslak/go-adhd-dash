package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// someTasks are n open tasks on cards 101 up.
func someTasks(n int) []tasks.Task {
	var out []tasks.Task
	for i := range n {
		out = append(out, tasks.Task{Number: i + 1, Title: fmt.Sprint("task ", i+1), CardID: fmt.Sprint(101 + i)})
	}
	return out
}

// fakeTasks is a task backend: fakeMover's boards and moves, tasks to
// show, and any list's cards. It records what it archives, renames,
// repositions and adds, failing to archive or rename the task numbered
// failOn.
type fakeTasks struct {
	fakeMover
	snap  tasks.Snapshot
	cards map[string][]tasks.Task // by list ID
	reads int                     // of a list's cards

	failOn   int
	archived []int
	renamed  []string // number>title
	repos    []string // number:pos
	added    []string // list ID:title
}

func (f *fakeTasks) Snapshot() tasks.Snapshot { return f.snap }

func (f *fakeTasks) Archive(_ context.Context, t tasks.Task) error {
	if t.Number == f.failOn {
		return errors.New("Trello said no")
	}
	f.archived = append(f.archived, t.Number)
	return nil
}

func (f *fakeTasks) Rename(_ context.Context, t tasks.Task, title string) error {
	if t.Number == f.failOn {
		return errors.New("Trello said no")
	}
	f.renamed = append(f.renamed, fmt.Sprintf("%d>%s", t.Number, title))
	return nil
}

func (f *fakeTasks) Reposition(_ context.Context, t tasks.Task, pos string) error {
	if t.Number == f.failOn {
		return errors.New("Trello said no")
	}
	f.repos = append(f.repos, fmt.Sprintf("%d:%s", t.Number, pos))
	return nil
}

func (f *fakeTasks) Add(_ context.Context, title string, d tasks.Destination) (int, error) {
	f.added = append(f.added, d.ListID+":"+title)
	return 99, nil
}

func (f *fakeTasks) Cards(_ context.Context, d tasks.Destination) ([]tasks.Task, error) {
	f.reads++
	if f.boardsErr != nil {
		return nil, f.boardsErr
	}
	return f.cards[d.ListID], nil
}

func noLog(string, ...any) {}

func TestTaskListScreen(t *testing.T) {
	all := someTasks(25)
	tp := &taskPageState{marked: map[string]bool{"101": true}}

	s, shown, total, crow, ccol := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
	if shown != 0 || total != 2 {
		t.Errorf("page %d of %d, want 0 of 2 (18 tasks a page)", shown, total)
	}
	rows := screenText(t, s, 24, 80)
	if !strings.HasPrefix(rows[taskHeaderRow], " TASKS 25 open, page 1/2, 1 marked") {
		t.Errorf("header is %q", rows[taskHeaderRow])
	}
	if rows[taskColumnRow] != " S  Num Task" {
		t.Errorf("column headings are %q", rows[taskColumnRow])
	}
	// The underline covers the whole headings row and stops there: a
	// field's highlighting runs to the next attribute byte, which must be the
	// first task row's, at its start.
	var headings go3270.Field
	next := 24 * 80
	for _, f := range s {
		switch at := f.Row*80 + f.Col; {
		case at == taskColumnRow*80:
			headings = f
		case at > taskColumnRow*80 && at < next:
			next = at
		}
	}
	if headings.Highlighting != go3270.Underscore || len(headings.Content) != 79 {
		t.Errorf("headings field is %+v; want underlined to the end of the row", headings)
	}
	if next != taskFirstRow*80 {
		t.Errorf("next attribute after the headings is at row %d col %d; want the first task row's start", next/80, next%80)
	}
	if rows[taskFirstRow] != " X    1 task 1" || rows[taskFirstRow+1] != "      2 task 2" {
		t.Errorf("first rows are %q, %q", rows[taskFirstRow], rows[taskFirstRow+1])
	}
	if !strings.Contains(rows[22], "Mark tasks with X") || !strings.Contains(rows[23], "PF3=Back") {
		t.Errorf("message and help rows are %q, %q", rows[22], rows[23])
	}
	if crow != taskFirstRow || ccol != 1 {
		t.Errorf("cursor at %d,%d, want on the first mark field", crow, ccol)
	}

	fields := map[string]go3270.Field{}
	for _, f := range s {
		if f.Write {
			fields[f.Name] = f
		}
	}
	if len(fields) != 36 {
		t.Errorf("%d input fields, want 18 marks and 18 titles", len(fields))
	}
	// Each title is typed over in place, to the edge of the screen.
	if f := fields["title:101"]; f.Content != "task 1" || f.Row != taskFirstRow || f.Col != taskTitleCol+5 || f.Highlighting != go3270.Underscore {
		t.Errorf("task 1's title field is %+v", f)
	}
	stops := 0
	for _, f := range s {
		if f.Col == 79 && f.Row >= taskFirstRow && f.Row < taskFirstRow+18 && !f.Write {
			stops++
		}
	}
	if stops != 18 {
		t.Errorf("%d title fields ended at the edge, want 18", stops)
	}
	if f := fields["mark:101"]; f.Content != "X" || f.Row != taskFirstRow || f.Col != 0 {
		t.Errorf("task 1's mark field is %+v", f)
	}
	for _, f := range s {
		if f.Row >= taskFirstRow && f.Row < taskFirstRow+18 && f.Col == taskTitleCol && !f.Autoskip {
			t.Errorf("field after the mark on row %d does not skip on: %+v", f.Row, f)
		}
	}

	tp.page = 1
	s, _, _, _, _ = buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
	if rows := screenText(t, s, 24, 80); !strings.HasPrefix(rows[taskFirstRow], "     19 task 19") {
		t.Errorf("second page starts %q", rows[taskFirstRow])
	}

	s, _, _, _, _ = buildTaskList(24, 80, now, tasks.Snapshot{Fetched: now}, &taskPageState{})
	if rows := screenText(t, s, 24, 80); rows[taskColumnRow] != "" || !strings.HasPrefix(rows[taskHeaderRow], " TASKS 0 open") {
		t.Errorf("with no tasks, header row %q and column headings row %q; want no column headings", rows[taskHeaderRow], rows[taskColumnRow])
	}
}

func TestTaskListStatus(t *testing.T) {
	for _, c := range []struct {
		snap tasks.Snapshot
		want string
	}{
		{tasks.Snapshot{Loading: true}, " TASKS fetching from Trello"},
		{tasks.Snapshot{Err: errors.New("trello: 401 Unauthorized")}, " TASKS trello: 401 Unauthorized"},
		{tasks.Snapshot{Tasks: someTasks(2), Fetched: now.Add(-20 * time.Minute), Err: errors.New("timeout")}, " TASKS 2 open stale, from 09:40: timeout"},
	} {
		s, _, _, _, _ := buildTaskList(24, 80, now, c.snap, &taskPageState{})
		if row := screenText(t, s, 24, 80)[taskHeaderRow]; row != c.want {
			t.Errorf("header is %q, want %q", row, c.want)
		}
	}
}

func TestTaskListMarking(t *testing.T) {
	all := someTasks(25)
	arch := &fakeTasks{}
	tp := &taskPageState{}
	key := func(aid go3270.AID, values map[string]string) (confirm, leave bool) {
		a := tp.handleList(go3270.Response{AID: aid, Values: values}, all, 2, arch, noLog)
		return a == taskListConfirm, a == taskListLeave
	}

	if confirm, _ := key(go3270.AIDPF6, map[string]string{"mark:101": ""}); confirm || !tp.isError || !strings.Contains(tp.message, "Nothing is marked") {
		t.Errorf("PF6 with nothing marked: confirm %v, message %q", confirm, tp.message)
	}
	if confirm, _ := key(go3270.AIDEnter, map[string]string{"mark:101": "q"}); confirm || !strings.Contains(tp.message, `not "q"`) {
		t.Errorf("bad mark: confirm %v, message %q", confirm, tp.message)
	}

	// Marks on the first page survive paging to the second and marking there.
	key(go3270.AIDPF8, map[string]string{"mark:101": "x", "mark:102": "X"})
	if tp.page != 1 || len(tp.marked) != 2 {
		t.Fatalf("after PF8: page %d, marked %v", tp.page, tp.marked)
	}
	// Enter keeps the marks but does not archive.
	if confirm, _ := key(go3270.AIDEnter, map[string]string{"mark:120": "X"}); confirm || tp.isError || len(tp.marked) != 3 {
		t.Fatalf("Enter: confirm %v, message %q, marked %v; want the mark kept and nothing else", confirm, tp.message, tp.marked)
	}
	confirm, _ := key(go3270.AIDPF6, nil)
	if !confirm {
		t.Fatalf("PF6 with marks did not confirm: %q", tp.message)
	}
	var got []int
	for _, c := range tp.confirming {
		got = append(got, c.Number)
	}
	if fmt.Sprint(got) != "[1 2 20]" {
		t.Errorf("confirming %v, want tasks 1, 2 and 20", got)
	}

	if _, leave := key(go3270.AIDPF3, nil); !leave {
		t.Errorf("PF3 did not leave the task screen")
	}

	// PF4 goes to add a task, keeping the marks typed with it.
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF4, Values: map[string]string{"mark:103": "X"}}, all, 2, arch, noLog); a != taskListAdd || !tp.marked["103"] {
		t.Errorf("PF4: action %v, marked %v", a, tp.marked)
	}
}

func TestArchiveConfirm(t *testing.T) {
	all := someTasks(3)
	s := buildArchiveConfirm(24, 80, now, all)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Archive these 3 tasks?", "   2 task 2", "Their Trello cards will be marked done and archived.", "Press PF4 to archive", "PF4=Archive"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}

	arch := &fakeTasks{}
	tp := &taskPageState{marked: map[string]bool{"101": true, "102": true, "103": true}, confirming: all}
	if back := tp.handleConfirm(go3270.Response{AID: go3270.AIDEnter}, arch, noLog); back || len(arch.archived) != 0 {
		t.Errorf("Enter archived or left the confirmation: back %v, archived %v", back, arch.archived)
	}

	arch.failOn = 2
	if back := tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, arch, noLog); !back {
		t.Errorf("PF4 did not go back to the list")
	}
	if fmt.Sprint(arch.archived) != "[1]" || !tp.isError || !strings.Contains(tp.message, "Archived 1 of 3. Task 2") {
		t.Errorf("failure midway: archived %v, message %q", arch.archived, tp.message)
	}
	if tp.marked["101"] || !tp.marked["102"] || !tp.marked["103"] {
		t.Errorf("marks after failure: %v; want the unarchived ones kept", tp.marked)
	}

	arch.failOn, arch.archived = 0, nil
	tp.confirming = all[1:]
	tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, arch, noLog)
	if fmt.Sprint(arch.archived) != "[2 3]" || tp.isError || tp.message != "Archived 2 tasks." || len(tp.marked) != 0 {
		t.Errorf("retry: archived %v, message %q, marked %v", arch.archived, tp.message, tp.marked)
	}

	tp.confirming = all
	if back := tp.handleConfirm(go3270.Response{AID: go3270.AIDPF3}, arch, noLog); !back || tp.confirming != nil {
		t.Errorf("PF3 did not cancel")
	}
}

func TestTaskRename(t *testing.T) {
	all := someTasks(3)
	all[2].Title = strings.Repeat("long ", 30) // cut to fit its field
	src := &fakeTasks{}
	tp := &taskPageState{}
	build := func() string {
		s, _, _, _, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
		return strings.Join(screenText(t, s, 24, 80), "\n")
	}
	key := func(aid go3270.AID, values map[string]string) taskListAction {
		build()
		return tp.handleList(go3270.Response{AID: aid, Values: values}, all, 1, src, noLog)
	}

	// A title sent back as drawn, cut short or not, is no rename.
	build()
	unchanged := map[string]string{"title:101": "task 1", "title:103": tp.shown["title:103"] + "  "}
	if a := key(go3270.AIDEnter, unchanged); a != taskListStay || tp.renaming != nil {
		t.Errorf("titles unchanged: %v, renaming %v", a, tp.renaming)
	}

	// Blanked: refused, still typed.
	if a := key(go3270.AIDEnter, map[string]string{"title:102": ""}); a != taskListStay || !tp.isError || !strings.Contains(tp.message, "cannot be blank") {
		t.Errorf("blanked: %v, %q", a, tp.message)
	}

	// Typed over, whatever the key, asks to confirm, keeping marks typed.
	if a := key(go3270.AIDPF8, map[string]string{"title:102": " second task ", "mark:101": "X"}); a != taskListConfirm || len(tp.renaming) != 1 || tp.renaming[0].title != "second task" || !tp.marked["101"] {
		t.Fatalf("typed over: %v, renaming %+v, marks %v", a, tp.renaming, tp.marked)
	}
	text := strings.Join(screenText(t, buildTaskConfirm(24, 80, now, tp), 24, 80), "\n")
	for _, want := range []string{"RENAME TASKS", "Rename this task?", "2 task 2 to second task", "PF12=Discard"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	// PF3 goes back with it still typed.
	if !tp.handleConfirm(go3270.Response{AID: go3270.AIDPF3}, src, noLog) || tp.renaming != nil {
		t.Errorf("PF3 on the confirmation")
	}
	if text := build(); !strings.Contains(text, "2  second task") || len(src.renamed) != 0 {
		t.Errorf("after PF3, not still typed, or renamed %v:\n%s", src.renamed, text)
	}
	// PF12 discards it.
	key(go3270.AIDEnter, map[string]string{"title:102": "second task"})
	if !tp.handleConfirm(go3270.Response{AID: go3270.AIDPF12}, src, noLog) || tp.typed != nil || len(src.renamed) != 0 {
		t.Errorf("PF12: typed %v, renamed %v", tp.typed, src.renamed)
	}
	if text := build(); !strings.Contains(text, "2 task 2") {
		t.Errorf("after PF12, still typed:\n%s", text)
	}

	// PF4 renames, stopping at a failure, which stays typed.
	src.failOn = 2
	key(go3270.AIDEnter, map[string]string{"title:101": "one", "title:102": "two"})
	tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, src, noLog)
	if fmt.Sprint(src.renamed) != "[1>one]" || !tp.isError || !strings.Contains(tp.message, "Renamed 1 of 2. Task 2") || tp.typed["title:102"] != "two" || tp.typed["title:101"] != "" {
		t.Errorf("failing: renamed %v, %q, typed %v", src.renamed, tp.message, tp.typed)
	}
	src.failOn = 0
	all[0].Title = "one"
	key(go3270.AIDEnter, map[string]string{"title:102": "two"})
	tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, src, noLog)
	if fmt.Sprint(src.renamed) != "[1>one 2>two]" || tp.message != "Renamed 1 task." {
		t.Errorf("retry: renamed %v, %q", src.renamed, tp.message)
	}
}

func TestTaskReorder(t *testing.T) {
	today := tasks.Destination{ListID: "l1"}
	later := tasks.Destination{ListID: "l2"}
	// Two lists, mixed, as the user's tasks are not: the order within each
	// is what matters.
	all := []tasks.Task{
		{Number: 1, Title: "a", CardID: "a", Dest: today, Pos: 100},
		{Number: 2, Title: "b", CardID: "b", Dest: today, Pos: 200},
		{Number: 3, Title: "x", CardID: "x", Dest: later, Pos: 50},
		{Number: 4, Title: "c", CardID: "c", Dest: today, Pos: 300},
		{Number: 5, Title: "d", CardID: "d", Dest: today, Pos: 400},
	}
	src := &fakeTasks{}
	tp := &taskPageState{}
	key := func(aid go3270.AID, row int, values map[string]string) {
		buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
		tp.handleList(go3270.Response{AID: aid, Row: row, Values: values}, all, 1, src, noLog)
	}
	rowOf := func(n int) int { return taskFirstRow + n - 1 }

	for _, c := range []struct {
		aid  go3270.AID
		task int
		want string
	}{
		{go3270.AIDPF10, 4, "4:150"},    // c up: between a and b
		{go3270.AIDPF10, 2, "2:top"},    // b up: above a
		{go3270.AIDPF11, 2, "2:350"},    // b down: between c and d, past x on another list
		{go3270.AIDPF11, 4, "4:bottom"}, // c down: below d
	} {
		src.repos = nil
		key(c.aid, rowOf(c.task), nil)
		if fmt.Sprint(src.repos) != "["+c.want+"]" || tp.isError {
			t.Errorf("PF%x on task %d: %v, %q; want %s", c.aid, c.task, src.repos, tp.message, c.want)
		}
	}
	if tp.follow != "c" {
		t.Errorf("the cursor does not follow the task moved: %q", tp.follow)
	}
	_, _, _, crow, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
	if crow != rowOf(4) {
		t.Errorf("cursor on row %d, want task 4's", crow)
	}

	// At the ends of its list, or with none to act on, refused.
	src.repos = nil
	for _, c := range []struct {
		aid    go3270.AID
		row    int
		values map[string]string
		want   string
	}{
		{go3270.AIDPF10, rowOf(1), nil, "already at the top"},
		{go3270.AIDPF10, rowOf(3), nil, "already at the top"}, // x, alone on its list
		{go3270.AIDPF11, rowOf(5), nil, "already at the bottom"},
		{go3270.AIDPF11, 22, nil, "put the cursor on one"},
		{go3270.AIDPF11, rowOf(1), map[string]string{"mark:a": "X", "mark:b": "X"}, "Mark only one"},
	} {
		key(c.aid, c.row, c.values)
		if !tp.isError || !strings.Contains(tp.message, c.want) {
			t.Errorf("PF%x on row %d: %q; want %q", c.aid, c.row, tp.message, c.want)
		}
		tp.marked = nil
	}
	if len(src.repos) != 0 {
		t.Errorf("repositioned when refused: %v", src.repos)
	}

	// The one marked, wherever the cursor.
	key(go3270.AIDPF11, rowOf(5), map[string]string{"mark:a": "X"})
	if fmt.Sprint(src.repos) != "[1:250]" {
		t.Errorf("marked a down: %v", src.repos)
	}
	// A failure says so.
	src.failOn, src.repos, tp.marked = 2, nil, nil
	key(go3270.AIDPF10, rowOf(2), nil)
	if !tp.isError || !strings.Contains(tp.message, "Task 2 was not moved") {
		t.Errorf("failing: %q", tp.message)
	}
}

// TestTaskMessagesFit checks that the task screen's fixed messages fit on
// a Model 2's 80 columns, less the attribute byte before them.
func TestTaskMessagesFit(t *testing.T) {
	all := someTasks(3)
	src := &fakeTasks{}
	for _, c := range []struct {
		aid    go3270.AID
		values map[string]string
	}{
		{go3270.AIDPF5, nil},
		{go3270.AIDPF6, nil},
		{go3270.AIDEnter, map[string]string{"title:101": ""}},
		{go3270.AIDEnter, map[string]string{"mark:101": "q"}},
		{go3270.AIDPF10, nil},
		{go3270.AIDPF11, map[string]string{"mark:101": "X", "mark:102": "X"}},
	} {
		tp := &taskPageState{}
		buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
		tp.handleList(go3270.Response{AID: c.aid, Row: 22, Values: c.values}, all, 1, src, noLog)
		if tp.message == "" || len([]rune(tp.message)) > 79 {
			t.Errorf("PF%x %v: message of %d characters: %q", c.aid, c.values, len([]rune(tp.message)), tp.message)
		}
	}
	for _, m := range []string{taskMarkMessage, movePrompt, browsePrompt} {
		if len(m) > 79 {
			t.Errorf("%d characters: %q", len(m), m)
		}
	}
}
