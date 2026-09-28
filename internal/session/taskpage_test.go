package session

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// someTasks are n open tasks with IDs 101 up; the second mirrors a Trello
// card.
func someTasks(n int) []tasks.Task {
	var out []tasks.Task
	for i := range n {
		t := tasks.Task{Number: i + 1, Title: fmt.Sprint("task ", i+1), ID: big.NewInt(int64(101 + i))}
		if i == 1 {
			t.TrelloID = "card2"
		}
		out = append(out, t)
	}
	return out
}

// fakeArchiver records what it archives, refusing Check with checkErr and
// failing Archive for the task numbered failOn.
type fakeArchiver struct {
	checkErr error
	failOn   int
	archived []int
}

func (f *fakeArchiver) Check([]tasks.Task) error { return f.checkErr }

func (f *fakeArchiver) Archive(_ context.Context, t tasks.Task) error {
	if t.Number == f.failOn {
		return errors.New("Trello said no")
	}
	f.archived = append(f.archived, t.Number)
	return nil
}

func noLog(string, ...any) {}

func TestTaskListScreen(t *testing.T) {
	all := someTasks(25)
	all[2].ID = nil // not yet upgraded: cannot be marked
	tp := &taskPageState{marked: map[string]bool{"101": true}}

	s, shown, total, crow, ccol := buildTaskList(24, 80, now, all, nil, tp)
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
	// The underline must end with the headings: a field's highlighting runs
	// to the next attribute byte.
	ended := false
	for _, f := range s {
		if f.Row == taskColumnRow && f.Col == len(" S  Num Task") && f.Highlighting == go3270.DefaultHighlight {
			ended = true
		}
	}
	if !ended {
		t.Errorf("no plain attribute right after the column headings, so the underline runs on")
	}
	if rows[taskFirstRow] != " X    1 task 1" || rows[taskFirstRow+1] != "      2 task 2" {
		t.Errorf("first rows are %q, %q", rows[taskFirstRow], rows[taskFirstRow+1])
	}
	if !strings.Contains(rows[22], "Type X beside each task") || !strings.Contains(rows[23], "PF3=Back") {
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
	if len(fields) != 17 {
		t.Errorf("%d mark fields, want 17: the task without an ID gets none", len(fields))
	}
	if f := fields["mark:101"]; f.Content != "X" || f.Row != taskFirstRow || f.Col != 0 {
		t.Errorf("task 1's mark field is %+v", f)
	}

	tp.page = 1
	s, _, _, _, _ = buildTaskList(24, 80, now, all, nil, tp)
	if rows := screenText(t, s, 24, 80); !strings.HasPrefix(rows[taskFirstRow], "     19 task 19") {
		t.Errorf("second page starts %q", rows[taskFirstRow])
	}
}

func TestTaskListMarking(t *testing.T) {
	all := someTasks(25)
	arch := &fakeArchiver{}
	tp := &taskPageState{}
	key := func(aid go3270.AID, values map[string]string) (confirm, leave bool) {
		return tp.handleList(go3270.Response{AID: aid, Values: values}, all, 2, arch)
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

	arch.checkErr = errors.New("no Trello credentials")
	tp.confirming = nil
	if confirm, _ := key(go3270.AIDPF6, nil); confirm || !strings.Contains(tp.message, "no Trello credentials") {
		t.Errorf("failed check: confirm %v, message %q", confirm, tp.message)
	}

	if _, leave := key(go3270.AIDPF3, nil); !leave {
		t.Errorf("PF3 did not leave the task screen")
	}
}

func TestArchiveConfirm(t *testing.T) {
	all := someTasks(3)
	s := buildArchiveConfirm(24, 80, now, all)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"Archive these 3 tasks?", "   2 task 2", "1 of them mirror Trello cards", "Press PF4 to archive", "PF4=Archive"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}

	arch := &fakeArchiver{}
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
