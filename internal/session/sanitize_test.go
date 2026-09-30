package session

import (
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
)

func TestSafeText(t *testing.T) {
	for _, cp := range []go3270.Codepage{go3270.Codepage037(), go3270.Codepage1047()} {
		for in, want := range map[string]string{
			"plain text [x] ^~|!":       "plain text [x] ^~|!",
			"café":                      "café",
			"box ─ block █ euro €":      "box ─ block █ euro €",
			"sf\u001dsba\u0011ic\u0013": "sf?sba?ic?",
			"tab\tnul\x00":              "tab?nul?",
			"iac\u009feor":              "iac?eor",
			"del\u007f nel\u0085":       "del? nel?",
			"emoji 😀":                   "emoji ?",
			"":                          "",
		} {
			got := safeText(in, cp)
			if got != want {
				t.Errorf("%s: %q -> %q, want %q", cp.ID(), in, got, want)
			}
			if utf8.RuneCountInString(got) != utf8.RuneCountInString(in) {
				t.Errorf("%s: %q changed length", cp.ID(), in)
			}
		}
	}
}

// assertShowable fails for any field of screen whose text would encode, in
// cp, to anything but bytes the terminal shows.
func assertShowable(t *testing.T, screen go3270.Screen, cp go3270.Codepage) {
	t.Helper()
	for _, f := range screen {
		enc := cp.Encode(f.Content)
		for i := 0; i < len(enc); i++ {
			switch {
			case enc[i] == geOrder && i+1 < len(enc) && shown(enc[i+1]):
				i++
			case !shown(enc[i]):
				t.Errorf("field at %d,%d %q encodes to % X", f.Row, f.Col, f.Content, enc)
				return
			}
		}
	}
}

func TestSanitizeScreens(t *testing.T) {
	cp := go3270.Codepage037()
	evil := "hi\u001dè\u0011@@\u0013\u009fï" // SF, SBA, IC, IAC EOR

	// A chat message from a hostile client.
	c := NewChat()
	conn, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	c.watch(2, "AD000002", conn)
	c.post(1, "AD000001", evil, now)
	chat, _, _ := buildChat(24, 80, now, c, "AD000002", &chatState{})
	if !strings.Contains(strings.Join(screenText(t, chat, 24, 80), ""), "\u001d") {
		t.Fatal("test is broken: the chat screen did not carry the controls")
	}
	safe := sanitizeScreen(chat, cp)
	assertShowable(t, safe, cp)
	if !strings.Contains(strings.Join(screenText(t, safe, 24, 80), "\n"), "hi?è?@@??ï") {
		t.Errorf("sanitized chat:\n%s", strings.Join(screenText(t, safe, 24, 80), "\n"))
	}
	if chat[len(chat)-1].Content == safe[len(safe)-1].Content && strings.Contains(chat[len(chat)-1].Content, "\u001d") {
		t.Error("sanitizeScreen changed the screen it was given")
	}

	// A calendar invitation's title, from anyone at all.
	v := sampleView(2)
	v.Agenda.Events = []agenda.Event{{Summary: evil, Start: now.Add(10 * 60e9), End: now.Add(20 * 60e9)}}
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	assertShowable(t, sanitizeScreen(s, cp), cp)

	// And filled for a timed redraw, as the dashboard is.
	assertShowable(t, sanitizeScreen(fillScreen(s, 24, 80), cp), cp)
}
