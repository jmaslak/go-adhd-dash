// Package busy drives the busy light: Luxafor flags red while a user who
// controls them is in a meeting, by their calendar, and as those users set
// it by hand. It is go-busy-indicator's logic, brought here so that the
// dashboard drives the flags itself:
//
//   - the light is red from two minutes before a meeting to two minutes
//     after; meetings of more than four hours (day-blockers) and of less
//     than two minutes (spam) do not count;
//   - "off" turns it off until the meetings under way end;
//   - "busy" and "green" force it red or green until another key;
//   - an unchanged color is sent twice, then not again until it changes.
//
// The same logic, with no light, gives every other user a busy state of
// their own (see Personal), from their calendar and their own keys.
package busy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/luxafor"
)

// The meeting rules, as go-busy-indicator has them.
const (
	// Fuzz widens a meeting at both ends: the light comes on two minutes
	// early and stays on two minutes late.
	Fuzz = 2 * time.Minute

	// Long is the length past which a meeting blocks out a day rather
	// than being one to light the flag for.
	Long = 4 * time.Hour

	// Short is the length under which a meeting is ignored: spammers send
	// zero-length invitations.
	Short = 2 * time.Minute
)

// maxRepeats is how many times the same color is sent before identical
// updates are suppressed: twice covers a dropped USB transfer without
// writing to the flag at every tick.
const maxRepeats = 2

// externalTimeout bounds a run of the external color command, so that one
// that hangs cannot hold the light.
const externalTimeout = 10 * time.Second

// Status is the light as last decided.
type Status struct {
	// Enabled is set while some user controls the light; with none, it is
	// kept off and there is nothing to show.
	Enabled bool

	// Light is "red", "green" or "off".
	Light string

	// Updated is when Light last changed.
	Updated time.Time

	// Problem is what is wrong with the flag, if anything: none attached,
	// or one that will not take a color.
	Problem string
}

// Source supplies the light's status.
type Source interface {
	Status() Status
}

// Light is a light that can be set to a color; *luxafor.Flag is one.
type Light interface {
	Indicate(r, g, b byte) error
}

// Events supplies the meetings of the users who control the light, and how
// many such users there are.
type Events func() (meetings []agenda.Event, controllers int)

// Indicator decides the light's color and sets it. It is safe for
// concurrent use.
type Indicator struct {
	light  Light // nil for none
	events Events

	// External, if set, is a command run with red, green and blue
	// arguments (0 to 255) at each change of color, for another light.
	External string

	now         func() time.Time
	runExternal func(command string, r, g, b int) error
	logf        func(format string, args ...any)

	// live decides afresh each time the status is asked for, for a light
	// no one runs.
	live bool

	mu      sync.Mutex
	manual  bool            // forced red
	green   bool            // forced green
	ignores map[string]bool // the meetings "off" was pressed during, until they end
	status  Status

	lastColor     [3]byte
	haveLast      bool
	sentTimes     int
	warnedMissing bool
}

// New returns an indicator setting light (nil for none) from the meetings
// events gives.
func New(light Light, events Events) *Indicator {
	return &Indicator{
		light: light, events: events,
		now: time.Now, runExternal: runExternal, logf: log.Printf,
		status: Status{Light: "off"},
	}
}

// Status is the light as last decided, or for a personal one, as decided
// now.
func (i *Indicator) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.live {
		i.decide(modeAuto)
	}
	return i.status
}

