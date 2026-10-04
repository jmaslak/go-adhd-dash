package session

import (
	"cmp"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/racingmars/go3270"
)

// The terminal test (option 5 on the settings screen): what the terminal
// reported of itself; every color in each kind of highlighting; every
// EBCDIC character, as its code page has it; and a field to type in, and
// the last key pressed, as the server received them.
const (
	ttInfoRow    = 2
	ttHeadRow    = 4
	ttFirstColor = ttHeadRow + 1
	ttCharsRow   = ttFirstColor + 9 // after the colors, and a blank row
	ttTypeRow    = ttCharsRow + 5   // after the characters' heading, digits and three rows
	ttField      = "ttinput"
	ttLabel      = "Type here ===>"
	ttWidth      = 40
	ttNameWidth  = 11 // a color's name, and the column after it
	ttSample     = "Sample"
	ttPrompt     = "Press any key (Enter, a PF or PA key, Clear) to see it here."
)

// ttColors are the colors tested, by name, the terminal's default first.
var ttColors = []struct {
	name  string
	color go3270.Color
}{
	{"Default", go3270.DefaultColor},
	{"Blue", go3270.Blue},
	{"Red", go3270.Red},
	{"Pink", go3270.Pink},
	{"Green", go3270.Green},
	{"Turquoise", go3270.Turquoise},
	{"Yellow", go3270.Yellow},
	{"White", go3270.White},
}

// ttStyles are the kinds of highlighting tested, each a column: its
// heading, and how a sample in it is drawn.
var ttStyles = []struct {
	heading   string
	intense   bool
	highlight go3270.Highlight
}{
	{"Normal", false, go3270.DefaultHighlight},
	{"Intense", true, go3270.DefaultHighlight},
	{"Reverse", false, go3270.ReverseVideo},
	{"Underscore", false, go3270.Underscore},
	{"Blink", false, go3270.Blink},
}

// ttStyleWidth is how many columns a style's column takes: room for its
// heading, or a sample with its attribute byte and the field ending it,
// and a space.
const ttStyleWidth = 12

// termInfo is what a terminal reported of itself on connecting.
type termInfo struct {
	Type       string // as the terminal names its type
	Rows, Cols int
	Codepage   go3270.Codepage // nil if it reported none
	LU         string
}

// ebcdicGroups is how many groups of sixteen EBCDIC characters, one for
// each first hex digit, go on a row.
const ebcdicGroups = 4

// ebcdicChars are the characters of cp at every EBCDIC code point a screen
// can show, X'40' (space) to X'FE', in order, then X'FF', which cannot be
// sent (it is the telnet IAC) and so is a blank, as is any the code page
// has nothing shown for.
func ebcdicChars(cp go3270.Codepage) []rune {
	out := make([]rune, 0, 0x100-0x40)
	for b := 0x40; b <= 0xFF; b++ {
		r := ' '
		if shown(byte(b)) {
			if s := []rune(cp.Decode([]byte{byte(b)})); len(s) == 1 && unicode.IsPrint(s[0]) {
				r = s[0]
			}
		}
		out = append(out, r)
	}
	return out
}

// ebcdicRows lays out ebcdicChars, ebcdicGroups groups of sixteen to a
// row, each after its first hex digit, as "4x": first a row of the second
// hex digits, over the characters.
func ebcdicRows(cp go3270.Codepage) []string {
	chars := ebcdicChars(cp)
	var out []string
	var digits []string
	for range ebcdicGroups {
		digits = append(digits, "   0123456789ABCDEF")
	}
	out = append(out, strings.Join(digits, " "))
	for first := 0; first < len(chars)/16; first += ebcdicGroups {
		var groups []string
		for g := first; g < first+ebcdicGroups && g < len(chars)/16; g++ {
			groups = append(groups, fmt.Sprintf("%Xx %s", 4+g, string(chars[g*16:g*16+16])))
		}
		out = append(out, strings.Join(groups, " "))
	}
	return out
}

// termTestState is one session's terminal test: what was last typed, and
// the last key pressed, as received.
type termTestState struct {
	received    string
	gotInput    bool
	lastKey     string
	row, col    int
	hasPosition bool // the last key sent the cursor's place: Enter and PF keys do, PA keys and Clear do not
}

// aidName names a key, as its keycap does.
func aidName(aid go3270.AID) string {
	switch aid {
	case go3270.AIDEnter:
		return "Enter"
	case go3270.AIDClear:
		return "Clear"
	case go3270.AIDPA1:
		return "PA1"
	case go3270.AIDPA2:
		return "PA2"
	case go3270.AIDPA3:
		return "PA3"
	case go3270.AIDNone:
		return "none"
	}
	for i, pf := range []go3270.AID{
		go3270.AIDPF1, go3270.AIDPF2, go3270.AIDPF3, go3270.AIDPF4, go3270.AIDPF5, go3270.AIDPF6,
		go3270.AIDPF7, go3270.AIDPF8, go3270.AIDPF9, go3270.AIDPF10, go3270.AIDPF11, go3270.AIDPF12,
		go3270.AIDPF13, go3270.AIDPF14, go3270.AIDPF15, go3270.AIDPF16, go3270.AIDPF17, go3270.AIDPF18,
		go3270.AIDPF19, go3270.AIDPF20, go3270.AIDPF21, go3270.AIDPF22, go3270.AIDPF23, go3270.AIDPF24,
	} {
		if aid == pf {
			return fmt.Sprintf("PF%d", i+1)
		}
	}
	return fmt.Sprintf("an unknown key (AID %02X)", byte(aid))
}

