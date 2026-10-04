package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// sampleDetails are a card's details with something of every kind.
func sampleDetails() tasks.Details {
	return tasks.Details{
		Title:  "Renew the passport",
		Notes:  "Bring the old one, two photos and the form.\nThe office is on 5th street, " + strings.Repeat("and it is a long way ", 6) + "away.",
		Due:    now.Add(-time.Hour),
		Labels: []string{"urgent", "blue"},
		Checklists: []tasks.Checklist{
			{Name: "Before", Items: []tasks.CheckItem{{Name: "photos", Done: true}, {Name: "fill in the form", Done: false}}},
			{Name: "After", Items: []tasks.CheckItem{{Name: "file the receipt"}}},
		},
		Comments: []tasks.Comment{{Author: "Joelle M", Date: now.Add(-24 * time.Hour), Text: "Office opens at nine."}},
	}
}

func TestTaskDetailsKeys(t *testing.T) {
	all := someTasks(3)
	src := &fakeTasks{details: sampleDetails()}
	tp := &taskPageState{}
	key := func(aid go3270.AID, row int, values map[string]string) taskListAction {
		buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, tp)
		return tp.handleList(go3270.Response{AID: aid, Row: row, Values: values}, all, 1, src, noLog)
	}

	// The task the cursor is on, or the one marked.
	if a := key(go3270.AIDPF2, taskFirstRow+1, nil); a != taskListDetails || tp.detailsOf.Number != 2 {
		t.Errorf("PF2 on task 2: %v, %+v", a, tp.detailsOf)
	}
	if a := key(go3270.AIDPF2, taskFirstRow, map[string]string{"mark:103": "X"}); a != taskListDetails || tp.detailsOf.Number != 3 {
		t.Errorf("PF2 with task 3 marked: %v, %+v", a, tp.detailsOf)
	}
	if a := key(go3270.AIDPF2, taskFirstRow, map[string]string{"mark:101": "X"}); a != taskListStay || !strings.Contains(tp.message, "Mark only one task to see its details") {
		t.Errorf("PF2 with two marked: %v, %q", a, tp.message)
	}
	tp.marked = nil
	if a := key(go3270.AIDPF2, 22, nil); a != taskListStay || !strings.Contains(tp.message, "then press PF2") {
		t.Errorf("PF2 on no task: %v, %q", a, tp.message)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF2, Row: taskFirstRow}, all, 1, nil, noLog); a != taskListStay || !strings.Contains(tp.message, "not available") {
		t.Errorf("PF2 with no tasks: %v, %q", a, tp.message)
	}
	s, _, _, _, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, &taskPageState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "PF2=Details") {
		t.Errorf("help lacks PF2:\n%s", text)
	}
}

func TestTaskDetailsScreen(t *testing.T) {
	task := tasks.Task{Number: 4, Title: "passport", CardID: "c4", Dest: tasks.Destination{Board: "Home", List: "Errands", Tag: "home"}}
	src := &fakeTasks{details: sampleDetails()}
	d := startDetails(src, task)
	if fmt.Sprint(src.detailsOf) != "[c4]" {
		t.Errorf("read %v", src.detailsOf)
	}
	s := buildTaskDetails(24, 80, now, &d)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"TASK DETAILS", "Task 4: Renew the passport", "On Home / Errands [home]",
		"Due " + now.Add(-time.Hour).Local().Format("Mon Jan 2 15:04") + " (overdue)", "Labels: urgent, blue",
		"Notes", "  Bring the old one, two photos and the form.", "  The office is on 5th street,",
		"Checklist: Before (1 of 2 done)", "  [X] photos", "  [ ] fill in the form",
		"Checklist: After (0 of 1 done)", "  [ ] file the receipt",
		"PF3=Back PF7=Up PF8=Down",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("details lack %q:\n%s", want, text)
		}
	}
	for _, row := range rows {
		if len([]rune(row)) > 80 {
			t.Errorf("row too long: %q", row)
		}
	}
	for _, f := range s {
		if f.Write {
			t.Errorf("an input field on a screen only to look at: %+v", f)
		}
	}

	// More than a page: the comments on the next, PF7 and PF8 paging.
	if !strings.Contains(text, "Page 1 of 2") {
		t.Fatalf("one page only:\n%s", text)
	}
	d.handle(go3270.Response{AID: go3270.AIDPF8})
	page2 := strings.Join(screenText(t, buildTaskDetails(24, 80, now, &d), 24, 80), "\n")
	if !strings.Contains(text+page2, "Comments, newest first") || !strings.Contains(page2, "Joelle M, ") || !strings.Contains(page2, "    Office opens at nine.") || !strings.Contains(page2, "Page 2 of 2") {
		t.Errorf("pages:\n%s\n%s", text, page2)
	}
	d.handle(go3270.Response{AID: go3270.AIDPF8}) // no further
	if d.handle(go3270.Response{AID: go3270.AIDPF7}); d.page != 0 {
		buildTaskDetails(24, 80, now, &d)
	}
	if !d.handle(go3270.Response{AID: go3270.AIDPF3}) || d.handle(go3270.Response{AID: go3270.AIDEnter}) {
		t.Error("PF3 does not go back, or Enter does")
	}

	// Nothing but a title: no notes said so; complete, not overdue.
	src.details = tasks.Details{Title: "passport", Due: now.Add(-time.Hour), DueComplete: true}
	d = startDetails(src, task)
	text = strings.Join(screenText(t, buildTaskDetails(24, 80, now, &d), 24, 80), "\n")
	if !strings.Contains(text, "No notes.") || !strings.Contains(text, "(complete)") || strings.Contains(text, "overdue") || strings.Contains(text, "Checklist") || strings.Contains(text, "Comments") || !strings.Contains(text, "Only to look at") {
		t.Errorf("bare details:\n%s", text)
	}

	// Not read: said why, under the title as listed.
	src.boardsErr = errors.New("401 Unauthorized")
	d = startDetails(src, task)
	text = strings.Join(screenText(t, buildTaskDetails(24, 80, now, &d), 24, 80), "\n")
	if !strings.Contains(text, "Task 4: passport") || !strings.Contains(text, "Could not read the details: 401 Unauthorized") {
		t.Errorf("failed:\n%s", text)
	}
}
