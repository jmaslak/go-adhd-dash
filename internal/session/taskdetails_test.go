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
	if a := key(go3270.AIDPF1, taskFirstRow+1, nil); a != taskListDetails || tp.detailsOf.Number != 2 {
		t.Errorf("PF1 on task 2: %v, %+v", a, tp.detailsOf)
	}
	if a := key(go3270.AIDPF1, taskFirstRow, map[string]string{"mark:103": "X"}); a != taskListDetails || tp.detailsOf.Number != 3 {
		t.Errorf("PF1 with task 3 marked: %v, %+v", a, tp.detailsOf)
	}
	if a := key(go3270.AIDPF1, taskFirstRow, map[string]string{"mark:101": "X"}); a != taskListStay || !strings.Contains(tp.message, "Mark only one task to see its details") {
		t.Errorf("PF1 with two marked: %v, %q", a, tp.message)
	}
	tp.marked = nil
	if a := key(go3270.AIDPF1, 22, nil); a != taskListStay || !strings.Contains(tp.message, "then press PF1") {
		t.Errorf("PF1 on no task: %v, %q", a, tp.message)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF1, Row: taskFirstRow}, all, 1, nil, noLog); a != taskListStay || !strings.Contains(tp.message, "not available") {
		t.Errorf("PF1 with no tasks: %v, %q", a, tp.message)
	}
	s, _, _, _, _ := buildTaskList(24, 80, now, tasks.Snapshot{Tasks: all, Fetched: now}, &taskPageState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "PF1=Details") {
		t.Errorf("help lacks PF1:\n%s", text)
	}
}

func TestTaskDetailsScreen(t *testing.T) {
	task := tasks.Task{Number: 4, Title: "passport", CardID: "c4", Dest: tasks.Destination{Board: "Home", List: "Errands", Tag: "home"}}
	src := &fakeTasks{details: sampleDetails()}
	d := startDetails(src, task)
	if fmt.Sprint(src.detailsOf) != "[c4]" {
		t.Errorf("read %v", src.detailsOf)
	}
	s, cursorRow, cursorCol := buildTaskDetails(24, 80, now, &d)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"TASK DETAILS", "Renew the passport", "On Home / Errands [home]",
		"Due " + now.Add(-time.Hour).Format("Mon Jan 2 15:04") + " (overdue)", "Labels: urgent, blue",
		"Notes", "  Bring the old one, two photos and the form.", "  The office is on 5th street,",
		"Checklist: Before (1 of 2 done)", "  [X] photos", "  [ ] fill in the form",
		"Checklist: After (0 of 1 done)", "  [ ] file the receipt",
		"PF3=Back Enter=Save PF7=Up PF8=Down",
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
	// The title is the one input field, and the cursor is in it.
	for _, f := range s {
		if f.Write && (f.Name != detailsTitleField || f.Content != "Renew the passport") {
			t.Errorf("an input field not the title: %+v", f)
		}
	}
	if cursorRow != detailsFirstRow || cursorCol != 1 {
		t.Errorf("cursor at %d,%d; want on the title", cursorRow, cursorCol)
	}

	// More than a page: the comments on the next, PF7 and PF8 paging.
	if !strings.Contains(text, "Page 1 of 2") {
		t.Fatalf("one page only:\n%s", text)
	}
	d.handle(go3270.Response{AID: go3270.AIDPF8}, src, noLog)
	s, _, _ = buildTaskDetails(24, 80, now, &d)
	page2 := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, f := range s {
		if f.Write {
			t.Errorf("an input field past the first page: %+v", f)
		}
	}
	if !strings.Contains(text+page2, "Comments, newest first") || !strings.Contains(page2, "Joelle M, ") || !strings.Contains(page2, "    Office opens at nine.") || !strings.Contains(page2, "Page 2 of 2") {
		t.Errorf("pages:\n%s\n%s", text, page2)
	}
	d.handle(go3270.Response{AID: go3270.AIDPF8}, src, noLog) // no further
	if d.handle(go3270.Response{AID: go3270.AIDPF7}, src, noLog); d.page != 0 {
		buildTaskDetails(24, 80, now, &d)
	}
	if !d.handle(go3270.Response{AID: go3270.AIDPF3}, src, noLog) || d.handle(go3270.Response{AID: go3270.AIDEnter}, src, noLog) {
		t.Error("PF3 does not go back, or Enter does")
	}

	// Nothing but a title: no notes said so; complete, not overdue.
	src.details = tasks.Details{Title: "passport", Due: now.Add(-time.Hour), DueComplete: true}
	d = startDetails(src, task)
	s, _, _ = buildTaskDetails(24, 80, now, &d)
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, "No notes.") || !strings.Contains(text, "(complete)") || strings.Contains(text, "overdue") || strings.Contains(text, "Checklist") || strings.Contains(text, "Comments") || !strings.Contains(text, "press Enter to rename") {
		t.Errorf("bare details:\n%s", text)
	}

	// Not read: said why, under the title as listed.
	src.boardsErr = errors.New("401 Unauthorized")
	d = startDetails(src, task)
	s, _, _ = buildTaskDetails(24, 80, now, &d)
	text = strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, " passport\n") || !strings.Contains(text, "Could not read the details: 401 Unauthorized") {
		t.Errorf("failed:\n%s", text)
	}
}

