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

// someday is fakeMover's list not on the task list.
var someday = tasks.Destination{BoardID: "b3", Board: "Projects", ListID: "l4", List: "Someday"}

// ideas are n cards on Someday.
func ideas(n int) []tasks.Task {
	var out []tasks.Task
	for i := range n {
		out = append(out, tasks.Task{Number: i + 1, Title: fmt.Sprint("idea ", i+1), CardID: fmt.Sprint("c", i), Dest: someday, Pos: float64(100 * (i + 1))})
	}
	return out
}

func TestTaskListBrowse(t *testing.T) {
	tp := &taskPageState{}
	all := someTasks(3)
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF9, Values: map[string]string{"mark:101": "X"}}, all, 1, &fakeTasks{}, noLog); a != taskListBrowse || !tp.marked["101"] {
		t.Errorf("PF9: %v, marks %v; want the lists, the mark kept", a, tp.marked)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF9}, all, 1, nil, noLog); a != taskListStay || !strings.Contains(tp.message, "not available") {
		t.Errorf("PF9 with no tasks: %v, %q", a, tp.message)
	}
	s, _, _, _, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, &taskPageState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "PF9=Lists") || !strings.Contains(text, "PF10/11=Reorder") {
		t.Errorf("task list does not offer other lists and reordering:\n%s", text)
	}
}

func TestBrowsePicker(t *testing.T) {
	b := startBrowse(&fakeTasks{})
	s, crow, _ := buildBrowse(24, 80, now, &b)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"TRELLO LISTS", "Show which list?", "Work / Today [work]", "Projects / Someday", "Enter=Show the list selected"} {
		if !strings.Contains(text, want) {
			t.Errorf("lists lack %q:\n%s", want, text)
		}
	}
	if crow != taskFirstRow {
		t.Errorf("cursor on row %d; want the first list", crow)
	}
	if back, _, ok := b.handle(go3270.Response{AID: go3270.AIDEnter}); back || ok || !b.isError {
		t.Errorf("Enter with nothing selected: back %v, ok %v, %q", back, ok, b.message)
	}
	if back, d, ok := b.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{"dest:l4": "S"}}); back || !ok || d != someday {
		t.Errorf("Enter with Someday selected: back %v, %v, %+v", back, ok, d)
	}
	if back, _, _ := b.handle(go3270.Response{AID: go3270.AIDPF3}); !back {
		t.Errorf("PF3 did not go back")
	}
}

func TestListView(t *testing.T) {
	backend := &fakeTasks{cards: map[string][]tasks.Task{"l4": ideas(25)}}
	v := newListView(backend, someday)
	clock := now
	v.now = func() time.Time { return clock }

	// Read once, and again only once changed, or old.
	if s := v.Snapshot(); len(s.Tasks) != 25 || backend.reads != 1 || !s.Fetched.Equal(now) {
		t.Fatalf("first snapshot: %d tasks, %d reads", len(s.Tasks), backend.reads)
	}
	v.Snapshot()
	if backend.reads != 1 {
		t.Errorf("read again unchanged: %d reads", backend.reads)
	}
	ctx := context.Background()
	for _, change := range []func() error{
		func() error { return v.Archive(ctx, ideas(1)[0]) },
		func() error { return v.Rename(ctx, ideas(1)[0], "x") },
		func() error { return v.Reposition(ctx, ideas(1)[0], "top") },
		func() error { return v.Move(ctx, ideas(1)[0], someday) },
	} {
		reads := backend.reads
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if v.Snapshot(); backend.reads != reads+1 {
			t.Errorf("not read again after a change")
		}
	}
	if fmt.Sprint(backend.archived, backend.renamed, backend.repos, backend.moved) != "[1] [1>x] [1:top] [1>b3/l4]" {
		t.Errorf("changes went through as %v %v %v %v", backend.archived, backend.renamed, backend.repos, backend.moved)
	}
	clock = clock.Add(2 * listViewTTL)
	reads := backend.reads
	if v.Snapshot(); backend.reads != reads+1 {
		t.Errorf("not read again once old")
	}
	// A failed read keeps the cards, saying why.
	backend.boardsErr = errors.New("401 Unauthorized")
	v.stale = true
	if s := v.Snapshot(); len(s.Tasks) != 25 || s.Err == nil {
		t.Errorf("failed read: %d tasks, err %v", len(s.Tasks), s.Err)
	}
	backend.boardsErr = nil

	// Adding goes to this list only, numbered at its bottom.
	a := listAdder{v}
	if ds, _ := a.Destinations(); len(ds) != 1 || ds[0] != someday {
		t.Errorf("add destinations %+v", ds)
	}
	if n, err := a.Add(ctx, "new idea", someday); err != nil || n != 26 || fmt.Sprint(backend.added) != "[l4:new idea]" {
		t.Errorf("added as %d, %v, %v", n, err, backend.added)
	}

	// Its tags in the move picker are the user's lists', not this one's.
	if ds, _ := v.Destinations(); len(ds) != 1 || ds[0].Tag != "work" {
		t.Errorf("destinations for tags %+v", ds)
	}
}

