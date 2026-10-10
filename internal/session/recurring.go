// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/recurring"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// Recurring tasks (see package recurring) are listed on the task screen
// after the Trello tasks, every one of them, in the order they come due,
// tagged [recurring] and with when they are, or were, due; on the
// dashboard, those due, due soon or due later today, but those hidden
// until due (recurring.Task.HideUntilDue) only once due. Each is a tasks.Task
// on the task screen, its card ID recurringCard and its ID, the day it is
// due and the minute of the day, so that one done since it was shown is
// not done again, marks on it go once it is, and it is colored by when it
// is due as on the dashboard.
const (
	recurringCard = "recurring:"
	recurringTag  = "recurring"
)

// recurringCardID is r's card ID on the task screen.
func recurringCardID(r recurring.Task) string {
	return recurringCard + strconv.Itoa(r.ID) + ":" + r.Due.String() + ":" + strconv.Itoa(r.At)
}

// recurringOf is the recurring task t is, as shown: its ID, the day it
// was due (Due) and the time (At); ok is false for a Trello task.
func recurringOf(t tasks.Task) (r recurring.Task, ok bool) {
	rest, found := strings.CutPrefix(t.CardID, recurringCard)
	if !found {
		return recurring.Task{}, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return recurring.Task{}, false
	}
	var err1, err2, err3 error
	r.ID, err1 = strconv.Atoi(parts[0])
	r.Due, err2 = recurring.ParseDate(parts[1])
	r.At, err3 = strconv.Atoi(parts[2])
	if errors.Join(err1, err2, err3) != nil {
		return recurring.Task{}, false
	}
	return r, true
}

// isRecurring reports whether t is a recurring task.
func isRecurring(t tasks.Task) bool {
	_, ok := recurringOf(t)
	return ok
}

// notYetDue reports whether t is a recurring task not yet due at now.
func notYetDue(t tasks.Task, now time.Time) bool {
	r, ok := recurringOf(t)
	return ok && !r.IsDue(now)
}

// whenText is at, as seen at now: the time alone on now's day, with the
// day of the week within a week of it, else with the date.
func whenText(at, now time.Time) string {
	switch d := recurring.DateOf(at).DaysSince(recurring.DateOf(now)); {
	case d == 0:
		return at.Format("15:04")
	case d > -7 && d < 7:
		return at.Format("Mon 15:04")
	}
	return at.Format("Jan 2 15:04")
}

// dueText is when r is, or was, due, as seen at now: "due 09:00",
// "due Tue 09:00".
func dueText(r recurring.Task, now time.Time) string {
	return "due " + whenText(r.DueAt(now.Location()), now)
}

// recurringTask is r as a task on the task screen, numbered n.
func recurringTask(r recurring.Task, n int, now time.Time) tasks.Task {
	return tasks.Task{Number: n, Title: r.Title, Tags: []string{recurringTag, dueText(r, now)}, CardID: recurringCardID(r)}
}

// errNoTrello is the error of a change to a Trello task with no Trello
// linked.
var errNoTrello = errors.New("no Trello lists are linked; link them in SETTINGS")

// userTasks is the task screen's TaskSource for a user's own tasks: their
// Trello tasks (trello, nil for none), then their recurring tasks, in
// store, which are archived by marking them done.
type userTasks struct {
	trello TaskBackend
	store  *recurring.Store
	owner  int
	now    func() time.Time
}

// ownTasks is u's tasks for the task screen: their Trello tasks and their
// recurring tasks; nil for a user with neither, or none.
func (cfg Config) ownTasks(u *users.User) TaskSource {
	trello := taskBackend(cfg.tasksFor(u))
	switch {
	case cfg.Recurring != nil && u != nil:
		return userTasks{trello: trello, store: cfg.Recurring, owner: u.ID, now: func() time.Time { return time.Now().In(cfg.locationFor(u)) }}
	case trello == nil:
		return nil
	}
	return trello
}