func TestTaskDetailsRename(t *testing.T) {
	task := tasks.Task{Number: 2, Title: "task 2", CardID: "102"}
	src := &fakeTasks{details: tasks.Details{Title: "task 2"}}
	d := startDetails(src, task)
	key := func(aid go3270.AID, title string) bool {
		buildTaskDetails(24, 80, now, &d)
		return d.handle(go3270.Response{AID: aid, Values: map[string]string{detailsTitleField: title}}, src, noLog)
	}
	text := func() string {
		s, _, _ := buildTaskDetails(24, 80, now, &d)
		return strings.Join(screenText(t, s, 24, 80), "\n")
	}

	// Sent back as drawn, give or take spaces, is no rename, and PF3 goes
	// straight back.
	if key(go3270.AIDEnter, " task 2  ") || len(src.renamed) != 0 || d.isError {
		t.Errorf("unchanged: renamed %v, %q", src.renamed, d.message)
	}
	if !key(go3270.AIDPF3, "task 2") {
		t.Errorf("PF3 with nothing typed did not go back")
	}

	// Only Enter saves: no other key does, and each keeps it typed.
	for _, aid := range []go3270.AID{
		go3270.AIDPF1, go3270.AIDPF2, go3270.AIDPF4, go3270.AIDPF5, go3270.AIDPF6, go3270.AIDPF7, go3270.AIDPF8,
		go3270.AIDPF9, go3270.AIDPF10, go3270.AIDPF11, go3270.AIDPF12, go3270.AIDPA1, go3270.AIDPA2, go3270.AIDClear,
	} {
		d.page = 0
		if key(aid, "second task") || len(src.renamed) != 0 || d.typed == nil {
			t.Errorf("key %x: renamed %v, typed %v", aid, src.renamed, d.typed)
		}
	}
	d.page = 0
	if txt := text(); !strings.Contains(txt, " second task") {
		t.Errorf("not still typed:\n%s", txt)
	}

	// PF3 with it typed warns, saving nothing; Enter then saves it after
	// all.
	if key(go3270.AIDPF3, "second task") || !d.leaveArmed || len(src.renamed) != 0 || !strings.Contains(d.message, "PF3 again to leave without saving") {
		t.Errorf("PF3 with a title typed: armed %v, renamed %v, %q", d.leaveArmed, src.renamed, d.message)
	}
	if txt := text(); !strings.Contains(txt, " second task") {
		t.Errorf("not still typed after the warning:\n%s", txt)
	}

	// Blanked: refused, still typed.
	if key(go3270.AIDEnter, "") || len(src.renamed) != 0 || !strings.Contains(d.message, "cannot be blank") {
		t.Errorf("blanked: renamed %v, %q", src.renamed, d.message)
	}

	// Failing: stays, still typed.
	src.failOn = 2
	if key(go3270.AIDEnter, "second task") || !d.isError || !strings.Contains(d.message, `"task 2" was not renamed: Trello said no`) {
		t.Errorf("failing: %q", d.message)
	}
	if txt := text(); !strings.Contains(txt, " second task") {
		t.Errorf("not still typed:\n%s", txt)
	}

	// Renamed, then PF3 goes back at once.
	src.failOn = 0
	if key(go3270.AIDEnter, " second task ") || fmt.Sprint(src.renamed) != "[2>second task]" || d.isError || d.task.Title != "second task" {
		t.Fatalf("renaming: renamed %v, %q", src.renamed, d.message)
	}
	if txt := text(); !strings.Contains(txt, " second task") || !strings.Contains(txt, `Renamed to "second task".`) {
		t.Errorf("after renaming:\n%s", txt)
	}
	if !key(go3270.AIDPF3, "second task") || len(src.renamed) != 1 {
		t.Errorf("PF3 after renaming: renamed %v", src.renamed)
	}

	// A second PF3 leaves without saving.
	d = startDetails(src, task)
	key(go3270.AIDPF3, "third task")
	if !key(go3270.AIDPF3, "third task") || len(src.renamed) != 1 || d.typed != nil {
		t.Errorf("second PF3: renamed %v, typed %v", src.renamed, d.typed)
	}

	// Typed, then paged away: kept, and PF3 from there still warns.
	src.details = sampleDetails()
	d = startDetails(src, task)
	key(go3270.AIDPF8, "fourth task")
	if d.page != 1 || d.typed == nil {
		t.Fatalf("PF8 with a title typed: page %d, typed %v", d.page, d.typed)
	}
	buildTaskDetails(24, 80, now, &d)
	if d.handle(go3270.Response{AID: go3270.AIDPF3}, src, noLog) || !d.leaveArmed {
		t.Errorf("PF3 from page 2 with a title typed did not warn")
	}
	d.handle(go3270.Response{AID: go3270.AIDPF7}, src, noLog)
	if txt := text(); !strings.Contains(txt, " fourth task") {
		t.Errorf("not still typed on page 1:\n%s", txt)
	}

	// Nothing to rename through.
	d = startDetails(src, task)
	buildTaskDetails(24, 80, now, &d)
	d.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{detailsTitleField: "x"}}, nil, noLog)
	if !strings.Contains(d.message, "not available") {
		t.Errorf("nil source: %q", d.message)
	}

	// A long title takes more than one row.
	src.details.Title = strings.Repeat("long ", 30)
	d = startDetails(src, task)
	if txt := text(); !strings.Contains(txt, strings.Repeat("long ", 30)[80:120]) {
		t.Errorf("long title not whole:\n%s", txt)
	}
}