// Run decides the light now and every interval until ctx is canceled.
func (i *Indicator) Run(ctx context.Context, interval time.Duration) {
	for {
		i.mu.Lock()
		i.decide(modeAuto)
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// ErrUnknownKey reports a key Key does not know.
var ErrUnknownKey = errors.New("unknown key")

// Key acts on one of go-busy-indicator's keys: b forces the light red, g
// forces it green, o turns it off until the meetings under way end, and .
// decides it afresh.
func (i *Indicator) Key(key rune) error {
	m := modeAuto
	switch key {
	case 'b':
		m = modeRed
	case 'g':
		m = modeGreen
	case 'o':
		m = modeOff
	case '.':
	default:
		return fmt.Errorf("%w %q", ErrUnknownKey, key)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.decide(m)
	return nil
}

// mode is what asked for the light to be decided.
type mode int

const (
	modeAuto  mode = iota // follow the calendars
	modeOff               // off until the meetings under way end
	modeRed               // forced red
	modeGreen             // forced green
)

// meetingKey identifies a meeting across calendar reads.
func meetingKey(e agenda.Event) string {
	return e.Start.Format(time.RFC3339) + " " + e.End.Format(time.RFC3339) + " " + e.Summary
}

// counts reports whether e is a meeting to light the flag for at all:
// neither a day-blocker nor spam.
func counts(e agenda.Event) bool {
	d := e.End.Sub(e.Start)
	return d <= Long && d >= Short
}

// inMeeting reports whether now falls in e, widened by Fuzz.
func inMeeting(e agenda.Event, now time.Time) bool {
	return !now.Before(e.Start.Add(-Fuzz)) && !now.After(e.End.Add(Fuzz))
}

// decide works out the light's color, after the key m stands for, and sets
// it. i.mu must be held.
func (i *Indicator) decide(m mode) {
	meetings, controllers := i.events()
	now := i.now()
	var current []agenda.Event
	for _, e := range meetings {
		if counts(e) && inMeeting(e, now) {
			current = append(current, e)
		}
	}

	switch m {
	case modeOff:
		// The meetings running now are passed over until they end.
		i.ignores = map[string]bool{}
		for _, e := range current {
			i.ignores[meetingKey(e)] = true
		}
		i.manual, i.green = false, false
	case modeRed:
		i.manual, i.green = true, false
	case modeGreen:
		i.manual, i.green = false, true
	}
	if len(current) == 0 {
		i.ignores = nil // the override lapses once they are over
	}
	current = slices.DeleteFunc(current, func(e agenda.Event) bool { return i.ignores[meetingKey(e)] })

	light := "off"
	switch {
	case controllers == 0:
	case i.manual:
		light = "red"
	case i.green:
		light = "green"
	case len(current) > 0:
		light = "red"
	}
	if light != i.status.Light || controllers > 0 != i.status.Enabled {
		i.status.Updated = now
	}
	i.status.Enabled, i.status.Light = controllers > 0, light

	// The flag is driven dimly, so as not to be blinding at a desk; the
	// external light at full brightness.
	switch light {
	case "red":
		i.set([3]byte{20, 0, 0}, 255, 0, 0)
	case "green":
		i.set([3]byte{0, 20, 0}, 0, 255, 0)
	default:
		i.set([3]byte{0, 0, 0}, 250, 0, 250)
	}
}

// set sends color to the flag, and the external command its own, unless the
// same color has been sent maxRepeats times already. i.mu must be held.
func (i *Indicator) set(color [3]byte, extR, extG, extB int) {
	if i.haveLast && color == i.lastColor {
		i.sentTimes++
		if i.sentTimes > maxRepeats {
			return
		}
	} else {
		i.lastColor, i.haveLast, i.sentTimes = color, true, 1
	}

	if i.light != nil {
		err := i.light.Indicate(color[0], color[1], color[2])
		switch {
		case errors.Is(err, luxafor.ErrNoDevice):
			// An external light may be the only one, so this is noted, once.
			i.status.Problem = "no Luxafor flag attached"
			if !i.warnedMissing {
				i.warnedMissing = true
				i.logf("busy light: no Luxafor flag found")
			}
		case err != nil:
			i.status.Problem = "the flag is not responding"
			i.logf("busy light: %v", err)
			// It never got there, so it does not count against the
			// repeats: the next decision tries again.
			i.haveLast = false
			return
		default:
			i.warnedMissing, i.status.Problem = false, ""
		}
	}
	if i.External == "" {
		return
	}
	if err := i.runExternal(i.External, extR, extG, extB); err != nil {
		i.logf("busy light: the external color command: %v", err)
	}
}

// runExternal runs command with the color as its arguments.
func runExternal(command string, r, g, b int) error {
	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, strconv.Itoa(r), strconv.Itoa(g), strconv.Itoa(b))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", command, err)
	}
	return nil
}

// Personal keeps a busy state for each user who does not control the light:
// the same logic, from their own calendar and their own keys, with no light
// to set. Each is decided whenever it is asked for. It is safe for
// concurrent use.
type Personal struct {
	meetings func(userID int) []agenda.Event

	mu sync.Mutex
	by map[int]*Indicator
}

// NewPersonal returns the busy states of users whose meetings meetings
// gives.
func NewPersonal(meetings func(userID int) []agenda.Event) *Personal {
	return &Personal{meetings: meetings, by: map[int]*Indicator{}}
}

// For is the busy state of the user with userID, kept for as long as the
// server runs, so that their keys hold across their sessions.
func (p *Personal) For(userID int) *Indicator {
	p.mu.Lock()
	defer p.mu.Unlock()
	i, ok := p.by[userID]
	if !ok {
		i = New(nil, func() ([]agenda.Event, int) { return p.meetings(userID), 1 })
		i.live = true
		p.by[userID] = i
	}
	return i
}
