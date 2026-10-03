package session

import (
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/bigtext"
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
		return tm.handle(go3270.Response{AID: aid, Values: map[string]string{timerField: typed}}, at, noLog)
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
	if !strings.Contains(text, "Ends at 09:01:05") || !strings.Contains(text, "of 01:05") || !strings.Contains(text, "PF3=Stop the timer") || crow != 23 {
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
		t.Errorf("a key other than PF3 stopped the timer")
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
		if on := when.Unix()%2 == 0; (reverse == 22) != on || (reverse == 0) == on || !strings.Contains(text, bigLine(t, "DONE", 0)) || !strings.Contains(text, "Time is up. Press Enter.") {
			t.Errorf("at %v: %d rows reversed, flash %v:\n%s", when, reverse, on, text)
		}
	}
	if key(go3270.AIDPF8, done, "") || key(go3270.AIDPA1, done, "") {
		t.Errorf("a key other than Enter or PF3 left DONE")
	}
	if !key(go3270.AIDEnter, done, "") {
		t.Errorf("Enter on DONE did not go back")
	}
	if !key(go3270.AIDPF3, done, "") {
		t.Errorf("PF3 on DONE did not go back")
	}

	// Stopped with PF3, and left with PF3 when asking.
	tm = &timerState{}
	key(go3270.AIDEnter, start, "1:00")
	if !key(go3270.AIDPF3, start.Add(time.Minute), "") {
		t.Errorf("PF3 did not stop the timer")
	}
	if !(&timerState{}).handle(go3270.Response{AID: go3270.AIDPF3}, start, noLog) {
		t.Errorf("PF3 did not leave the asking screen")
	}
}

func TestTimerHours(t *testing.T) {
	tm := &timerState{}
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.Local)
	tm.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{timerField: "99h59m59s"}}, start, noLog)
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
