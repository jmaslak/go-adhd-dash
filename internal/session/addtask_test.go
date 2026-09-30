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

// fakeAdder offers dests and records what it is asked to add, failing with
// err when set.
type fakeAdder struct {
	dests   []tasks.Destination
	destErr error
	err     error
	added   []string
}

func (f *fakeAdder) Destinations() ([]tasks.Destination, error) { return f.dests, f.destErr }

func (f *fakeAdder) Add(_ context.Context, title string, d tasks.Destination) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.added = append(f.added, title+" -> "+d.String())
	return 42, nil
}

var twoDests = []tasks.Destination{{Board: "Home", List: "Inbox", Tag: "home"}, {Board: "Work", List: "To Do", Tag: "work"}}

func TestAddTaskScreen(t *testing.T) {
	var a addTaskState
	s, row, col := buildAddTask(24, 80, now, &fakeAdder{dests: twoDests}, &a)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{"Title ===>", "  1 Home / Inbox [home]", "  2 Work / To Do [work]", "Board ===>", "PF3=Cancel Enter=Add"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(rows[atTitleRow], " Title ===>") || !strings.HasPrefix(rows[atBoardRow], " Board ===>") || !strings.HasPrefix(rows[atBoardsRow], " No.") {
		t.Errorf("want the title, then the board, then the boards:\n%s", text)
	}
	if row != atTitleRow || col != len(atTitleLabel)+2 {
		t.Errorf("cursor at %d,%d, want the title", row, col)
	}

	// One board is picked already.
	s, _, _ = buildAddTask(24, 80, now, &fakeAdder{dests: twoDests[:1]}, &a)
	for _, f := range s {
		if f.Name == atBoardField && f.Content != "1" {
			t.Errorf("sole board not picked: %+v", f)
		}
	}

	for _, c := range []struct {
		adder TaskAdder
		want  string
	}{
		{&fakeAdder{}, "No Trello lists are configured"},
		{&fakeAdder{destErr: errors.New("reading the task configuration: bad")}, "reading the task configuration: bad"},
		{nil, "not available"},
	} {
		s, _, _ = buildAddTask(24, 80, now, c.adder, &addTaskState{})
		if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, c.want) {
			t.Errorf("screen lacks %q:\n%s", c.want, text)
		}
	}

	// More boards than fit.
	var many []tasks.Destination
	for i := range 30 {
		many = append(many, tasks.Destination{Board: fmt.Sprint("b", i), List: "l", Tag: "t"})
	}
	s, _, _ = buildAddTask(24, 80, now, &fakeAdder{dests: many}, &addTaskState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "... and 18 more, 13 to 30") {
		t.Errorf("boards that do not fit not summarized:\n%s", text)
	}
}

func TestAddTaskHandle(t *testing.T) {
	enter := func(title, board string) go3270.Response {
		return go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{atTitleField: title, atBoardField: board}}
	}
	adder := &fakeAdder{dests: twoDests}
	var a addTaskState
	buildAddTask(24, 80, now, adder, &a)

	for _, c := range []struct {
		title, board, want string
		onBoard            bool
	}{
		{"  ", "1", "Type the task's title.", false},
		{"x", "", "Type a board's number, 1 to 2.", true},
		{"x", "3", "Type a board's number, 1 to 2.", true},
		{"x", "z", "Type a board's number, 1 to 2.", true},
	} {
		if leave, added := a.handle(enter(c.title, c.board), adder); leave || added != "" || a.message != c.want || a.cursorOnBoard != c.onBoard || !a.isError {
			t.Errorf("%q %q: leave %v, added %q, message %q, on board %v", c.title, c.board, leave, added, a.message, a.cursorOnBoard)
		}
		if a.title != c.title || a.board != c.board {
			t.Errorf("%q %q: typed kept as %q %q", c.title, c.board, a.title, a.board)
		}
	}

	adder.err = errors.New("trello: 1/cards: 401 Unauthorized")
	if leave, _ := a.handle(enter("x", "2"), adder); leave || !strings.Contains(a.message, "Could not add: trello") {
		t.Errorf("failed add: leave %v, message %q", leave, a.message)
	}

	adder.err = nil
	leave, added := a.handle(enter(" write report ", " 2"), adder)
	if !leave || added != "Added task 42 to Work / To Do." || fmt.Sprint(adder.added) != "[write report -> Work / To Do]" {
		t.Errorf("add: leave %v, added %q, adder got %v", leave, added, adder.added)
	}

	if leave, _ := a.handle(go3270.Response{AID: go3270.AIDPF3}, adder); !leave {
		t.Error("PF3 does not leave")
	}
}
