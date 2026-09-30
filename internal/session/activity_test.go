package session

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"
)

// fakeConn is a connection from addr, which is all Activity reads of it
// until a session is terminated.
type fakeConn struct {
	net.Conn
	addr net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.addr }

func connFrom(ip string) net.Conn {
	return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5000}}
}

func TestActivityRegistry(t *testing.T) {
	a := NewActivity()
	a.add(2, connFrom("10.0.0.2"), now)
	a.add(1, connFrom("::1"), now.Add(-time.Hour))
	a.update(1, func(s *SessionActivity) { s.LU, s.Screen = "AD000001", "Dashboard" })
	a.update(9, func(s *SessionActivity) { s.LU = "gone" }) // not connected: nothing
	list := a.List()
	if len(list) != 2 || list[0].ID != 1 || list[0].Addr != "::1" || list[0].LU != "AD000001" || list[1].Addr != "10.0.0.2" || list[1].Screen != "Connecting" {
		t.Errorf("list %+v", list)
	}
	a.remove(1)
	if list := a.List(); len(list) != 1 || list[0].ID != 2 {
		t.Errorf("after removing: %+v", list)
	}

	var nilActivity *Activity
	nilActivity.add(1, connFrom("::1"), now)
	nilActivity.update(1, func(*SessionActivity) {})
	nilActivity.remove(1)
	if nilActivity.List() != nil || nilActivity.terminate(1) || nilActivity.terminated(1) {
		t.Error("nil Activity has sessions")
	}
}

