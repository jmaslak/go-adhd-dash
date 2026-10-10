// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/db"
	"github.com/jmaslak/go-adhd-dash/internal/recurring"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

// openRecurring makes recurring tasks in a new database, closed when the
// test ends.
func openRecurring(t *testing.T) *recurring.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "adhd-dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) //nolint:errcheck
	s, err := recurring.New(d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addRecurring adds a task for owner 1 every interval days from today,
// at the time at (hh:mm).
func addRecurring(t *testing.T, s *recurring.Store, title string, interval int, at string) recurring.Task {
	t.Helper()
	minutes, err := parseTimeOfDay(at)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Add(recurring.Task{Owner: 1, Title: title, Schedule: recurring.Every, Interval: interval, At: minutes, Start: recurring.DateOf(now)}, recurring.DateOf(now))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUserTasks(t *testing.T) {
	s := openRecurring(t)
	due := addRecurring(t, s, "Water plants", 2, "09:00") // now is 10:00
	later := addRecurring(t, s, "Stretch", 1, "15:30")
	trello := &fakeTasks{snap: tasks.Snapshot{Tasks: someTasks(2), Fetched: now}}
	u := userTasks{trello: trello, store: s, owner: 1, now: func() time.Time { return now }}

	snap := u.Snapshot()
	if len(snap.Tasks) != 4 || snap.Err != nil {
		t.Fatalf("snapshot: %+v", snap)
	}
	got := snap.Tasks[2]
	if got.Number != 3 || got.Title != "Water plants" || strings.Join(got.Tags, "|") != "recurring|due 09:00" || notYetDue(got) {
		t.Errorf("the recurring task due: %+v", got)
	}
	if got := snap.Tasks[3]; got.Number != 4 || strings.Join(got.Tags, "|") != "recurring|next 15:30" || !notYetDue(got) {
		t.Errorf("the recurring task not due: %+v", got)
	}
	if isRecurring(snap.Tasks[0]) {
		t.Errorf("a Trello task is recurring: %+v", snap.Tasks[0])
	}
	// On the task screen, recurring tasks are colored as on the
	// dashboard: the one due an hour green, the one due later blue; one
	// due over an hour red, its tags too.
	late := addRecurring(t, s, "Pay rent", 3, "08:59")
	screen, _, _, _, _ := buildTaskList(24, 80, now, u.Snapshot(), &taskPageState{})
	colors := map[string]go3270.Color{}
	for _, f := range screen {
		if f.Row >= taskFirstRow {
			colors[f.Content] = f.Color
		}
	}
	for content, want := range map[string]go3270.Color{
		"task 1": go3270.Green, "Water plants": go3270.Green, "Stretch": go3270.Blue,
		"Pay rent": go3270.Red, "[recurring] [due 08:59]": go3270.Red, "[recurring] [due 09:00]": go3270.Turquoise,
	} {
		if got, ok := colors[content]; !ok || got != want {
			t.Errorf("%q in %v (shown %v), want %v", content, got, ok, want)
		}
	}
	if err := s.Remove(1, late.ID); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := u.Move(ctx, snap.Tasks[2], tasks.Destination{}); err == nil {
		t.Error("moved a recurring task")
	}
	if err := u.Reposition(ctx, snap.Tasks[2], "top"); err == nil {
		t.Error("repositioned a recurring task")
	}
	if err := u.Rename(ctx, snap.Tasks[3], "Stretch well"); err != nil {
		t.Error(err)
	}
	if err := u.Archive(ctx, snap.Tasks[0]); err != nil || len(trello.archived) != 1 {
		t.Errorf("archiving a Trello task: %v, %v", err, trello.archived)
	}
	if err := u.Archive(ctx, snap.Tasks[2]); err != nil {
		t.Fatal(err)
	}
	// Archived as shown again, it is not done twice.
	if err := u.Archive(ctx, snap.Tasks[2]); err == nil {
		t.Error("done twice for one showing")
	}
	r, err := s.Get(1, due.ID)
	if err != nil || !r.Due.Equal(recurring.DateOf(now).AddDays(2)) {
		t.Errorf("after done: %+v, %v", r, err)
	}
	if r, _ := s.Get(1, later.ID); r.Title != "Stretch well" {
		t.Errorf("renamed: %+v", r)
	}

	// Without Trello, the recurring tasks alone, and Trello changes refused.
	u.trello = nil
	snap = u.Snapshot()
	if len(snap.Tasks) != 2 || snap.Err != nil || snap.Fetched.IsZero() {
		t.Errorf("without Trello: %+v", snap)
	}
	if err := u.Archive(ctx, someTasks(1)[0]); err == nil {
		t.Error("archived a Trello task with no Trello")
	}
}

func TestDashboardRecurring(t *testing.T) {
	v := sampleView(2)
	day := recurring.DateOf(now)
	v.Recurring = []recurring.Task{
		{ID: 1, Title: "Water plants", At: 9 * 60, Due: day},
		{ID: 2, Title: "Stretch", At: 15 * 60, Due: day},
		{ID: 3, Title: "Pay rent", At: 8 * 60, Due: day.AddDays(-2)},
		{ID: 4, Title: "Bins", At: 7 * 60, Due: day.AddDays(1)},
		{ID: 5, Title: "Feed cat", At: 8*60 + 59, Due: day},
		{ID: 6, Title: "Make tea", At: 11 * 60, Due: day},
		{ID: 7, Title: "Hidden soon", At: 11 * 60, Due: day, HideUntilDue: true},
		{ID: 8, Title: "Hidden later", At: 15 * 60, Due: day, HideUntilDue: true},
		{ID: 9, Title: "Shown once due", At: 9 * 60, Due: day, HideUntilDue: true},
	}
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"TASKS 7 open", "[recurring] Water plants (due 09:00)", "[recurring] Shown once due (due 09:00)", "[recurring] Pay rent (due Fri 08:00)", "[recurring] Stretch (next 15:00)", "[recurring] Make tea (next 11:00)"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Bins") {
		t.Errorf("a recurring task due tomorrow is shown:\n%s", text)
	}
	if strings.Contains(text, "Hidden") {
		t.Errorf("a recurring task hidden until due is shown before it is due:\n%s", text)
	}
	// Due an hour, or due within the next hour, green; due over an hour,
	// past due, red, whatever the day; due later today, blue.
	for _, f := range s {
		switch {
		case strings.Contains(f.Content, "Water plants") || strings.Contains(f.Content, "Make tea"):
			if f.Color != go3270.Green {
				t.Errorf("due an hour, or due within the next, not in green: %+v", f)
			}
		case strings.Contains(f.Content, "Pay rent") || strings.Contains(f.Content, "due Fri 08:00") ||
			strings.Contains(f.Content, "Feed cat") || strings.Contains(f.Content, "due 08:59"):
			if f.Color != go3270.Red {
				t.Errorf("past due, not in red: %+v", f)
			}
		case strings.Contains(f.Content, "Stretch") || strings.Contains(f.Content, "next 15:00"):
			if f.Color != go3270.Blue {
				t.Errorf("due later today, not in blue: %+v", f)
			}
		}
	}
}

func TestDueSoon(t *testing.T) {
	day := recurring.DateOf(now)
	late := day.At(23*60+30, time.UTC)
	for _, tc := range []struct {
		name string
		task recurring.Task
		at   time.Time
		want bool
	}{
		{"in an hour", recurring.Task{At: 11 * 60, Due: day}, now, true},
		{"in over an hour", recurring.Task{At: 11*60 + 1, Due: day}, now, false},
		{"already due", recurring.Task{At: 9 * 60, Due: day}, now, false},
		{"just after midnight", recurring.Task{At: 15, Due: day.AddDays(1)}, late, true},
	} {
		if got := dueSoon(tc.task, tc.at); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// recurringForm is what is typed on the recurring task screen: a task on
// Monday and Wednesday at 09:00 from today, changed by the pairs given.
func recurringForm(pairs ...string) map[string]string {
	v := map[string]string{
		rtTitleField: "Water plants", rtTimeField: "09:00", rtStartField: "",
		rtDayField + "0": "X", rtDayField + "2": "x", rtWeeksField: "1", rtApartField: "", rtAfterField: "N", rtHideField: "N",
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		v[pairs[i]] = pairs[i+1]
	}
	return v
}

func TestRecurringParse(t *testing.T) {
	today := recurring.DateOf(now)
	var r recurringState
	got, field, bad := r.parse(recurringForm(), now)
	if bad != "" || got.Schedule != recurring.Weekly || got.Weeks != 1 || got.At != 9*60 || !got.Start.Equal(today) ||
		!got.Days.Has(time.Monday) || !got.Days.Has(time.Wednesday) || got.Days.Has(time.Tuesday) {
		t.Errorf("weekly: %+v, %q %q", got, field, bad)
	}
	if got.HideUntilDue {
		t.Errorf("hidden until due with N: %+v", got)
	}
	for _, hide := range []string{"y", " Y "} {
		if got, _, bad = r.parse(recurringForm(rtHideField, hide), now); bad != "" || !got.HideUntilDue {
			t.Errorf("hide %q: %+v, %q", hide, got, bad)
		}
	}
	if got, _, bad = r.parse(recurringForm(rtHideField, ""), now); bad != "" || got.HideUntilDue {
		t.Errorf("hide blank: %+v, %q", got, bad)
	}
	got, _, bad = r.parse(recurringForm(rtDayField+"0", "", rtDayField+"2", "", rtApartField, "3", rtAfterField, "y"), now)
	if bad != "" || got.Schedule != recurring.After || got.Interval != 3 || got.Days != 0 {
		t.Errorf("after done: %+v, %q", got, bad)
	}
	got, _, bad = r.parse(recurringForm(rtDayField+"0", "", rtDayField+"2", "", rtApartField, "3"), now)
	if bad != "" || got.Schedule != recurring.Every {
		t.Errorf("every: %+v, %q", got, bad)
	}

	for name, tc := range map[string]struct {
		values []string
		field  string
	}{
		"no title":         {[]string{rtTitleField, "  "}, rtTitleField},
		"no time":          {[]string{rtTimeField, ""}, rtTimeField},
		"bad time":         {[]string{rtTimeField, "25:00"}, rtTimeField},
		"seconds":          {[]string{rtTimeField, "09:00:30"}, rtTimeField},
		"bad start":        {[]string{rtStartField, "tomorrow"}, rtStartField},
		"start before":     {[]string{rtStartField, today.AddDays(-1).String()}, rtStartField},
		"bad mark":         {[]string{rtDayField + "1", "Y"}, rtDayField + "1"},
		"both":             {[]string{rtApartField, "3"}, rtApartField},
		"after and weekly": {[]string{rtAfterField, "Y"}, rtAfterField},
		"no weeks":         {[]string{rtWeeksField, "0"}, rtWeeksField},
		"neither":          {[]string{rtDayField + "0", "", rtDayField + "2", ""}, rtDayField + "0"},
		"bad apart":        {[]string{rtDayField + "0", "", rtDayField + "2", "", rtApartField, "0"}, rtApartField},
		"bad hide":         {[]string{rtHideField, "X"}, rtHideField},
	} {
		if _, field, bad := r.parse(recurringForm(tc.values...), now); bad == "" || field != tc.field {
			t.Errorf("%s: %q on %q, want an error on %q", name, bad, field, tc.field)
		}
	}

	// Editing a task that started before today, its start kept, is fine.
	r.task = recurring.Task{ID: 1, Start: today.AddDays(-30)}
	if _, _, bad := r.parse(recurringForm(rtStartField, today.AddDays(-30).String()), now); bad != "" {
		t.Errorf("keeping an old start: %q", bad)
	}
	if _, _, bad := r.parse(recurringForm(rtStartField, today.AddDays(-29).String()), now); bad == "" {
		t.Error("moved the start to before today")
	}
}

func TestRecurringScreen(t *testing.T) {
	s := openRecurring(t)
	var r recurringState
	screen, crow, ccol := buildRecurring(24, 80, now, &r)
	text := strings.Join(screenText(t, screen, 24, 80), "\n")
	for _, want := range []string{"NEW RECURRING TASK", "Title      ===>", "Mon   Tue   Wed", "Days apart ===>", "After done ===>", "Hide       ===> N", "PF3=Cancel Enter=Save", recurring.DateOf(now).String()} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "PF6") || crow != rtTitleRow || ccol != rtLabelWidth+1 {
		t.Errorf("new task: cursor %d,%d:\n%s", crow, ccol, text)
	}

	// A mistake keeps what was typed, the cursor on it.
	enter := func(values map[string]string) (bool, string, bool) {
		return r.handle(go3270.Response{AID: go3270.AIDEnter, Values: values}, s, 1, now, noLog)
	}
	if back, _, _ := enter(recurringForm(rtTimeField, "9am")); back || !r.isError {
		t.Fatalf("a bad time saved: %q", r.message)
	}
	screen, crow, _ = buildRecurring(24, 80, now, &r)
	if text := strings.Join(screenText(t, screen, 24, 80), "\n"); !strings.Contains(text, "9am") || crow != rtTimeRow {
		t.Errorf("after a bad time, cursor on row %d:\n%s", crow, text)
	}

	// Starting today at a time gone by, it is due at once.
	daily := []string{rtDayField + "0", "", rtDayField + "2", "", rtApartField, "2"}
	back, said, isError := enter(recurringForm(append(daily, rtTimeField, "0815")...))
	if !back || isError || !strings.Contains(said, "due now") {
		t.Fatalf("added: %v %q %v", back, said, isError)
	}
	list, _ := s.List(1)
	if len(list) != 1 || !list[0].IsDue(now) {
		t.Fatalf("saved: %+v", list)
	}

	// Edited, it shows what was saved, and when it is due.
	r = startRecurring(s, 1, list[0].ID)
	screen, _, _ = buildRecurring(24, 80, now, &r)
	text = strings.Join(screenText(t, screen, 24, 80), "\n")
	for _, want := range []string{"RECURRING TASK", "Water plants", "08:15", "Due now, since 08:15", "Hide       ===> N", "PF6=Delete"} {
		if !strings.Contains(text, want) {
			t.Errorf("editing, screen lacks %q:\n%s", want, text)
		}
	}
	back, said, _ = enter(recurringForm(append(daily, rtTimeField, "08:15", rtTitleField, "Water the plants")...))
	if !back || !strings.HasPrefix(said, "Saved") {
		t.Errorf("saved: %v %q", back, said)
	}
	if got, _ := s.Get(1, list[0].ID); got.Title != "Water the plants" || !got.Due.Equal(list[0].Due) {
		t.Errorf("after editing the title: %+v", got)
	}
	r = startRecurring(s, 1, list[0].ID)
	if back, _, _ := enter(recurringForm(append(daily, rtTimeField, "08:15", rtHideField, "Y")...)); !back {
		t.Errorf("hiding until due: %q", r.message)
	}
	r = startRecurring(s, 1, list[0].ID)
	screen, _, _ = buildRecurring(24, 80, now, &r)
	if text := strings.Join(screenText(t, screen, 24, 80), "\n"); !r.task.HideUntilDue || !strings.Contains(text, "Hide       ===> Y") {
		t.Errorf("editing one hidden until due:\n%s", text)
	}

	// PF6 asks; PF4 then deletes; any other key between forgets it, and
	// PF4 alone does nothing.
	r = startRecurring(s, 1, list[0].ID)
	pf4 := go3270.Response{AID: go3270.AIDPF4, Values: recurringForm()}
	pf6 := go3270.Response{AID: go3270.AIDPF6, Values: recurringForm()}
	if back, _, _ := r.handle(pf4, s, 1, now, noLog); back {
		t.Fatal("PF4 alone deleted")
	}
	if back, _, _ := r.handle(pf6, s, 1, now, noLog); back || !strings.Contains(r.message, "Press PF4 to delete") {
		t.Fatalf("PF6: %q", r.message)
	}
	if back, _, _ := r.handle(pf6, s, 1, now, noLog); back {
		t.Fatal("PF6 twice deleted")
	}
	r.handle(go3270.Response{AID: go3270.AIDPF7}, s, 1, now, noLog)
	if back, _, _ := r.handle(pf4, s, 1, now, noLog); back {
		t.Fatal("PF4 after another key deleted")
	}
	r.handle(pf6, s, 1, now, noLog)
	if back, said, _ := r.handle(pf4, s, 1, now, noLog); !back || !strings.Contains(said, "Deleted") {
		t.Fatalf("PF6 then PF4: %v %q", back, said)
	}
	if list, _ := s.List(1); len(list) != 0 {
		t.Errorf("left after deleting: %+v", list)
	}
	if r := startRecurring(s, 1, list[0].ID); r.loadErr == nil {
		t.Error("editing a deleted task read it")
	}
}

func TestTaskListRecurringKeys(t *testing.T) {
	all := append(someTasks(1), tasks.Task{Number: 2, Title: "Water plants", Tags: []string{recurringTag, "due 09:00"}, CardID: "recurring:7:2026-09-27:540"})
	tp := &taskPageState{}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF2}, all, 1, &fakeTasks{}, noLog); a != taskListRecurring {
		t.Errorf("PF2: %v", a)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF5, Values: map[string]string{"mark:recurring:7:2026-09-27:540": "X"}}, all, 1, &fakeTasks{}, noLog); a != taskListStay || !strings.Contains(tp.message, "cannot be moved") {
		t.Errorf("PF5 on a recurring task: %v %q", a, tp.message)
	}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF1}, all, 1, &fakeTasks{}, noLog); a != taskListDetails || tp.detailsOf.CardID != all[1].CardID {
		t.Errorf("PF1 on a recurring task: %v %+v", a, tp.detailsOf)
	}
	f := &fakeTasks{}
	tp.handleList(go3270.Response{AID: go3270.AIDPF10}, all, 1, f, noLog)
	if len(f.repos) != 0 || !strings.Contains(tp.message, "order they come due") {
		t.Errorf("PF10 on a recurring task: %v %q", f.repos, tp.message)
	}

	tp = &taskPageState{view: &listView{}}
	if a := tp.handleList(go3270.Response{AID: go3270.AIDPF2}, all, 1, &fakeTasks{}, noLog); a != taskListStay || !tp.isError {
		t.Errorf("PF2 on a Trello list: %v %q", a, tp.message)
	}

	screen := buildTaskConfirm(24, 80, now, &taskPageState{confirming: all[1:]})
	if text := strings.Join(screenText(t, screen, 24, 80), "\n"); !strings.Contains(text, "marked complete now") {
		t.Errorf("archiving a recurring task:\n%s", text)
	}
}
