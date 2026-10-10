// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/bigtext"
	"github.com/jmaslak/go-adhd-dash/internal/timers"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestParseTimer(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1:30":      90 * time.Minute,
		"0:05":      5 * time.Minute,
		"12:00":     12 * time.Hour,
		"99:59":     99*time.Hour + 59*time.Minute,
		"2h3m20s":   2*time.Hour + 3*time.Minute + 20*time.Second,
		"2H 3M 20S": 2*time.Hour + 3*time.Minute + 20*time.Second,
		"45m":       45 * time.Minute,
		"90s":       90 * time.Second,
		"1h30m":     90 * time.Minute,
		"1h20s":     time.Hour + 20*time.Second,
		"  10m  ":   10 * time.Minute,
		"99h59m59s": timerMax,
	} {
		if got, err := parseTimer(in); err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"":                      "type how long",
		"5":                     "not hh:mm",
		"1:3":                   "not hh:mm",
		"1:60":                  "minutes go up to 59",
		"0:00":                  "more than no time",
		"0s":                    "more than no time",
		"3m2h":                  "not hh:mm",
		"1.5h":                  "not hh:mm",
		"100h":                  "longer than 99:59:59",
		"99h60m":                "longer than 99:59:59",
		"6000m":                 "longer than 99:59:59",
		"99999999999999999999h": "longer than 99:59:59",
		"-5m":                   "not hh:mm",
		"5ms":                   "not hh:mm",
	} {
		if _, err := parseTimer(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v; want %q", in, err, want)
		}
	}
}

func TestTimerCountdown(t *testing.T) {
	tm := &timerState{}
	start := time.Date(2026, 10, 3, 9, 0, 0, 300*int(time.Millisecond), time.Local)
	key := func(aid go3270.AID, at time.Time, typed string) bool {
		back, _ := tm.handle(go3270.Response{AID: aid, Values: map[string]string{timerField: typed}}, at, nil, 0, noLog)
		return back
	}

	// Asking.
	s, crow, ccol := buildTimer(24, 80, start, tm, users.Preferences{})
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"TIMER", "How long ===>", "1:30 is an hour and a half", "2h3m20s", timerPrompt, "PF3=Back Enter=Start"} {
		if !strings.Contains(text, want) {
			t.Errorf("asking screen lacks %q:\n%s", want, text)
		}
	}
	if crow != timerRow || ccol != len(timerLabel)+2 {
		t.Errorf("cursor at %d,%d; want the field", crow, ccol)
	}
	if key(go3270.AIDEnter, start, "soon") || !tm.isError || tm.step != timerAsk {
		t.Errorf("bad time: step %v, %q", tm.step, tm.message)
	}
	s, _, _ = buildTimer(24, 80, start, tm, users.Preferences{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, `"soon" is not hh:mm`) || !strings.Contains(text, "soon") {
		t.Errorf("refused, not said, or not typed again:\n%s", text)
	}

	// Running: 1m05s from 09:00:00.3; the digits count down in whole
	// seconds, rounded up.
	if key(go3270.AIDEnter, start, "1m5s") || tm.step != timerRunning {
		t.Fatalf("start: step %v, %q", tm.step, tm.message)
	}
	s, crow, _ = buildTimer(24, 80, start, tm, users.Preferences{})
	rows := screenText(t, s, 24, 80)
	text = strings.Join(rows, "\n")
	if !strings.Contains(text, "Ends at 09:01:05") || !strings.Contains(text, "Ends at 09:01:05 for 01:05") || !strings.Contains(text, "PF3=Back, leaving it running PF6=Stop the timer") || crow != 23 {
		t.Errorf("running screen:\n%s", text)
	}
	if want := bigLine(t, "01:05", 0); !strings.Contains(text, want) {
		t.Errorf("digits do not read 01:05 (top row %q):\n%s", want, text)
	}
	for _, f := range s {
		if f.Write {
			t.Errorf("an input field while running: %+v", f)
		}
		if strings.Contains(f.Content, "#") && f.Color != go3270.Green {
			t.Errorf("digits with over a minute left in %v", f.Color)
		}
	}
	at := start.Add(55*time.Second + 500*time.Millisecond) // 9.8s left: 10
	if got := clockText(tm.remaining(at), tm.total); got != "00:10" {
		t.Errorf("9.8s left shows %s", got)
	}
	s, _, _ = buildTimer(24, 80, at, tm, users.Preferences{})
	for _, f := range s {
		if strings.Contains(f.Content, "#") && f.Color != go3270.Red {
			t.Errorf("digits with ten seconds left in %v", f.Color)
		}
	}
	// Redrawn each second, and as it runs out.
	if d := tm.deadline(at); !d.Equal(nextRedraw(at, time.Second)) {
		t.Errorf("deadline %v", d)
	}
	almost := tm.end.Add(-300 * time.Millisecond)
	if d := tm.deadline(almost); !d.Equal(tm.end.Add(5 * time.Millisecond)) {
		t.Errorf("deadline near the end %v, end %v", d, tm.end)
	}
	if key(go3270.AIDEnter, at, "") || key(go3270.AIDPF8, at, "") {
		t.Errorf("a key other than PF3 or PF6 left the timer")
	}

	// Done: DONE, flashing, until Enter.
	done := tm.end.Add(time.Second)
	for _, sec := range []int64{0, 1} {
		when := time.Unix(done.Unix()+sec, 0)
		s, _, _ = buildTimer(24, 80, when, tm, users.Preferences{})
		if tm.step != timerDone {
			t.Fatalf("not done at the end")
		}
		reverse := 0
		for _, f := range s {
			if f.Highlighting == go3270.ReverseVideo {
				reverse++
			}
		}
		text := strings.Join(screenText(t, s, 24, 80), "\n")
		if on := when.Unix()%2 == 0; (reverse == 22) != on || (reverse == 0) == on || !strings.Contains(text, bigLine(t, "DONE", 0)) || !strings.Contains(text, "Timer: time is up. Enter=Dismiss PF3=Back") {
			t.Errorf("at %v: %d rows reversed, flash %v:\n%s", when, reverse, on, text)
		}
	}
	if key(go3270.AIDPF8, done, "") || key(go3270.AIDPA1, done, "") {
		t.Errorf("a key other than Enter, PF3 or PF6 left DONE")
	}
	if !key(go3270.AIDPF6, done, "") {
		t.Errorf("PF6 on DONE did not go back")
	}
	if !key(go3270.AIDEnter, done, "") {
		t.Errorf("Enter on DONE did not go back")
	}
	if !key(go3270.AIDPF3, done, "") {
		t.Errorf("PF3 on DONE did not go back")
	}

	// Stopped with PF6, and left with PF3 when asking.
	tm = &timerState{}
	key(go3270.AIDEnter, start, "1:00")
	if !key(go3270.AIDPF6, start.Add(30*time.Second), "") {
		t.Errorf("PF6 did not stop the timer")
	}
	if back, _ := (&timerState{}).handle(go3270.Response{AID: go3270.AIDPF3}, start, nil, 0, noLog); !back {
		t.Errorf("PF3 did not leave the asking screen")
	}
}

