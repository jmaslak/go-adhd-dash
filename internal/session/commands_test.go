package session

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/racingmars/go3270"
)

func TestLookupCommand(t *testing.T) {
	for typed, want := range map[string]string{
		"tasks": "tasks", "Task": "tasks", " cal ": "cal", "CALENDAR": "cal",
		"calc": "calc", "dbm": "dbm", "busy": "busy", "green": "green", "off": "off",
		"?": "help", "quit": "exit", "logoff": "exit", "next": "down", "prev": "up",
		"red": "busy", "cl": "checklist", "Checklist": "checklist",
	} {
		if c, ok := lookupCommand(typed); !ok || c.name != want {
			t.Errorf("%q: got %q, %v; want %q", typed, c.name, ok, want)
		}
	}
	for _, typed := range []string{"", "ca", "tasks now", "x"} {
		if c, ok := lookupCommand(typed); ok {
			t.Errorf("%q matched %q", typed, c.name)
		}
	}
}

func TestPFCommand(t *testing.T) {
	for aid, want := range map[go3270.AID]string{
		go3270.AIDPF1: "busy", go3270.AIDPF2: "off", go3270.AIDPF3: "exit", go3270.AIDPF4: "calc",
		go3270.AIDPF5: "", go3270.AIDPF7: "up", go3270.AIDPF8: "down", go3270.AIDPF9: "cal",
		go3270.AIDPF10: "tasks", go3270.AIDPF6: "", go3270.AIDPF12: "", go3270.AIDClear: "",
	} {
		if got := pfCommand(aid, true); got != want {
			t.Errorf("PF key %x: got %q, want %q", aid, got, want)
		}
	}
	if got := pfCommand(go3270.AIDPF1, false); got != "" {
		t.Errorf("PF1 without a control port runs %q", got)
	}
}

func TestHelpScreen(t *testing.T) {
	s, crow, ccol := buildHelp(24, 80, now, "Unknown command \"x\"; type help.")
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, c := range commands {
		switch shown := strings.Contains(text, c.what); {
		case c.hidden && shown:
			t.Errorf("command list shows the hidden %q:\n%s", c.name, text)
		case !c.hidden && (!shown || !strings.Contains(text, " "+c.name)):
			t.Errorf("command list lacks %q:\n%s", c.name, text)
		}
	}
	// Hidden from the list, on the settings screen, but still commands.
	for _, name := range []string{"google", "gcal", "trello", "password", "passwd"} {
		if c, ok := lookupCommand(strings.ToUpper(name)); !ok || !c.hidden {
			t.Errorf("%s: found %v, %+v; want a hidden command", name, ok, c)
		}
	}
	if strings.Contains(text, "passwd") || strings.Contains(text, "gcal") {
		t.Errorf("command list shows a hidden command's alias:\n%s", text)
	}
	for _, want := range []string{"tasks (task)", "exit (logoff, quit)", "PF10", "Unknown command"} {
		if !strings.Contains(text, want) {
			t.Errorf("command list lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(rows[21], " Command ===>") || crow != 21 || ccol != commandInputCol+1 {
		t.Errorf("command row %q, cursor %d,%d", rows[21], crow, ccol)
	}
}

func TestDashboardCommandLine(t *testing.T) {
	s, _, _, _ := buildDashboard(24, 80, sampleView(3), 0, "")
	var input go3270.Field
	for _, f := range s {
		if f.Write {
			input = f
		}
	}
	if input.Name != commandField || input.Row != dashboardCommandRow(24) || input.Col != commandInputCol || input.Content != "" {
		t.Errorf("command field is %+v", input)
	}
	if rows := screenText(t, s, 24, 80); !strings.HasPrefix(rows[21], " Command ===>") {
		t.Errorf("command row is %q", rows[21])
	}
}

// TestFillScreen checks that a filled screen, written without erasing,
// leaves nothing of what was there before: every position is an attribute
// byte or a protected field's content, except the input fields', which are
// left as typed.
func TestFillScreen(t *testing.T) {
	v := sampleView(3)
	v.Message = "short"
	s, _, _, _ := buildDashboard(24, 80, v, 0, "")
	filled := fillScreen(s, 24, 80)
	if len(filled) != len(s) {
		t.Fatalf("filled screen has %d fields, want %d", len(filled), len(s))
	}

	covered := make([]bool, 24*80)
	for _, f := range filled {
		start := f.Row*80 + f.Col
		covered[start] = true
		n := utf8.RuneCountInString(f.Content)
		if f.Write {
			if f.Content != "" {
				t.Errorf("input field %q written with content %q", f.Name, f.Content)
			}
			n = commandWidth
		}
		for a := start + 1; a <= start+n && a < len(covered); a++ {
			covered[a] = true
		}
	}
	for a, ok := range covered {
		if !ok {
			t.Errorf("row %d col %d is not written", a/80, a%80)
		}
	}

	// Padding does not change what is shown.
	before, after := screenText(t, s, 24, 80), screenText(t, filled, 24, 80)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("filling changed the screen:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}
