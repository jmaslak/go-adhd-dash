package session

import (
	"strings"
	"testing"

	"github.com/racingmars/go3270"
)

func TestTermTestScreen(t *testing.T) {
	info := termInfo{Type: "IBM-3279-2-E", Rows: 24, Cols: 80, Codepage: go3270.Codepage037(), LU: "AD000001"}
	tt := &termTestState{}
	s, crow, ccol := buildTermTest(24, 80, now, info, tt)
	rows := screenText(t, s, 24, 80)
	text := strings.Join(rows, "\n")
	for _, want := range []string{
		"TERMINAL TEST", "Terminal IBM-3279-2-E, 24x80, code page 037, LU AD000001",
		"Color      Normal      Intense     Reverse     Underscore  Blink",
		"Received: nothing yet", "Last key: none yet", ttPrompt, "PF3=Back",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
	if crow != ttTypeRow || ccol != len(ttLabel)+2 {
		t.Errorf("cursor at %d,%d; want the field", crow, ccol)
	}

	// Every color in every style: a sample each, ended by a field of its
	// own, so that its highlighting stops with it.
	type key struct {
		color     go3270.Color
		intense   bool
		highlight go3270.Highlight
	}
	samples := map[key]int{}
	for i, f := range s {
		if f.Content != ttSample {
			continue
		}
		samples[key{f.Color, f.Intense, f.Highlighting}]++
		if i+1 >= len(s) || s[i+1].Row != f.Row || s[i+1].Col != f.Col+1+len(ttSample) || s[i+1].Highlighting != go3270.DefaultHighlight {
			t.Errorf("sample at %d,%d not ended right after it", f.Row, f.Col)
		}
	}
	for _, c := range ttColors {
		for _, st := range ttStyles {
			if samples[key{c.color, st.intense, st.highlight}] != 1 {
				t.Errorf("%s %s: %d samples", c.name, st.heading, samples[key{c.color, st.intense, st.highlight}])
			}
		}
	}

	// Every EBCDIC character, by code point: in code page 037, X'4A' is ¢,
	// X'5F' ¬, X'C1' A, X'F0' 0, X'BA' [ and X'BB' ]; X'FF' is blank.
	digits := "   0123456789ABCDEF"
	if want := strings.Join([]string{digits, digits, digits, digits}, " "); rows[ttCharsRow+1] != " "+want {
		t.Errorf("digits row %q", rows[ttCharsRow+1])
	}
	chars := ebcdicChars(go3270.Codepage037())
	at := func(b byte) rune { return chars[b-0x40] }
	for b, want := range map[byte]rune{0x40: ' ', 0x4A: '¢', 0x5F: '¬', 0xC1: 'A', 0xF0: '0', 0xBA: '[', 0xBB: ']', 0x81: 'a', 0xFF: ' '} {
		if got := at(b); got != want {
			t.Errorf("X'%02X' is %q, want %q", b, got, want)
		}
	}
	if len(chars) != 192 {
		t.Errorf("%d characters, want X'40' to X'FF'", len(chars))
	}
	for i, first := range []string{"4x", "8x", "Cx"} {
		row := rows[ttCharsRow+2+i]
		if !strings.HasPrefix(row, " "+first+" ") || len([]rune(row)) < 79 {
			t.Errorf("row %d is %q", i, row)
		}
	}
	if !strings.Contains(rows[ttCharsRow+2], "4x  ") || !strings.Contains(rows[ttCharsRow+4], "Fx 0123456789") {
		t.Errorf("rows:\n%s\n%s", rows[ttCharsRow+2], rows[ttCharsRow+4])
	}
	if !strings.Contains(text, "Every EBCDIC character, X'40' to X'FE', in this code page:") || strings.Contains(text, "ASCII") {
		t.Errorf("heading:\n%s", text)
	}

	// Another code page, other characters: in 273 (German), X'4A' is Ä.
	if got := ebcdicChars(go3270.Codepage273())[0x4A-0x40]; got != 'Ä' {
		t.Errorf("273's X'4A' is %q", got)
	}

	// No code page, or no type, reported: said so.
	s, _, _ = buildTermTest(24, 80, now, termInfo{Rows: 24, Cols: 80, LU: "AD000002"}, tt)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "(type not reported)") || !strings.Contains(text, "no code page reported (1047 assumed)") {
		t.Errorf("nothing reported:\n%s", text)
	}
}

func TestTermTestKeys(t *testing.T) {
	info := termInfo{Type: "IBM-3278-2", Rows: 24, Cols: 80, Codepage: go3270.Codepage1047(), LU: "AD000001"}
	tt := &termTestState{}
	shown := func() string {
		s, _, _ := buildTermTest(24, 80, now, info, tt)
		return strings.Join(screenText(t, s, 24, 80), "\n")
	}

	if tt.handle(go3270.Response{AID: go3270.AIDPF7, Row: 2, Col: 11, Values: map[string]string{ttField: "héllo [ok]"}}) {
		t.Fatal("PF7 went back")
	}
	if text := shown(); !strings.Contains(text, `Received: "héllo [ok]"`) || !strings.Contains(text, "Last key: PF7, cursor at row 3, column 12") {
		t.Errorf("after PF7:\n%s", text)
	}
	// PA keys and Clear send neither the cursor nor what was typed: what
	// was received before stays.
	tt.handle(go3270.Response{AID: go3270.AIDPA2})
	if text := shown(); !strings.Contains(text, "Last key: PA2 (which sends no cursor position, nor what was typed)") || !strings.Contains(text, `"héllo [ok]"`) {
		t.Errorf("after PA2:\n%s", text)
	}
	tt.handle(go3270.Response{AID: go3270.AIDEnter, Row: 18, Col: 15, Values: map[string]string{ttField: ""}})
	if text := shown(); !strings.Contains(text, `Received: ""`) || !strings.Contains(text, "Last key: Enter, cursor at row 19, column 16") {
		t.Errorf("after Enter:\n%s", text)
	}
	if !tt.handle(go3270.Response{AID: go3270.AIDPF3}) {
		t.Error("PF3 did not go back")
	}

	for aid, want := range map[go3270.AID]string{
		go3270.AIDPF1: "PF1", go3270.AIDPF12: "PF12", go3270.AIDPF13: "PF13", go3270.AIDPF24: "PF24",
		go3270.AIDPA1: "PA1", go3270.AIDPA3: "PA3", go3270.AIDClear: "Clear", go3270.AIDEnter: "Enter",
		go3270.AID(0x01): "an unknown key (AID 01)",
	} {
		if got := aidName(aid); got != want {
			t.Errorf("aid %02X: %q, want %q", byte(aid), got, want)
		}
	}
}