func TestActivityTerminate(t *testing.T) {
	a := NewActivity()
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck
	a.add(1, server, now)

	woken := make(chan error, 1)
	go func() {
		_, err := server.Read(make([]byte, 1))
		woken <- err
	}()
	if !a.terminate(1) || !a.terminated(1) {
		t.Fatal("session not terminated")
	}
	if list := a.List(); list[0].Screen != "Terminating" {
		t.Errorf("terminated session on screen %q", list[0].Screen)
	}
	select {
	case err := <-woken:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("read ended with %v, want its deadline passing", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting read not woken")
	}
	if a.terminate(2) || a.terminated(2) {
		t.Error("session never connected terminated")
	}
	a.remove(1)
	if a.terminated(1) || a.terminate(1) {
		t.Error("session gone still terminated")
	}
}

func sampleSessions(n int) []SessionActivity {
	var sessions []SessionActivity
	for i := range n {
		sessions = append(sessions, SessionActivity{
			ID: uint64(i + 1), LU: fmt.Sprintf("AD%06X", i+1), Addr: fmt.Sprint("10.0.0.", i+1),
			Screen: "Checklist: " + strings.Repeat("long name ", 5), Connected: now.Add(-2 * time.Hour), LastKey: now.Add(-5 * time.Minute),
		})
	}
	return sessions
}

func TestActivityScreen(t *testing.T) {
	sessions := sampleSessions(25)
	sessions[0].Screen = "Dashboard"
	sessions[1].Connected = now.AddDate(0, 0, -2)
	sessions[1].User = "joelle"

	a := &activityState{marked: map[uint64]bool{1: true}}
	s, total, crow, ccol := buildActivity(24, 80, now, sessions, 2, a)
	rows := screenText(t, s, 24, 80)
	if a.page != 0 || total != 2 {
		t.Errorf("page %d of %d, want 0 of 2 (18 a page)", a.page, total)
	}
	for i, want := range map[int]string{
		actHeaderRow:    " ACTIVITY 25 sessions connected, page 1/2, 1 marked; * is this one",
		actColumnRow:    " S   LU       User       Screen             Connected Idle  IP address",
		actFirstRow:     " X   AD000001            Dashboard          08:00     5m    10.0.0.1",
		actFirstRow + 1: "   * AD000002 joelle     Checklist: long n> Sep 25    5m    10.0.0.2",
	} {
		if rows[i] != want {
			t.Errorf("row %d is %q, want %q", i, rows[i], want)
		}
	}
	if crow != actFirstRow || ccol != 1 {
		t.Errorf("cursor at %d,%d, want the first mark", crow, ccol)
	}
	for _, f := range s {
		if f.Row >= actFirstRow && f.Row < actFirstRow+18 && f.Col == actTextCol && !f.Autoskip {
			t.Errorf("field after the mark on row %d does not skip on", f.Row)
		}
	}
	if !strings.Contains(rows[22], "Type X beside sessions") || !strings.Contains(rows[23], "PF6=Terminate marked") {
		t.Errorf("message and help rows are %q, %q", rows[22], rows[23])
	}

	a.page = 9
	s, _, _, _ = buildActivity(24, 80, now, sessions, 2, a)
	if rows := screenText(t, s, 24, 80); a.page != 1 || !strings.HasPrefix(rows[actFirstRow], "     AD000013") {
		t.Errorf("page %d starts %q", a.page, rows[actFirstRow])
	}
}

func TestActivityMarking(t *testing.T) {
	sessions := sampleSessions(3)
	a := &activityState{marked: map[uint64]bool{99: true}} // 99 has gone
	key := func(aid go3270.AID, values map[string]string) (leave, confirm bool) {
		return a.handleList(go3270.Response{AID: aid, Values: values}, sessions, 1)
	}

	if _, confirm := key(go3270.AIDPF6, nil); confirm || !a.isError || !strings.Contains(a.message, "Nothing is marked") || a.marked[99] {
		t.Errorf("PF6 with nothing marked: confirm %v, message %q, marked %v", confirm, a.message, a.marked)
	}
	if _, confirm := key(go3270.AIDEnter, map[string]string{"sel:1": "q"}); confirm || !strings.Contains(a.message, `not "q"`) {
		t.Errorf("bad mark: message %q", a.message)
	}
	key(go3270.AIDEnter, map[string]string{"sel:1": "x", "sel:3": "X"})
	if len(a.marked) != 2 || a.isError {
		t.Fatalf("marks %v, message %q", a.marked, a.message)
	}
	if _, confirm := key(go3270.AIDPF6, map[string]string{"sel:3": ""}); !confirm || len(a.confirming) != 1 || a.confirming[0].ID != 1 {
		t.Errorf("PF6: confirm %v, confirming %+v", confirm, a.confirming)
	}
	if leave, _ := key(go3270.AIDPF3, nil); !leave {
		t.Error("PF3 does not leave")
	}
}

func TestTerminateConfirm(t *testing.T) {
	sessions := sampleSessions(3)
	a := &activityState{marked: map[uint64]bool{1: true, 2: true}, confirming: sessions[:2]}
	confirm := buildTerminateConfirm(24, 80, now, 2, a)
	text := strings.Join(screenText(t, confirm, 24, 80), "\n")
	// The underlined headings end at the end of their row: the next
	// attribute byte is in the row, and not underlined.
	var headings go3270.Field
	next := go3270.Field{Row: 24}
	for _, f := range confirm {
		switch at, h := f.Row*80+f.Col, actColumnRow*80+actTextCol; {
		case at == h:
			headings = f
		case at > h && at < next.Row*80+next.Col:
			next = f
		}
	}
	if headings.Highlighting != go3270.Underscore || next.Row != actColumnRow || next.Highlighting == go3270.Underscore {
		t.Errorf("headings %+v, then %+v; want the underline ended on the headings row", headings, next)
	}
	for _, want := range []string{"Terminate these 2 sessions?", "AD000001", "* AD000002", "This session (*) is among them", "PF4 to terminate"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	if text := strings.Join(screenText(t, buildTerminateConfirm(24, 80, now, 3, a), 24, 80), "\n"); strings.Contains(text, "This session") {
		t.Errorf("notes this session though it is not among them:\n%s", text)
	}
	one := &activityState{confirming: sessions[2:]}
	if text := strings.Join(screenText(t, buildTerminateConfirm(24, 80, now, 1, one), 24, 80), "\n"); !strings.Contains(text, "Terminate this session?") {
		t.Errorf("one session:\n%s", text)
	}

	activity := NewActivity()
	server, client := net.Pipe()
	defer client.Close()         //nolint:errcheck
	activity.add(1, server, now) // 2 has already disconnected

	if back := a.handleConfirm(go3270.Response{AID: go3270.AIDEnter}, activity, noLog); back || activity.terminated(1) {
		t.Error("Enter terminated or left the confirmation")
	}
	if back := a.handleConfirm(go3270.Response{AID: go3270.AIDPF4}, activity, noLog); !back || !activity.terminated(1) {
		t.Error("PF4 did not terminate")
	}
	if a.message != "Terminated 1 session. 1 had already disconnected." || a.isError || len(a.marked) != 0 || a.confirming != nil {
		t.Errorf("after PF4: message %q, marked %v, confirming %v", a.message, a.marked, a.confirming)
	}

	a.confirming = sessions[2:]
	if back := a.handleConfirm(go3270.Response{AID: go3270.AIDPF3}, activity, noLog); !back || a.confirming != nil {
		t.Error("PF3 did not cancel")
	}
}

func TestScreenName(t *testing.T) {
	for _, c := range []struct {
		mode int
		cl   checklistState
		calc calcState
		want string
	}{
		{modeDashboard, checklistState{}, calcState{}, "Dashboard"},
		{modeCalc, checklistState{}, calcState{dbm: true}, "dBm calculator"},
		{modeChecklist, checklistState{}, calcState{}, "Checklists"},
		{modeChecklist, checklistState{open: 3, openName: "Morning"}, calcState{}, "Checklist: Morning"},
		{modeActivity, checklistState{}, calcState{}, "Activity viewer"},
		{modeTerminateConfirm, checklistState{}, calcState{}, "Terminate sessions"},
	} {
		if got := screenName(c.mode, &c.cl, &c.calc); got != c.want {
			t.Errorf("mode %d: %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestActivityClaimLU(t *testing.T) {
	a := NewActivity()
	old, oldPeer := net.Pipe()
	other, otherPeer := net.Pipe()
	mine, minePeer := net.Pipe()
	for _, c := range []net.Conn{oldPeer, otherPeer, minePeer} {
		defer c.Close() //nolint:errcheck
	}
	a.add(1, old, now)
	a.add(2, other, now)
	a.add(3, mine, now)
	a.update(1, func(s *SessionActivity) { s.LU = consoleLU })
	a.update(2, func(s *SessionActivity) { s.LU = "AD000002" })

	if booted := a.claimLU(3, consoleLU, consoleBootedFarewell); fmt.Sprint(booted) != "[1]" {
		t.Errorf("booted %v, want the old console alone", booted)
	}
	if a.farewellFor(1) != consoleBootedFarewell || a.terminated(2) || a.terminated(3) {
		t.Errorf("farewells: %q %q %q", a.farewellFor(1), a.farewellFor(2), a.farewellFor(3))
	}
	if list := a.List(); list[2].LU != consoleLU {
		t.Errorf("claimer's LU is %q", list[2].LU)
	}
	// Terminated from the viewer: an administrator did it.
	a.terminate(2)
	if a.farewellFor(2) != terminatedFarewell {
		t.Errorf("viewer farewell %q", a.farewellFor(2))
	}
	var nilActivity *Activity
	if nilActivity.claimLU(1, consoleLU, "x") != nil || nilActivity.farewellFor(1) != "" {
		t.Error("nil Activity booted something")
	}
}
