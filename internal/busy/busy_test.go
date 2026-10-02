package busy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/luxafor"
)

var base = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

// fakeLight records the colors sent, failing as told.
type fakeLight struct {
	mu   sync.Mutex
	sent [][3]byte
	err  error
}

func (f *fakeLight) Indicate(r, g, b byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, [3]byte{r, g, b})
	return nil
}

func (f *fakeLight) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// rig is an indicator over one controller's meetings, at a set time.
type rig struct {
	*Indicator
	flag        *fakeLight
	meetings    []agenda.Event
	controllers int
	at          time.Time
	external    [][3]int
	logged      []string
}

func newRig() *rig {
	r := &rig{flag: &fakeLight{}, controllers: 1, at: base}
	r.Indicator = New(r.flag, func() ([]agenda.Event, int) { return r.meetings, r.controllers })
	r.now = func() time.Time { return r.at }
	r.runExternal = func(_ string, red, green, blue int) error {
		r.external = append(r.external, [3]int{red, green, blue})
		return nil
	}
	r.logf = func(f string, a ...any) { r.logged = append(r.logged, fmt.Sprintf(f, a...)) }
	return r
}

// decided decides, and returns the light.
func (r *rig) decided() string {
	r.Key('.') //nolint:errcheck
	return r.Status().Light
}

func meeting(start, end time.Duration, title string) agenda.Event {
	return agenda.Event{Summary: title, Start: base.Add(start), End: base.Add(end)}
}

func TestMeetingRules(t *testing.T) {
	for _, c := range []struct {
		name string
		e    agenda.Event
		want string
	}{
		{"under way", meeting(-10*time.Minute, 20*time.Minute, "Standup"), "red"},
		{"starting in a minute", meeting(time.Minute, 30*time.Minute, "Soon"), "red"},
		{"starting in three minutes", meeting(3*time.Minute, 30*time.Minute, "Later"), "off"},
		{"ended a minute ago", meeting(-30*time.Minute, -time.Minute, "Over"), "red"},
		{"ended three minutes ago", meeting(-30*time.Minute, -3*time.Minute, "Long over"), "off"},
		{"a day-blocker", meeting(-time.Hour, 4*time.Hour, "Offsite"), "off"},
		{"spam", meeting(0, time.Minute, "Spam"), "off"},
		{"exactly four hours", meeting(-time.Hour, 3*time.Hour, "Workshop"), "red"},
	} {
		r := newRig()
		r.meetings = []agenda.Event{c.e}
		if got := r.decided(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestKeys(t *testing.T) {
	r := newRig()
	r.meetings = []agenda.Event{meeting(-10*time.Minute, 20*time.Minute, "Standup"), meeting(time.Hour, 2*time.Hour, "Review")}
	if r.decided() != "red" {
		t.Fatal("not red in a meeting")
	}

	// Off holds until the meeting under way is over, then lapses.
	if err := r.Key('o'); err != nil || r.Status().Light != "off" {
		t.Fatalf("o: %v, %s", err, r.Status().Light)
	}
	r.at = base.Add(10 * time.Minute)
	if r.decided() != "off" {
		t.Errorf("off did not hold through the meeting")
	}
	r.at = base.Add(time.Hour)
	if r.decided() != "red" {
		t.Errorf("off held into the next meeting")
	}

	// b and g force a color until another key.
	r.at = base.Add(30 * time.Minute)
	r.Key('b') //nolint:errcheck
	if r.decided() != "red" {
		t.Errorf("b: %s", r.Status().Light)
	}
	r.Key('g') //nolint:errcheck
	r.at = base.Add(time.Hour)
	if r.decided() != "green" {
		t.Errorf("g held: %s", r.Status().Light)
	}
	r.Key('o') //nolint:errcheck
	if r.Status().Light != "off" {
		t.Errorf("o after g: %s", r.Status().Light)
	}
	if err := r.Key('x'); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("x: %v", err)
	}

	// With no one controlling it, it is off, and not enabled.
	r.Key('b') //nolint:errcheck
	r.controllers = 0
	if s := r.Status(); r.decided() != "off" || r.Status().Enabled {
		t.Errorf("with no controllers: %+v", s)
	}
}

func TestSending(t *testing.T) {
	r := newRig()
	r.External = "/bin/true"
	for range 5 {
		r.decided()
	}
	if n := r.flag.count(); n != maxRepeats {
		t.Errorf("off sent %d times, want %d", n, maxRepeats)
	}
	r.meetings = []agenda.Event{meeting(-time.Minute, 30*time.Minute, "Now")}
	r.decided()
	if got := r.flag.sent[len(r.flag.sent)-1]; got != [3]byte{20, 0, 0} || r.external[len(r.external)-1] != [3]int{255, 0, 0} {
		t.Errorf("red sent as %v, externally %v", got, r.external[len(r.external)-1])
	}

	// No flag: noted once, and the external light still set.
	r.flag.err = luxafor.ErrNoDevice
	r.meetings = nil
	r.decided()
	r.Key('g') //nolint:errcheck
	if s := r.Status(); s.Problem != "no Luxafor flag attached" || len(r.logged) != 1 || r.external[len(r.external)-1] != [3]int{0, 255, 0} {
		t.Errorf("no flag: %+v, logged %v", s, r.logged)
	}
	// A flag failing is tried again at the next decision.
	r.flag.err = errors.New("pipe broken")
	r.decided()
	r.decided()
	r.decided()
	if s := r.Status(); s.Problem != "the flag is not responding" {
		t.Errorf("failing flag: %+v", s)
	}
	r.flag.err = nil
	r.decided()
	if s := r.Status(); s.Problem != "" || r.flag.count() == 0 {
		t.Errorf("flag back: %+v", s)
	}
}

func TestControlPort(t *testing.T) {
	r := newRig()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.ServeControl(ctx, conn); close(done) }()
	send := func(msg string) {
		c, err := net.Dial("udp", conn.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()      //nolint:errcheck
		c.Write([]byte(msg)) //nolint:errcheck
	}
	wait := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if r.Status().Light == want {
				return
			}
		}
		t.Errorf("light %s, want %s", r.Status().Light, want)
	}
	send("KEY b")
	wait("red")
	send("KEY o")
	wait("off")
	send("CAMERA ON") // no longer understood
	send("KEY g")
	wait("green")
	cancel()
	<-done
}