// Snapshot is the Trello tasks, then every recurring task, numbered on
// from them. Without Trello, the recurring tasks are as fetched now; one
// failure to read them is reported as a failed fetch.
func (u userTasks) Snapshot() tasks.Snapshot {
	now := u.now()
	snap := tasks.Snapshot{Fetched: now}
	if u.trello != nil {
		snap = u.trello.Snapshot()
	}
	list, err := u.store.List(u.owner)
	if err != nil {
		snap.Err = errors.Join(snap.Err, err)
	}
	for _, r := range list {
		snap.Tasks = append(snap.Tasks, recurringTask(r, len(snap.Tasks)+1, now))
	}
	return snap
}

// Archive marks a recurring task done, now, or archives a Trello one.
func (u userTasks) Archive(ctx context.Context, t tasks.Task) error {
	if r, ok := recurringOf(t); ok {
		_, err := u.store.Done(u.owner, r.ID, r.Due, u.now())
		return err
	}
	if u.trello == nil {
		return errNoTrello
	}
	return u.trello.Archive(ctx, t)
}

func (u userTasks) Rename(ctx context.Context, t tasks.Task, title string) error {
	if r, ok := recurringOf(t); ok {
		return u.store.Rename(u.owner, r.ID, title)
	}
	if u.trello == nil {
		return errNoTrello
	}
	return u.trello.Rename(ctx, t, title)
}

func (u userTasks) Move(ctx context.Context, t tasks.Task, d tasks.Destination) error {
	switch {
	case isRecurring(t):
		return errors.New("a recurring task cannot be moved to Trello")
	case u.trello == nil:
		return errNoTrello
	}
	return u.trello.Move(ctx, t, d)
}

func (u userTasks) Reposition(ctx context.Context, t tasks.Task, pos string) error {
	switch {
	case isRecurring(t):
		return errors.New("recurring tasks are in the order they come due")
	case u.trello == nil:
		return errNoTrello
	}
	return u.trello.Reposition(ctx, t, pos)
}

func (u userTasks) Boards(ctx context.Context) ([]tasks.Board, error) {
	if u.trello == nil {
		return nil, errNoTrello
	}
	return u.trello.Boards(ctx)
}

func (u userTasks) Destinations() ([]tasks.Destination, error) {
	if u.trello == nil {
		return nil, errNoTrello
	}
	return u.trello.Destinations()
}

// recurringDashTag is where a task's tags go for a recurring task in the
// dashboard's task list.
const recurringDashTag = "[recurring]"

// recurringTaskLine is r, which at now is due, due soon or due later
// today, in the dashboard's task list: its tag, its title, in
// recurringColor, and when it came due, or comes due, in red with the
// title once it is past due.
func recurringTaskLine(r recurring.Task, now time.Time) line {
	color, intense := recurringColor(r, now)
	title, when := go3270.Field{Content: r.Title, Color: color, Intense: intense}, go3270.Field{Content: "(" + dueText(r, now) + ")", Color: go3270.Blue}
	if pastDue(r, now) {
		when.Color, when.Intense = go3270.Red, true
	}
	return line{{Content: recurringDashTag, Color: go3270.Turquoise}, title, when}
}

// recurringColor is the color of r's title at now, on the dashboard and
// the task screen alike: red once past due (see pastDue), green while due
// or due soon (see dueSoon), and blue while due later than that.
func recurringColor(r recurring.Task, now time.Time) (color go3270.Color, intense bool) {
	switch {
	case pastDue(r, now):
		return go3270.Red, true
	case r.IsDue(now) || dueSoon(r, now):
		return go3270.Green, true
	}
	return go3270.Blue, false
}

// laterToday reports whether r, not due at now, comes due later on now's
// day.
func laterToday(r recurring.Task, now time.Time) bool {
	return !r.IsDue(now) && r.Due.Equal(recurring.DateOf(now))
}

