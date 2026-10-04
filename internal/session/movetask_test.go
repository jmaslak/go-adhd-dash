package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// fakeMover has two boards: Work, with Today, whose tasks are shown tagged
// work, and Later, and Projects, with Someday. It records what it moves
// where, failing for the task numbered failOn.
type fakeMover struct {
	boardsErr error
	failOn    int
	moved     []string
}

func (f *fakeMover) Boards(context.Context) ([]tasks.Board, error) {
	if f.boardsErr != nil {
		return nil, f.boardsErr
	}
	return []tasks.Board{
		{ID: "b1", Name: "Work", Lists: []tasks.List{{ID: "l1", Name: "Today"}, {ID: "l2", Name: "Later"}}},
		{ID: "b3", Name: "Projects", Lists: []tasks.List{{ID: "l4", Name: "Someday"}}},
	}, nil
}

func (f *fakeMover) Destinations() ([]tasks.Destination, error) {
	return []tasks.Destination{{BoardID: "b1", Board: "Work", ListID: "l1", List: "Today", Tag: "work"}}, nil
}

func (f *fakeMover) Move(_ context.Context, t tasks.Task, d tasks.Destination) error {
	if t.Number == f.failOn {
		return errors.New("Trello said no")
	}
	f.moved = append(f.moved, fmt.Sprintf("%d>%s/%s", t.Number, d.BoardID, d.ListID))
	return nil
}

func TestTaskListMove(t *testing.T) {
	all := someTasks(5)
	tp := &taskPageState{}
	src := &fakeTasks{}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF5}, all, 1, src, noLog); a != taskListStay || !strings.Contains(tp.message, "Nothing is marked") {
		t.Errorf("PF5 with nothing marked: %v, %q", a, tp.message)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF5, Values: map[string]string{"mark:102": "X"}}, all, 1, nil, noLog); a != taskListStay || !strings.Contains(tp.message, "not available") {
		t.Errorf("PF5 with no mover: %v, %q", a, tp.message)
	}
	a := tp.handleList(go3270.Response{AID: go3270.AIDPF5, Values: map[string]string{"mark:104": "x"}}, all, 1, src, noLog)
	if a != taskListMove || len(tp.moving) != 2 || tp.moving[0].Number != 2 || tp.moving[1].Number != 4 {
		t.Errorf("PF5 with marks: %v, moving %+v", a, tp.moving)
	}
	s, _, _, _, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, &taskPageState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "PF5 moves them") || strings.Contains(text, "PF5=Move") {
		t.Errorf("task list does not offer moving:\n%s", text)
	}
}

