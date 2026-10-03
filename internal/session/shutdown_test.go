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

func TestShutdownWakesAndRefuses(t *testing.T) {
	s := NewShutdown()
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck
	if !s.add(server) || s.Sessions() != 1 {
		t.Fatalf("session not counted: %d", s.Sessions())
	}

	woken := make(chan error, 1)
	go func() {
		_, err := server.Read(make([]byte, 1))
		woken <- err
	}()
	s.Request()
	s.Request() // again does nothing
	select {
	case err := <-woken:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("read ended with %v, want its deadline passing", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting read not woken by the shutdown request")
	}
	if !s.Requested() {
		t.Error("shutdown not reported as requested")
	}
	select {
	case <-s.Done():
	default:
		t.Error("Done not closed")
	}

	late, _ := net.Pipe()
	if s.add(late) {
		t.Error("session added after shutdown was requested")
	}

	s.remove(server)
	finished := make(chan struct{})
	go func() {
		s.Wait(time.Hour)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return with no sessions left")
	}
}

func TestShutdownWaitClosesStragglers(t *testing.T) {
	s := NewShutdown()
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck
	s.add(server)
	s.Request()

	// A session that does not notice the request until its connection is
	// closed, then takes a while longer to finish, as with a file being
	// written.
	go func() {
		for {
			if _, err := server.Read(make([]byte, 1)); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			_ = server.SetReadDeadline(time.Time{})
		}
		time.Sleep(50 * time.Millisecond)
		s.remove(server)
	}()

	start := time.Now()
	s.Wait(20 * time.Millisecond)
	if s.Sessions() != 0 {
		t.Errorf("Wait returned with %d sessions left", s.Sessions())
	}
	if d := time.Since(start); d < 70*time.Millisecond {
		t.Errorf("Wait returned after %v, before the session finished", d)
	}
}

func TestNilShutdown(t *testing.T) {
	var s *Shutdown
	conn, _ := net.Pipe()
	if !s.add(conn) || s.Requested() || s.Sessions() != 0 {
		t.Error("a nil Shutdown should admit every session and never be requested")
	}
	s.remove(conn)
}

func TestAdminMenu(t *testing.T) {
	s, row, col := buildAdmin(24, 80, now, "", false, 3)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{"ADMIN MENU 3 sessions connected", "  1 Shut down the server", "Option ===>", "PF3=Back Enter=Select"} {
		if !strings.Contains(text, want) {
			t.Errorf("admin menu lacks %q:\n%s", want, text)
		}
	}
	if row != 21 || col != len(adminOptionLabel)+2 {
		t.Errorf("cursor at %d,%d, want the option field", row, col)
	}

	s, _, _ = buildAdmin(24, 80, now, "Cleared the chat (2 messages).", true, 1)
	for _, f := range s {
		if f.Content == "Cleared the chat (2 messages)." && f.Color != go3270.Green {
			t.Errorf("done message not green: %+v", f)
		}
	}

	s, _, _ = buildAdmin(24, 80, now, `There is no option "9".`, false, 1)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, `There is no option "9".`) {
		t.Errorf("admin menu lacks the message:\n%s", text)
	}

	for _, c := range []struct {
		aid    go3270.AID
		typed  string
		action adminAction
		told   bool
	}{
		{go3270.AIDPF3, "1", adminLeave, false},
		{go3270.AIDEnter, " 1 ", adminShutdown, false},
		{go3270.AIDEnter, "2", adminActivity, false},
		{go3270.AIDEnter, "3", adminClearChat, false},
		{go3270.AIDEnter, "4", adminUsers, false},
		{go3270.AIDEnter, "8", adminBackup, false},
		{go3270.AIDEnter, "", adminStay, false},
		{go3270.AIDEnter, "9", adminStay, true},
		{go3270.AIDPF4, "1", adminStay, false},
	} {
		action, msg := adminChoice(go3270.Response{AID: c.aid, Values: map[string]string{adminOptionField: c.typed}})
		if action != c.action || (msg != "") != c.told {
			t.Errorf("%v %q: action %v, message %q", c.aid, c.typed, action, msg)
		}
	}
}

func TestShutdownConfirm(t *testing.T) {
	for sessions, want := range map[int]string{1: "this session and 0 others,", 2: "this session and 1 other,", 4: "this session and 3 others,"} {
		text := strings.Join(screenText(t, buildShutdownConfirm(24, 80, now, sessions), 24, 80), "\n")
		if !strings.Contains(text, want) || !strings.Contains(text, "PF4 to shut down") {
			t.Errorf("%d sessions: confirmation lacks %q:\n%s", sessions, want, text)
		}
	}
	if text := strings.Join(screenText(t, buildFarewell(80, now, shutdownFarewell), 24, 80), "\n"); !strings.Contains(text, "shutting down") {
		t.Errorf("goodbye screen is:\n%s", text)
	}
	if c, ok := lookupCommand("ADMIN"); !ok || c.name != "admin" {
		t.Error("admin command not found")
	}
}

func TestAdminBackup(t *testing.T) {
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }

	msg, ok := runBackup(func() (string, error) { return "/var/lib/adhd-dash/backup/adhd-dash-20261002-174428.tar.gz", nil }, logf)
	if !ok || msg != "Backed up to adhd-dash-20261002-174428.tar.gz in /var/lib/adhd-dash/backup." {
		t.Errorf("made: %q, %v", msg, ok)
	}
	msg, ok = runBackup(func() (string, error) { return "", errors.New("disk full") }, logf)
	if ok || msg != "Backup failed: disk full" {
		t.Errorf("failed: %q, %v", msg, ok)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "backed up to") || !strings.Contains(logged[1], "disk full") {
		t.Errorf("logged %q", logged)
	}
	if msg, ok := runBackup(nil, logf); ok || !strings.Contains(msg, "cannot make backups") {
		t.Errorf("no backups: %q, %v", msg, ok)
	}

	// The menu shows it, and a message that long fits on a Model 2.
	s, _, _ := buildAdmin(24, 80, time.Now(), "Backed up to adhd-dash-20261002-174428.tar.gz in /var/lib/adhd-dash/backup.", true, 1)
	text := strings.Join(screenText(t, s, 24, 80), "\n")
	for _, want := range []string{"8 Back up the users and checklists", "in /var/lib/adhd-dash/backup."} {
		if !strings.Contains(text, want) {
			t.Errorf("admin menu lacks %q:\n%s", want, text)
		}
	}
}