// dueSoonWithin is how near a recurring task's coming due must be for it to
// be due soon.
const dueSoonWithin = time.Hour

// dueSoon reports whether r, not due at now, comes due within
// dueSoonWithin, whether today or, near midnight, tomorrow.
func dueSoon(r recurring.Task, now time.Time) bool {
	return !r.IsDue(now) && r.DueAt(now.Location()).Sub(now) <= dueSoonWithin
}

// pastDueAfter is how long a recurring task is due, not done, before it is
// past due.
const pastDueAfter = time.Hour

// pastDue reports whether r is past due at now: due, not done, for more
// than pastDueAfter.
func pastDue(r recurring.Task, now time.Time) bool {
	return now.Sub(r.DueAt(now.Location())) > pastDueAfter
}

// The recurring task screen (PF2 on the task screen to add one, PF1 on one
// to edit it): its title, time and start date, then either the days of the
// week it is on, every so many weeks, or how many days apart it is, on a
// fixed schedule or counted from when it is done; then whether it is
// hidden from the dashboard until due.
const (
	rtTitleRow   = 3
	rtTimeRow    = 4
	rtStartRow   = 5
	rtWeeklyRow  = 7
	rtDaysRow    = 8
	rtWeeksRow   = 9
	rtDailyRow   = 11
	rtApartRow   = 12
	rtAfterRow   = 13
	rtHideRow    = 15
	rtStatusRow  = 17
	rtLabelWidth = 16 // "Days apart ===>" and its attribute byte; the fields' attribute bytes go here

	rtTitleField = "rtitle"
	rtTimeField  = "rtime"
	rtStartField = "rstart"
	rtDayField   = "rday" // and the day's place in rtDays
	rtWeeksField = "rweeks"
	rtApartField = "rapart"
	rtAfterField = "rafter"
	rtHideField  = "rhide"

	rtPrompt = "Mark days of the week with X, or type days apart; then press Enter to save."
)

// rtDays are the days of the week on the recurring task screen, in order.
var rtDays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

// recurringState is one session's recurring task screen.
type recurringState struct {
	// task is the task edited, as read; its ID is zero for a new one.
	task recurring.Task

	// loadErr is why the task to edit could not be read.
	loadErr error

	// typed is what was typed, by field, drawn again when it will not do.
	typed map[string]string

	// deleting is set once PF6 is pressed: PF4 next deletes.
	deleting bool

	// cursor is the field the message is about, where the cursor goes.
	cursor string

	message string
	isError bool
}

// startRecurring begins adding a recurring task, for id zero, or editing
// owner's task id, read from store.
func startRecurring(store *recurring.Store, owner, id int) recurringState {
	if id == 0 {
		return recurringState{}
	}
	t, err := store.Get(owner, id)
	if errors.Is(err, recurring.ErrChanged) {
		err = errors.New("it has been deleted in another session")
	}
	return recurringState{task: t, loadErr: err}
}

// value is what to draw in field name: what was typed there, or stored.
func (r *recurringState) value(name, stored string) string {
	if v, ok := r.typed[name]; ok {
		return v
	}
	return stored
}

// stored is what each field shows of the task as read, or for a new one,
// on today.
func (r *recurringState) stored(today recurring.Date) map[string]string {
	t := r.task
	out := map[string]string{rtWeeksField: "1", rtAfterField: "N", rtHideField: "N", rtStartField: today.String()}
	if t.ID == 0 {
		return out
	}
	out[rtTitleField] = t.Title
	out[rtTimeField] = fmt.Sprintf("%02d:%02d", t.At/60, t.At%60)
	out[rtStartField] = t.Start.String()
	out[rtHideField] = yesNo(t.HideUntilDue)
	switch t.Schedule {
	case recurring.Weekly:
		out[rtWeeksField] = strconv.Itoa(t.Weeks)
		for i, d := range rtDays {
			if t.Days.Has(d) {
				out[rtDayField+strconv.Itoa(i)] = "X"
			}
		}
	default:
		out[rtApartField] = strconv.Itoa(t.Interval)
		out[rtAfterField] = yesNo(t.Schedule == recurring.After)
	}
	return out
}