func TestMoveTask(t *testing.T) {
	all := someTasks(5)
	mover := &fakeMover{}
	tp := &taskPageState{marked: map[string]bool{"102": true, "104": true}}
	m := startMove(mover, []tasks.Task{all[1], all[3]})
	key := func(aid go3270.AID, values map[string]string) (bool, string, bool) {
		return m.handle(go3270.Response{AID: aid, Values: values}, mover, tp, noLog)
	}

	// Every list on every board, the one shown with its tag.
	s, crow, ccol := buildMoveTask(24, 80, now, &m)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Move 2 tasks to which list?", "Work / Today [work]", "Work / Later", "Projects / Someday"} {
		if !strings.Contains(text, want) {
			t.Errorf("lists screen lacks %q:\n%s", want, text)
		}
	}
	if crow != taskFirstRow || ccol != 1 {
		t.Errorf("cursor at %d,%d; want the first selection", crow, ccol)
	}

	// Enter with nothing, or two, selected stays.
	if back, _, _ := key(go3270.AIDEnter, nil); back || !m.isError || m.confirming {
		t.Errorf("Enter with nothing selected: back %v, message %q", back, m.message)
	}
	if back, _, _ := key(go3270.AIDEnter, map[string]string{"dest:l2": "S", "dest:l4": "x"}); back || m.message != "Select only one list." {
		t.Errorf("two selected: back %v, message %q", back, m.message)
	}

	// One selected: the confirmation, which says its tasks will not be
	// shown; PF3 there goes back to the lists, still selected.
	key(go3270.AIDEnter, map[string]string{"dest:l2": "", "dest:l4": "S"})
	if !m.confirming {
		t.Fatalf("Enter with one selected did not confirm: %q", m.message)
	}
	s, _, _ = buildMoveTask(24, 80, now, &m)
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Move 2 tasks to Projects / Someday?", "task 2", "task 4", "whose tasks are not shown here", "PF4=Move"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	if back, _, _ := key(go3270.AIDPF3, nil); back || m.confirming || m.picked != "l4" {
		t.Errorf("PF3 on the confirmation: back %v, confirming %v, picked %q", back, m.confirming, m.picked)
	}
	if len(mover.moved) != 0 {
		t.Fatalf("moved before PF4: %v", mover.moved)
	}

	// PF4 moves them, and goes back with their marks gone.
	key(go3270.AIDEnter, nil)
	back, said, isError := key(go3270.AIDPF4, nil)
	if !back || isError || said != "Moved 2 tasks to Projects / Someday." {
		t.Errorf("PF4: back %v, %q, error %v", back, said, isError)
	}
	if fmt.Sprint(mover.moved) != "[2>b3/l4 4>b3/l4]" || len(tp.marked) != 0 {
		t.Errorf("moved %v, marks left %v", mover.moved, tp.marked)
	}

	// To a list shown, the confirmation names its tag.
	m = startMove(mover, []tasks.Task{all[0]})
	key(go3270.AIDEnter, map[string]string{"dest:l1": "S"})
	s, _, _ = buildMoveTask(24, 80, now, &m)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Move 1 task to Work / Today?") || !strings.Contains(text, "tagged [work]") {
		t.Errorf("confirmation to a list shown:\n%s", text)
	}

	// A failure stops there, keeping the mark of the one that failed.
	mover.failOn, mover.moved = 3, nil
	tp.marked = map[string]bool{"101": true, "103": true, "105": true}
	m = startMove(mover, []tasks.Task{all[0], all[2], all[4]})
	key(go3270.AIDEnter, map[string]string{"dest:l2": "S"})
	back, said, isError = key(go3270.AIDPF4, nil)
	if !back || !isError || !strings.Contains(said, "Moved 1 of 3. Task 3") || fmt.Sprint(mover.moved) != "[1>b1/l2]" || !tp.marked["103"] || !tp.marked["105"] || tp.marked["101"] {
		t.Errorf("failing: back %v, %q, moved %v, marks %v", back, said, mover.moved, tp.marked)
	}

	// PF3 on the lists goes back, moving nothing.
	mover.moved = nil
	m = startMove(mover, []tasks.Task{all[0]})
	if back, said, _ := key(go3270.AIDPF3, map[string]string{"dest:l2": "S"}); !back || said != "" || len(mover.moved) != 0 {
		t.Errorf("PF3 on the lists: back %v, %q", back, said)
	}
}

func TestMoveTaskPagesAndErrors(t *testing.T) {
	// A selection on one page survives paging and is kept unless replaced.
	m := moveTaskState{moving: someTasks(1)}
	for i := range 30 {
		m.dests = append(m.dests, tasks.Destination{BoardID: "b", Board: "B", ListID: fmt.Sprint("l", i), List: fmt.Sprint("list ", i)})
	}
	mover := &fakeMover{}
	tp := &taskPageState{}
	m.handle(go3270.Response{AID: go3270.AIDPF8, Values: map[string]string{"dest:l3": "S"}}, mover, tp, noLog)
	s, _, _ := buildMoveTask(24, 80, now, &m)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if m.page != 1 || m.picked != "l3" || !strings.Contains(text, "page 2/2") || !strings.Contains(text, "list 29") {
		t.Errorf("after PF8: page %d, picked %q:\n%s", m.page, m.picked, text)
	}
	m.handle(go3270.Response{AID: go3270.AIDPF7, Values: map[string]string{"dest:l20": ""}}, mover, tp, noLog)
	if m.page != 0 || m.picked != "l3" {
		t.Errorf("after PF7: page %d, picked %q", m.page, m.picked)
	}
	s, _, _ = buildMoveTask(24, 80, now, &m)
	for _, f := range s {
		if f.Name == "dest:l3" && f.Content != "S" {
			t.Errorf("the selection is not shown: %+v", f)
		}
	}

	// Boards that cannot be read say why.
	m = startMove(&fakeMover{boardsErr: errors.New("401 Unauthorized")}, someTasks(1))
	s, _, _ = buildMoveTask(24, 80, now, &m)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Could not read your Trello boards: 401 Unauthorized") {
		t.Errorf("no boards:\n%s", text)
	}
	if back, _, _ := m.handle(go3270.Response{AID: go3270.AIDEnter}, mover, tp, noLog); back || !m.isError {
		t.Errorf("Enter with no boards: back %v, message %q", back, m.message)
	}
}