func TestTaskScreenOfAList(t *testing.T) {
	backend := &fakeTasks{cards: map[string][]tasks.Task{"l4": ideas(25)}}
	tp := &taskPageState{view: newListView(backend, someday)}
	if tp.source(backend) != TaskSource(tp.view) {
		t.Errorf("a list's screen does not change the list")
	}
	if _, ok := tp.adder(backend).(listAdder); !ok {
		t.Errorf("a list's screen does not add to the list")
	}
	if main := (&taskPageState{}); main.source(backend) != TaskSource(backend) || main.source(nil) != nil || main.adder(nil) != nil {
		t.Errorf("the user's tasks' screen does not change the user's tasks")
	}

	s, _, total, _, _ := buildTaskList(24, 80, now, tp.view.Snapshot(), tp)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{"TRELLO LIST", "Projects / Someday 25 open, page 1/2 (not on your task list)", "      1 idea 1", "PF5=Move", "PF6=Archive"} {
		if !strings.Contains(text, want) {
			t.Errorf("list's task screen lacks %q:\n%s", want, text)
		}
	}
	if total != 2 {
		t.Errorf("%d pages, want 2", total)
	}

	// The same keys as the user's tasks: here, marking two and archiving
	// them, then renaming one.
	all := tp.view.Snapshot().Tasks
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF6, Values: map[string]string{"mark:c0": "X", "mark:c2": "X"}}, all, total, tp.source(backend), noLog); a != taskListConfirm {
		t.Fatalf("PF6: %v, %q", a, tp.message)
	}
	tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, tp.source(backend), noLog)
	if fmt.Sprint(backend.archived) != "[1 3]" || tp.message != "Archived 2 tasks." {
		t.Errorf("archived %v: %q", backend.archived, tp.message)
	}
	buildTaskList(24, 80, now, tp.view.Snapshot(), tp)
	if a := tp.handleList(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{"title:c1": "idea two"}}, all, total, tp.source(backend), noLog); a != taskListConfirm {
		t.Fatalf("Enter with a title typed over: %v, %q", a, tp.message)
	}
	tp.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, tp.source(backend), noLog)
	if fmt.Sprint(backend.renamed) != "[2>idea two]" {
		t.Errorf("renamed %v", backend.renamed)
	}

	// A list on the task list says so.
	work := tasks.Destination{BoardID: "b1", Board: "Work", ListID: "l1", List: "Today", Tag: "work"}
	tp = &taskPageState{view: newListView(backend, work)}
	s, _, _, _, _ = buildTaskList(24, 80, now, tp.view.Snapshot(), tp)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Work / Today 0 open (your tasks tagged [work])") {
		t.Errorf("Today:\n%s", text)
	}
}