// buildRecurring renders the recurring task screen at now, and where the
// cursor goes: the title, or the field the message is about.
func buildRecurring(rows, cols int, now time.Time, r *recurringState) (screen go3270.Screen, cursorRow, cursorCol int) {
	title := "NEW RECURRING TASK"
	if r.task.ID != 0 || r.loadErr != nil {
		title = "RECURRING TASK"
	}
	screen = titleFields(cols, title, now)
	if r.loadErr != nil {
		screen = append(screen, placeLine(2, cols, line{{Content: "Could not read the task: " + r.loadErr.Error(), Color: go3270.Red, Intense: true}})...)
		return appendMessageRows(screen, rows, cols, "", false, "PF3 goes back to the tasks.", "PF3=Back"), rows - 1, 0
	}
	screen = append(screen, placeLine(1, cols, line{{
		Content: "On the dashboard from when it comes due until completed on the task screen.", Color: go3270.Blue,
	}})...)

	stored := r.stored(recurring.DateOf(now))
	at := map[string][2]int{} // each field's row and first column
	input := func(row, col int, name string, width int) {
		at[name] = [2]int{row, col + 1}
		screen = append(screen, go3270.Field{
			Row: row, Col: col, Write: true, Name: name, Content: cutRunes(r.value(name, stored[name]), width),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		})
	}
	// labeled puts label, an input field width wide after it, and hint
	// after that, which ends the field.
	labeled := func(row int, label, name string, width int, hint string) {
		screen = append(screen, go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: label})
		input(row, rtLabelWidth, name, width)
		screen = append(screen, placeLineAt(row, rtLabelWidth+1+width, cols, line{{Content: hint, Color: go3270.Blue, Autoskip: true}})...)
	}
	heading := func(row int, text string) {
		screen = append(screen, placeLine(row, cols, line{{Content: text, Color: go3270.Turquoise, Intense: true}})...)
	}

	screen = append(screen, go3270.Field{Row: rtTitleRow, Col: 0, Color: go3270.Turquoise, Content: "Title      ===>"})
	input(rtTitleRow, rtLabelWidth, rtTitleField, cols-rtLabelWidth-2)
	screen = append(screen, go3270.Field{Row: rtTitleRow, Col: cols - 1}) // ends the title field
	labeled(rtTimeRow, "Time       ===>", rtTimeField, 5, "24-hour, hh:mm")
	labeled(rtStartRow, "Starts     ===>", rtStartField, 10, "yyyy-mm-dd: today or later")

	heading(rtWeeklyRow, "On these days of the week (X each):")
	for i, d := range rtDays {
		col := 2 + 6*i
		input(rtDaysRow, col, rtDayField+strconv.Itoa(i), 1)
		screen = append(screen, go3270.Field{Row: rtDaysRow, Col: col + 2, Color: go3270.Green, Autoskip: true, Content: d.String()[:3]})
	}
	labeled(rtWeeksRow, "Every      ===>", rtWeeksField, 2, fmt.Sprintf("weeks: 1 for every week, up to %d", recurring.MaxWeeks))

	heading(rtDailyRow, "Or every so many days:")
	labeled(rtApartRow, "Days apart ===>", rtApartField, 3, fmt.Sprintf("1 to %d", recurring.MaxInterval))
	labeled(rtAfterRow, "After done ===>", rtAfterField, 1, "Y: that many days after it is done, at the time done")

	labeled(rtHideRow, "Hide       ===>", rtHideField, 1, "Y: not on the dashboard until it is due")

	help := "PF3=Cancel Enter=Save"
	if r.task.ID != 0 {
		status := line{{Content: "Due " + r.task.DueAt(now.Location()).Format("Mon Jan 2 15:04"), Color: go3270.White}}
		if r.task.IsDue(now) {
			status = line{{Content: "Due now, since " + whenText(r.task.DueAt(now.Location()), now), Color: go3270.Yellow, Intense: true}}
		}
		screen = append(screen, placeLine(rtStatusRow, cols, append(status, go3270.Field{
			Content: "(complete it on the task screen)", Color: go3270.Blue,
		}))...)
		help += " PF6=Delete"
	}
	screen = appendMessageRows(screen, rows, cols, r.message, r.isError, rtPrompt, help)

	if p, ok := at[r.cursor]; ok {
		return screen, p[0], p[1]
	}
	return screen, rtTitleRow, rtLabelWidth + 1
}