// buildTermTest renders the terminal test for the terminal info describes,
// and where the cursor goes: the field to type in.
func buildTermTest(rows, cols int, now time.Time, info termInfo, tt *termTestState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "TERMINAL TEST", now)

	codepage := info.Codepage
	cp := "no code page reported (1047 assumed)"
	if codepage == nil {
		codepage = go3270.Codepage1047()
	} else {
		cp = "code page " + codepage.ID()
	}
	screen = append(screen, placeLine(ttInfoRow, cols, line{
		{Content: "Terminal", Color: go3270.Turquoise},
		{Content: fmt.Sprintf("%s, %dx%d, %s, LU %s", cmp.Or(info.Type, "(type not reported)"), info.Rows, info.Cols, cp, info.LU), Color: go3270.White, Intense: true},
	})...)

	// The colors, a row each, in each style, a column each: every sample
	// its own field, ended by one of its own, so that no highlighting runs
	// on past it.
	heading := fmt.Sprintf("%-*s", ttNameWidth, "Color")
	for _, st := range ttStyles {
		heading += fmt.Sprintf("%-*s", ttStyleWidth, st.heading)
	}
	screen = append(screen, go3270.Field{Row: ttHeadRow, Col: 0, Color: go3270.Turquoise, Content: truncate(heading, cols-1)})
	for i, c := range ttColors {
		row := ttFirstColor + i
		screen = append(screen, go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: c.name})
		for j, st := range ttStyles {
			col := ttNameWidth + j*ttStyleWidth
			if col+1+len(ttSample)+1 > cols-1 {
				break
			}
			screen = append(screen,
				go3270.Field{Row: row, Col: col, Color: c.color, Intense: st.intense, Highlighting: st.highlight, Content: ttSample},
				go3270.Field{Row: row, Col: col + 1 + len(ttSample)},
			)
		}
	}

	// The characters, by code point: the second hex digits, then the rows.
	screen = append(screen, placeLine(ttCharsRow, cols, line{{
		Content: "Every EBCDIC character, X'40' to X'FE', in this code page:", Color: go3270.Turquoise,
	}})...)
	for i, row := range ebcdicRows(codepage) {
		color := go3270.Green
		if i == 0 {
			color = go3270.Turquoise
		}
		screen = append(screen, go3270.Field{Row: ttCharsRow + 1 + i, Col: 0, Color: color, Intense: i > 0, Content: truncate(row, cols-1)})
	}

	// Typing, and keys.
	fieldCol := len(ttLabel) + 1
	end := min(fieldCol+1+ttWidth, cols-1)
	screen = append(screen,
		go3270.Field{Row: ttTypeRow, Col: 0, Color: go3270.Turquoise, Content: ttLabel},
		go3270.Field{
			Row: ttTypeRow, Col: fieldCol, Write: true, Name: ttField,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: ttTypeRow, Col: end},
	)
	received := line{{Content: "Received:", Color: go3270.Turquoise}, {Content: "nothing yet", Color: go3270.Blue}}
	if tt.gotInput {
		received[1] = go3270.Field{Content: fmt.Sprintf("%q", tt.received), Color: go3270.White, Intense: true}
	}
	screen = append(screen, placeLine(ttTypeRow+1, cols, received)...)
	key := line{{Content: "Last key:", Color: go3270.Turquoise}, {Content: "none yet", Color: go3270.Blue}}
	switch {
	case tt.lastKey != "" && tt.hasPosition:
		key[1] = go3270.Field{Content: fmt.Sprintf("%s, cursor at row %d, column %d", tt.lastKey, tt.row+1, tt.col+1), Color: go3270.White, Intense: true}
	case tt.lastKey != "":
		key[1] = go3270.Field{Content: tt.lastKey + " (which sends no cursor position, nor what was typed)", Color: go3270.White, Intense: true}
	}
	screen = append(screen, placeLine(ttTypeRow+2, cols, key)...)

	screen = appendMessageRows(screen, rows, cols, "", false, ttPrompt, "PF3=Back")
	return screen, ttTypeRow, fieldCol + 1
}

// handle acts on a key on the terminal test, returning whether to go back
// to the settings screen: on PF3. Any other key is recorded, with what was
// typed and where the cursor was, if the key sends them.
func (tt *termTestState) handle(resp go3270.Response) (back bool) {
	if resp.AID == go3270.AIDPF3 {
		return true
	}
	tt.lastKey = aidName(resp.AID)
	switch resp.AID {
	case go3270.AIDPA1, go3270.AIDPA2, go3270.AIDPA3, go3270.AIDClear:
		tt.hasPosition = false
		return false
	}
	tt.row, tt.col, tt.hasPosition = resp.Row, resp.Col, true
	tt.received, tt.gotInput = resp.Values[ttField], true
	return false
}