func TestParseControl(t *testing.T) {
	for in, want := range map[string]string{
		"KEY b": "b", "KEY .": ".", "KEY bb": "", "KEY ": "", "CAMERA ON": "", "camera on": "", "": "",
	} {
		key, ok := parseControl(in)
		got := ""
		if ok {
			got = string(key)
		}
		if got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPersonal(t *testing.T) {
	at := base
	meetings := map[int][]agenda.Event{1: {meeting(-10*time.Minute, 20*time.Minute, "Standup")}}
	p := NewPersonal(func(id int) []agenda.Event { return meetings[id] })
	one, two := p.For(1), p.For(2)
	one.now, two.now = func() time.Time { return at }, func() time.Time { return at }
	if p.For(1) != one {
		t.Fatalf("a user's state not kept")
	}
	if s := one.Status(); !s.Enabled || s.Light != "red" {
		t.Errorf("user 1 in a meeting: %+v", s)
	}
	if s := two.Status(); !s.Enabled || s.Light != "off" {
		t.Errorf("user 2 with none: %+v", s)
	}
	// Each one's keys are their own.
	two.Key('g') //nolint:errcheck
	one.Key('o') //nolint:errcheck
	if one.Status().Light != "off" || two.Status().Light != "green" {
		t.Errorf("keys: %s, %s", one.Status().Light, two.Status().Light)
	}
	// Decided when asked: the meeting ends, then another begins.
	at = base.Add(25 * time.Minute)
	if one.Status().Light != "off" {
		t.Errorf("after the meeting: %s", one.Status().Light)
	}
	meetings[1] = append(meetings[1], meeting(30*time.Minute, time.Hour, "Review"))
	at = base.Add(30 * time.Minute)
	if one.Status().Light != "red" {
		t.Errorf("next meeting: %s", one.Status().Light)
	}
}

func TestWatch(t *testing.T) {
	r := newRig()
	r.decided()
	var mu sync.Mutex
	told := 0
	stop := r.Watch(func() { mu.Lock(); told++; mu.Unlock() })
	count := func() int { mu.Lock(); defer mu.Unlock(); return told }

	r.Key('b') //nolint:errcheck
	if count() != 1 {
		t.Errorf("told %d times of going red, want 1", count())
	}
	r.Key('b') //nolint:errcheck
	r.Key('.') //nolint:errcheck
	if count() != 1 {
		t.Errorf("told %d times with nothing changed, want 1", count())
	}
	r.controllers = 0
	r.Key('.') //nolint:errcheck
	if count() != 2 {
		t.Errorf("told %d times, after being disabled, want 2", count())
	}
	stop()
	r.controllers = 1
	r.Key('g') //nolint:errcheck
	if count() != 2 {
		t.Errorf("told after stopping")
	}
}

func TestPersonalRunTells(t *testing.T) {
	var mu sync.Mutex
	at := base
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	p := NewPersonal(func(int) []agenda.Event { return []agenda.Event{meeting(time.Hour, 2*time.Hour, "Later")} })
	i := p.For(1)
	i.now = clock
	i.Status()
	told := make(chan struct{}, 4)
	i.Watch(func() { told <- struct{}{} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, 10*time.Millisecond)
	select {
	case <-told:
		t.Fatalf("told with nothing changed")
	case <-time.After(50 * time.Millisecond):
	}
	// The meeting begins: the clock alone changes it, and Run tells.
	mu.Lock()
	at = base.Add(time.Hour)
	mu.Unlock()
	select {
	case <-told:
	case <-time.After(5 * time.Second):
		t.Fatalf("not told of the meeting beginning")
	}
	if i.Status().Light != "red" {
		t.Errorf("light %s", i.Status().Light)
	}
}