// parseTimeOfDay reads a 24-hour time of day, hh:mm or hhmm, as minutes
// after midnight.
func parseTimeOfDay(s string) (int, error) {
	s = strings.Join(strings.Fields(s), "")
	m := untilClock.FindStringSubmatch(s)
	if m == nil || m[3] != "" {
		return 0, fmt.Errorf("type the time as a 24-hour time, hh:mm, not %q", s)
	}
	hText, mText := m[1], m[2]
	if hText == "" {
		hText, mText = m[4], m[5]
	}
	h, _ := strconv.Atoi(hText)
	mins, _ := strconv.Atoi(mText)
	if h > 23 || mins > 59 {
		return 0, fmt.Errorf("%q is not a time of day: hours go up to 23, minutes to 59", s)
	}
	return h*60 + mins, nil
}

// parse reads the task typed, at now, or says what is wrong with it and
// which field that is in.
func (r *recurringState) parse(values map[string]string, now time.Time) (t recurring.Task, field, bad string) {
	t = r.task
	today := recurring.DateOf(now)
	t.Title = strings.Join(strings.Fields(values[rtTitleField]), " ")
	if t.Title == "" {
		return t, rtTitleField, "Type the task's title."
	}

	var err error
	if t.At, err = parseTimeOfDay(values[rtTimeField]); err != nil {
		if strings.TrimSpace(values[rtTimeField]) == "" {
			return t, rtTimeField, "Type the time of day it comes due, hh:mm."
		}
		return t, rtTimeField, upperFirst(err.Error()) + "."
	}

	start := strings.TrimSpace(values[rtStartField])
	if start == "" {
		t.Start = today
	} else if t.Start, err = recurring.ParseDate(start); err != nil {
		return t, rtStartField, upperFirst(err.Error()) + "."
	}
	// A task kept starting when it did need not start again; any other
	// start is today or later.
	if t.Start.Before(today) && !(r.task.ID != 0 && t.Start.Equal(r.task.Start)) {
		return t, rtStartField, "The start date cannot be before today, " + today.String() + "."
	}

	t.Days = 0
	for i, d := range rtDays {
		switch v := strings.ToUpper(strings.TrimSpace(values[rtDayField+strconv.Itoa(i)])); v {
		case "":
		case "X":
			t.Days = t.Days.With(d)
		default:
			return t, rtDayField + strconv.Itoa(i), fmt.Sprintf("Type X to mark %s, not %q.", d, v)
		}
	}
	apart := strings.TrimSpace(values[rtApartField])
	after := strings.ToUpper(strings.TrimSpace(values[rtAfterField]))
	if after != "" && after != "Y" && after != "N" {
		return t, rtAfterField, fmt.Sprintf("Type Y or N for after done, not %q.", after)
	}

	switch {
	case t.Days != 0 && apart != "":
		return t, rtApartField, "Mark days of the week, or type days apart, not both."
	case t.Days != 0 && after == "Y":
		return t, rtAfterField, "After done goes with days apart, not days of the week."
	case t.Days != 0:
		t.Schedule, t.Interval = recurring.Weekly, 0
		weeks := strings.TrimSpace(values[rtWeeksField])
		if weeks == "" {
			weeks = "1"
		}
		if t.Weeks, err = strconv.Atoi(weeks); err != nil || t.Weeks < 1 || t.Weeks > recurring.MaxWeeks {
			return t, rtWeeksField, fmt.Sprintf("Type how many weeks apart, 1 to %d.", recurring.MaxWeeks)
		}
	case apart == "":
		return t, rtDayField + "0", "Mark the days of the week it is on, or type how many days apart."
	default:
		t.Schedule, t.Days, t.Weeks = recurring.Every, 0, 1
		if after == "Y" {
			t.Schedule = recurring.After
		}
		if t.Interval, err = strconv.Atoi(apart); err != nil || t.Interval < 1 || t.Interval > recurring.MaxInterval {
			return t, rtApartField, fmt.Sprintf("Type how many days apart, 1 to %d.", recurring.MaxInterval)
		}
	}

	switch hide := strings.ToUpper(strings.TrimSpace(values[rtHideField])); hide {
	case "", "N":
		t.HideUntilDue = false
	case "Y":
		t.HideUntilDue = true
	default:
		return t, rtHideField, fmt.Sprintf("Type Y or N for hide, not %q.", hide)
	}
	return t, "", ""
}