func TestTimerHours(t *testing.T) {
	tm := &timerState{}
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	tm.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{timerField: "99h59m59s"}}, start, nil, 0, noLog)
	// The widest the digits get, on the narrowest screen.
	s, _, _ := buildTimer(24, 80, start, tm, users.Preferences{})
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	if !strings.Contains(text, bigLine(t, "99:59:59", 0)) {
		t.Errorf("99:59:59 not shown:\n%s", text)
	}
	if got := clockText(tm.remaining(start.Add(time.Hour)), tm.total); got != "98:59:59" {
		t.Errorf("an hour in: %s", got)
	}
	if got := clockText(5*time.Second, time.Hour); got != "0:00:05" {
		t.Errorf("5s of an hour: %s", got)
	}
}

// bigLine is row i of text in big characters, as the timer draws it,
// without the spaces after it.
func bigLine(t *testing.T, text string, i int) string {
	t.Helper()
	if !bigtext.Supports(text) {
		t.Fatalf("no big %q", text)
	}
	return strings.TrimRight(bigtext.Rows(text)[i], " ")
}

// TestTimerShared checks that a user's sessions show the one timer kept:
// started in one, it shows in the other, and stopped or dismissed in one,
// it goes from the other; another user's is their own.
func TestTimerShared(t *testing.T) {
	store, err := timers.New(openDB(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	a, b, other := &timerState{}, &timerState{}, &timerState{}
	key := func(tm *timerState, owner int, aid go3270.AID, at time.Time, typed string) (bool, string) {
		return tm.handle(go3270.Response{AID: aid, Values: map[string]string{timerField: typed}}, at, store, owner, noLog)
	}

	if a.sync(store, 1, start, noLog) || b.sync(store, 1, start, noLog) || a.step != timerAsk {
		t.Fatalf("a timer before any started")
	}
	b.typed = "half typed"
	if back, _ := key(a, 1, go3270.AIDEnter, start, "5m"); back || a.step != timerRunning {
		t.Fatalf("not started: %q", a.message)
	}
	if !b.sync(store, 1, start.Add(time.Second), noLog) || b.step != timerRunning || !b.end.Equal(a.end) || b.total != 5*time.Minute || b.typed != "" {
		t.Fatalf("the other session shows %+v", b)
	}
	if b.sync(store, 1, start.Add(2*time.Second), noLog) {
		t.Errorf("unchanged, yet changed")
	}
	if other.sync(store, 2, start, noLog) || other.step != timerAsk {
		t.Errorf("another user sees the timer")
	}

	// Left with PF3 in one, it runs on, there and in the other.
	if back, said := key(b, 1, go3270.AIDPF3, start.Add(time.Minute), ""); !back || said != "" {
		t.Fatalf("not left: %v, %q", back, said)
	}
	if _, ok, _ := store.Get(1); !ok {
		t.Fatalf("leaving with PF3 stopped the timer")
	}
	if a.sync(store, 1, start.Add(time.Minute), noLog) || a.step != timerRunning {
		t.Fatalf("leaving in one changed the other: %v", a.step)
	}
	b = &timerState{}
	if !b.sync(store, 1, start.Add(time.Minute), noLog) || b.step != timerRunning {
		t.Fatalf("opened again, it is not running: %v", b.step)
	}

	// Stopped with PF6 in one, it goes from the other, which says so.
	if back, said := key(b, 1, go3270.AIDPF6, start.Add(time.Minute), ""); !back || said != "" {
		t.Fatalf("not stopped: %v, %q", back, said)
	}
	if !a.sync(store, 1, start.Add(time.Minute), noLog) || a.step != timerAsk || a.message != timerStoppedElsewhere {
		t.Fatalf("still shown after stopping: %+v", a)
	}

	// Run out, it is done in both, until dismissed in one.
	key(a, 1, go3270.AIDEnter, start, "1m")
	done := start.Add(2 * time.Minute)
	if !a.sync(store, 1, done, noLog) || !b.sync(store, 1, done, noLog) || a.step != timerDone || b.step != timerDone {
		t.Fatalf("not done in both: %v, %v", a.step, b.step)
	}
	// PF3 leaves it done.
	if back, _ := key(a, 1, go3270.AIDPF3, done, ""); !back {
		t.Fatalf("PF3 did not go back from DONE")
	}
	if _, ok, _ := store.Get(1); !ok {
		t.Fatalf("PF3 dismissed DONE")
	}
	a.sync(store, 1, done, noLog)
	if back, _ := key(a, 1, go3270.AIDEnter, done, ""); !back {
		t.Fatalf("not dismissed")
	}
	if !b.sync(store, 1, done, noLog) || b.step != timerAsk {
		t.Errorf("still done after dismissing: %v", b.step)
	}

	// A session stopping a timer replaced since in another leaves the new
	// one alone.
	key(a, 1, go3270.AIDEnter, start, "1m")
	b.sync(store, 1, start, noLog)
	key(&timerState{}, 1, go3270.AIDEnter, start.Add(time.Second), "10m")
	key(b, 1, go3270.AIDPF6, start.Add(2*time.Second), "")
	if kept, ok, _ := store.Get(1); !ok || kept.Total != 10*time.Minute {
		t.Errorf("the new timer was stopped: %+v, %v", kept, ok)
	}
}

// TestTimerNote checks what a timer is for: typed, it shows as the timer
// runs and once done, in every session of the user; left blank, "Timer";
// typed too long, cut, spaces run together.
func TestTimerNote(t *testing.T) {
	store, err := timers.New(openDB(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	key := func(tm *timerState, aid go3270.AID, values map[string]string) {
		t.Helper()
		if back, _ := tm.handle(go3270.Response{AID: aid, Values: values}, start, store, 1, noLog); back {
			t.Fatalf("went back")
		}
	}

	// Asked for, under the rest.
	a := &timerState{}
	s, _, _ := buildTimer(24, 80, start, a, users.Preferences{})
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"What for ===>", "laundry, call Sam back", `Left blank, it is "Timer".`} {
		if !strings.Contains(text, want) {
			t.Errorf("asking screen lacks %q:\n%s", want, text)
		}
	}

	// Typed again with a time that will not do.
	key(a, go3270.AIDEnter, map[string]string{timerField: "soon", timerNoteField: "Laundry"})
	s, _, _ = buildTimer(24, 80, start, a, users.Preferences{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Laundry") {
		t.Errorf("the note is not typed again:\n%s", text)
	}

	key(a, go3270.AIDEnter, map[string]string{timerField: "5m", timerNoteField: "  Move   the\tlaundry  "})
	b := &timerState{}
	b.sync(store, 1, start, noLog)
	for _, tm := range []*timerState{a, b} {
		s, _, _ := buildTimer(24, 80, start, tm, users.Preferences{})
		if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Move the laundry: Ends at 09:05:00") {
			t.Errorf("running, the note is not shown:\n%s", text)
		}
		s, _, _ = buildTimer(24, 80, start.Add(time.Hour), tm, users.Preferences{})
		if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Move the laundry: time is up. Enter=Dismiss PF3=Back") {
			t.Errorf("done, the note is not shown:\n%s", text)
		}
	}

	for typed, want := range map[string]string{
		"":                       "Timer",
		"   ":                    "Timer",
		strings.Repeat("x", 50):  strings.Repeat("x", timerNoteWidth),
		"Call Sam back, re: tax": "Call Sam back, re: tax",
	} {
		key(&timerState{}, go3270.AIDEnter, map[string]string{timerUntilField: "17:00", timerNoteField: typed})
		if kept, _, _ := store.Get(1); kept.Note != want {
			t.Errorf("%q kept as %q; want %q", typed, kept.Note, want)
		}
	}
	key(&timerState{}, go3270.AIDEnter, map[string]string{timerUntilField: "17:00", timerNoteField: "Call Sam back"})
	b.sync(store, 1, start, noLog)
	s, _, _ = buildTimer(24, 80, start, b, users.Preferences{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Call Sam back: Until 17:00:00") {
		t.Errorf("until, the note is not shown:\n%s", text)
	}
}

// TestDashboardTimer checks the dashboard's line about the user's timer:
// none without one; with one, what it is for and how long it has left,
// then that its time is up; and only the user's own.
func TestDashboardTimer(t *testing.T) {
	store, err := timers.New(openDB(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	cfg := Config{Timers: store}
	// row is the timer's line, checking that it has a blank row above and
	// below, the tasks after; or with none, that the tasks are where they
	// always are, and it is "".
	row := func(at time.Time, owner int) string {
		t.Helper()
		s, _, _, _ := buildDashboard(24, 80, gather(cfg, at, nil, nil, owner, nil), 0, "")
		rows := screenText(t, s, 24, 80)
		got := strings.TrimSpace(rows[firstBodyRow])
		if !strings.HasPrefix(got, "Timer:") {
			if !strings.HasPrefix(got, "TASKS") {
				t.Errorf("with no timer, row %d is %q; want the tasks", firstBodyRow, got)
			}
			return ""
		}
		if above, below := strings.TrimSpace(rows[firstBodyRow-1]), strings.TrimSpace(rows[firstBodyRow+1]); above != "" || below != "" {
			t.Errorf("not blank around the timer: %q above, %q below", above, below)
		}
		if tasks := strings.TrimSpace(rows[firstBodyRow+2]); !strings.HasPrefix(tasks, "TASKS") {
			t.Errorf("after the timer, %q; want the tasks", tasks)
		}
		return got
	}

	if got := row(start, 1); got != "" {
		t.Errorf("with no timer: %q", got)
	}
	if err := store.Start(1, timers.Timer{Total: 5 * time.Minute, End: start.Add(5 * time.Minute), Note: "Laundry"}); err != nil {
		t.Fatal(err)
	}
	for at, want := range map[time.Time]string{
		start: "Timer: Laundry, 05:00 left, ends 09:05:00",
		start.Add(4*time.Minute + 30*time.Second): "Timer: Laundry, 00:30 left, ends 09:05:00",
		start.Add(5 * time.Minute):                "Timer: Laundry, time is up. Type TIMER to dismiss it.",
	} {
		if got := row(at, 1); got != want {
			t.Errorf("at %s: %q; want %q", at.Format("15:04:05"), got, want)
		}
	}
	if got := row(start, 2); got != "" {
		t.Errorf("another user sees the timer: %q", got)
	}
	if got := row(start, 0); got != "" {
		t.Errorf("no one sees the timer: %q", got)
	}

	// Noteless, to tomorrow, an hour or more away.
	evening := start.Add(13 * time.Hour) // 22:00
	if err := store.Start(1, timers.Timer{Until: true, Total: 10 * time.Hour, End: evening.Add(10 * time.Hour), Note: timerNoteless}); err != nil {
		t.Fatal(err)
	}
	if got, want := row(evening, 1), "Timer: 10:00:00 left, ends 08:00:00 tomorrow"; got != want {
		t.Errorf("%q; want %q", got, want)
	}
}

// TestDashboardTimerFits checks that the timer's line on the dashboard is
// never wider than the screen, the note, as long as it can be, cut to the
// room the rest leaves, so that the rest is always whole.
func TestDashboardTimerFits(t *testing.T) {
	start := time.Date(2026, 10, 3, 22, 0, 0, 0, time.Local)
	note := strings.Repeat("q", timerNoteWidth) // a letter in nothing else on the line
	long := timers.Timer{Until: true, Total: 10 * time.Hour, End: start.Add(10 * time.Hour), Note: note}
	for _, c := range []struct {
		name string
		at   time.Time
		rest string
	}{
		{"running", start, "10:00:00 left, ends 08:00:00 tomorrow"},
		{"done", long.End, "time is up. Type TIMER to dismiss it."},
	} {
		// From as narrow as the rest fits with no note (45), to the widest
		// screen, 132; a 3270 is at least 80.
		for cols := 45; cols <= 132; cols++ {
			l := timerStatus(view{Now: c.at, HasTimer: true, Timer: long}, cols)
			if w := lineWidth(l); w > cols {
				t.Errorf("%s, %d columns: %d wide", c.name, cols, w)
			}
			// Placed, nothing is cut.
			text := strings.TrimSpace(screenText(t, placeLine(0, cols, l), 1, cols)[0])
			if !strings.HasPrefix(text, "Timer:") || !strings.HasSuffix(text, c.rest) {
				t.Errorf("%s, %d columns: %q", c.name, cols, text)
				continue
			}
			// The note is as long as fits, and cut only when it must be.
			shown := strings.Count(text, "q")
			room := cols - len("Timer: ") - len(" "+c.rest) - len(" ,") // the attribute before "Timer:" is in TrimSpace
			if want := min(max(room, 0), timerNoteWidth); shown != want {
				t.Errorf("%s, %d columns: note %d long; want %d: %q", c.name, cols, shown, want, text)
			}
		}
	}

	// At 80 columns, the full note fits beside a timer of less than an
	// hour ending today.
	short := timers.Timer{Total: 5 * time.Minute, End: start.Add(5 * time.Minute), Note: note}
	s, _, _, _ := buildDashboard(24, 80, view{Now: start, HasTimer: true, Timer: short}, 0, "")
	if got, want := strings.TrimSpace(screenText(t, s, 24, 80)[firstBodyRow]), "Timer: "+note+", 05:00 left, ends 22:05:00"; got != want {
		t.Errorf("%q; want %q", got, want)
	}
}

// TestDashboardTimerFlash checks that the dashboard's line about a timer
// whose time is up is red, the whole row reversed every other second; for
// a user who avoids flashing, steadily red; and while it runs, never
// reversed. It checks too when the dashboard is redrawn for it.
func TestDashboardTimerFlash(t *testing.T) {
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	tm := timers.Timer{Total: time.Minute, End: start.Add(time.Minute), Note: "Laundry"}
	fields := func(v view) []go3270.Field {
		t.Helper()
		s, _, _, _ := buildDashboard(24, 80, v, 0, "")
		var out []go3270.Field
		for _, f := range s {
			if f.Row == firstBodyRow {
				out = append(out, f)
			}
		}
		return out
	}

	// Running: as before, nothing reversed.
	for _, f := range fields(view{Now: start, HasTimer: true, Timer: tm}) {
		if f.Highlighting == go3270.ReverseVideo {
			t.Errorf("running, reversed: %+v", f)
		}
	}

	for _, noFlash := range []bool{false, true} {
		for sec := range int64(4) {
			at := time.Unix(tm.End.Unix()+sec, 0)
			fs := fields(view{Now: at, HasTimer: true, Timer: tm, NoFlash: noFlash})
			if len(fs) != 1 || fs[0].Color != go3270.Red || !strings.HasPrefix(fs[0].Content, "Timer: Laundry, time is up.") {
				t.Fatalf("done at %d, no flash %v: %+v", sec, noFlash, fs)
			}
			reversed := fs[0].Highlighting == go3270.ReverseVideo
			if want := at.Unix()%2 == 0 && !noFlash; reversed != want {
				t.Errorf("done at %d, no flash %v: reversed %v", sec, noFlash, reversed)
			}
			if reversed && utf8.RuneCountInString(fs[0].Content) != 79 {
				t.Errorf("reversed, not the whole row: %q", fs[0].Content)
			}
			// The row below starts a plain field of its own, so that the
			// reversed one ends with its row.
			s, _, _, _ := buildDashboard(24, 80, view{Now: at, HasTimer: true, Timer: tm, NoFlash: noFlash}, 0, "")
			if i := slices.IndexFunc(s, func(f go3270.Field) bool { return f.Row == firstBodyRow+1 && f.Col == 0 }); i < 0 || s[i].Highlighting != go3270.DefaultHighlight || s[i].Content != "" {
				t.Errorf("no plain field starting the row below the timer: %v", i)
			}
		}
	}

	// Redrawn as it runs out; then each second while it flashes, and not
	// for it once done with no flashing; nor with no timer.
	for _, c := range []struct {
		v    view
		want time.Time
	}{
		{view{Now: start, HasTimer: true, Timer: tm}, tm.End.Add(5 * time.Millisecond)},
		{view{Now: tm.End.Add(300 * time.Millisecond), HasTimer: true, Timer: tm}, nextRedraw(tm.End.Add(300*time.Millisecond), time.Second)},
		{view{Now: tm.End, HasTimer: true, Timer: tm, NoFlash: true}, time.Time{}},
		{view{Now: start}, time.Time{}},
	} {
		if got := timerRedraw(c.v); !got.Equal(c.want) {
			t.Errorf("at %v, no flash %v: redrawn at %v; want %v", c.v.Now.Format("15:04:05.000"), c.v.NoFlash, got, c.want)
		}
	}
}

// TestEndText checks how a timer's end is told: the time today, "tomorrow"
// after it the next day, and the day of the week before it later; and that
// the timer screen and the dashboard tell it so.
func TestEndTextInNowsZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("no zone files:", err)
	}
	// 14:00 UTC is 23:00 in Tokyo; a timer ending at 15:30 UTC ends
	// tomorrow there.
	now := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC).In(tokyo)
	end := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	if got := endText(end, now); got != "00:30:00 tomorrow" {
		t.Errorf("endText = %q", got)
	}
	if got := endsAt(end.Add(48*time.Hour), now); got != "Ends Tue at 00:30:00" {
		t.Errorf("endsAt = %q", got)
	}
}

func TestEndText(t *testing.T) {
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.Local) // a Saturday
	for end, want := range map[time.Time]string{
		now.Add(time.Hour):     "23:00:00",
		now.Add(2 * time.Hour): "00:00:00 tomorrow",
		now.Add(25*time.Hour + 59*time.Minute + 59*time.Second): "23:59:59 tomorrow",
		now.Add(26 * time.Hour):                                 "Mon 00:00:00",
		now.Add(timerMax):                                       "Thu 01:59:59",
	} {
		if got := endText(end, now); got != want {
			t.Errorf("%v: %q; want %q", end, got, want)
		}
	}

	for end, want := range map[time.Time]string{
		now.Add(time.Hour):      "Ends at 23:00:00",
		now.Add(2 * time.Hour):  "Ends at 00:00:00 tomorrow",
		now.Add(26 * time.Hour): "Ends Mon at 00:00:00",
	} {
		if got := endsAt(end, now); got != want {
			t.Errorf("%v: %q; want %q", end, got, want)
		}
	}

	long := timers.Timer{Total: 99 * time.Hour, End: now.Add(99 * time.Hour), Note: "Soak"}
	s, _, _, _ := buildDashboard(24, 80, view{Now: now, HasTimer: true, Timer: long}, 0, "")
	if got, want := strings.TrimSpace(screenText(t, s, 24, 80)[firstBodyRow]), "Timer: Soak, 99:00:00 left, ends Thu 01:00:00"; got != want {
		t.Errorf("dashboard: %q; want %q", got, want)
	}
	tm := &timerState{step: timerRunning, total: long.Total, end: long.End, note: long.Note}
	s, _, _ = buildTimer(24, 80, now, tm, users.Preferences{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "Soak: Ends Thu at 01:00:00 for 99:00:00") {
		t.Errorf("timer screen:\n%s", text)
	}
}