// upperFirst is s with its first letter capitalized.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// handle acts on a key on the recurring task screen for owner, at now,
// returning whether to go back to the task screen, and what to say there.
// PF3 goes back; Enter saves what was typed into store; PF6, confirmed by
// PF4 next, deletes the task edited.
func (r *recurringState) handle(resp go3270.Response, store *recurring.Store, owner int, now time.Time, logf func(string, ...any)) (back bool, said string, isError bool) {
	r.message, r.isError, r.cursor = "", false, ""
	deleting := r.deleting
	r.deleting = false
	if resp.AID == go3270.AIDPF3 || r.loadErr != nil {
		return resp.AID == go3270.AIDPF3, "", false
	}
	r.typed = map[string]string{}
	for name, v := range resp.Values {
		r.typed[name] = v
	}

	switch resp.AID {
	case go3270.AIDEnter:
	case go3270.AIDPF6:
		if r.task.ID == 0 {
			r.message, r.isError = "There is nothing saved to delete; PF3 cancels.", true
			return false, "", false
		}
		r.deleting = true
		r.message, r.isError = fmt.Sprintf("Press PF4 to delete %q for good; any other key keeps it.", r.task.Title), true
		return false, "", false
	case go3270.AIDPF4:
		if !deleting {
			return false, "", false
		}
		if err := store.Remove(owner, r.task.ID); err != nil {
			r.message, r.isError = "Could not delete: "+err.Error()+".", true
			return false, "", false
		}
		logf("deleted recurring task %d %q", r.task.ID, r.task.Title)
		return true, fmt.Sprintf("Deleted the recurring task %q.", r.task.Title), false
	default:
		return false, "", false
	}

	t, field, bad := r.parse(resp.Values, now)
	if bad != "" {
		r.message, r.isError, r.cursor = bad, true, field
		return false, "", false
	}
	t.Owner = owner
	var err error
	verb := "Added"
	if t.ID == 0 {
		t, err = store.Add(t, recurring.DateOf(now))
	} else {
		verb = "Saved"
		t, err = store.Update(t, recurring.DateOf(now))
	}
	if err != nil {
		r.message, r.isError = "Could not save: "+err.Error()+".", true
		return false, "", false
	}
	logf("%s recurring task %d %q", strings.ToLower(verb), t.ID, t.Title)
	when := "due " + whenText(t.DueAt(now.Location()), now)
	if t.IsDue(now) {
		when = "due now"
	}
	return true, fmt.Sprintf("%s the recurring task %q, %s.", verb, t.Title, when), false
}
