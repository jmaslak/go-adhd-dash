package session

import (
	"strings"
	"testing"

	"github.com/racingmars/go3270"
)

// run applies input to an empty stack, returning the stack's decimal values
// bottom first, joined by spaces.
func run(t *testing.T, input string) string {
	t.Helper()
	stack, err := calcRun(nil, input)
	if err != nil {
		t.Fatalf("%q: %v", input, err)
	}
	var out []string
	for _, v := range stack {
		out = append(out, v.decimal())
	}
	return strings.Join(out, " ")
}

func TestCalcArithmetic(t *testing.T) {
	for input, want := range map[string]string{
		"3 4 +":              "7",
		"3 4 -":              "-1",
		"3 4 *":              "12",
		"12 4 /":             "3",
		"7 2 /":              "3.5",
		"2 10 ^":             "1024",
		"2 100 ^":            "1267650600228229401496703205376",
		"2 -1 ^":             "0.5",
		"2 0.5 ^":            "1.4142135623730951",
		"0x1F 1 +":           "32",
		"0xff 0X10 *":        "4080",
		"-0x10":              "-16",
		"1.5 2 *":            "3",
		"1e3 1 +":            "1001",
		".5 +2 +":            "2.5",
		"1 2 3":              "1 2 3",
		"":                   "",
		"99999 9999999999 ^": "+Inf",
	} {
		if want == "+Inf" {
			if _, err := calcRun(nil, input); err == nil || !strings.Contains(err.Error(), "out of range") {
				t.Errorf("%q: got %v, want out of range", input, err)
			}
			continue
		}
		if got := run(t, input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
}

func TestCalcErrorsLeaveStack(t *testing.T) {
	start, _ := calcRun(nil, "1 2")
	for input, want := range map[string]string{
		"3 +":      "", // fine: 2 3 + leaves 1 5
		"+ + +":    "needs two numbers",
		"1 0 /":    "division by zero",
		"1.5 0 /":  "division by zero",
		"-8 0.5 ^": "not a real number",
		"0x1.8":    "is not a number",
		"0xg":      "is not a number",
		"inf":      "is not a number",
		"3 4 plus": "is not a number",
		"0x-1":     "is not a number",
		"1,000":    "is not a number",
	} {
		stack, err := calcRun(start, input)
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", input, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", input, err, want)
		}
		if len(stack) != 2 || stack[0].decimal() != "1" || stack[1].decimal() != "2" {
			t.Errorf("%q changed the stack to %v", input, stack)
		}
	}
}

func TestCalcHex(t *testing.T) {
	stack, err := calcRun(nil, "255 -26 7 2 / 4 2 /")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range stack {
		got = append(got, v.hex())
	}
	if want := []string{"0xFF", "-0x1A", "", "0x2"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hex = %q, want %q; a float has none", got, want)
	}
}

func TestCalcKeys(t *testing.T) {
	c := &calcState{}
	key := func(aid go3270.AID, input string) bool {
		return c.handle(go3270.Response{AID: aid, Values: map[string]string{calcInputField: input}})
	}
	decimals := func() string {
		var out []string
		for _, v := range c.stack {
			out = append(out, v.decimal())
		}
		return strings.Join(out, " ")
	}

	key(go3270.AIDEnter, "1 2 3")
	if decimals() != "1 2 3" || c.input != "" {
		t.Fatalf("after Enter: stack %q, input %q", decimals(), c.input)
	}
	key(go3270.AIDPF5, "")
	if decimals() != "1 3 2" {
		t.Errorf("after swap: %q", decimals())
	}
	key(go3270.AIDPF4, "9") // pushes 9, then drops it
	if decimals() != "1 3 2" {
		t.Errorf("after typing 9 and dropping: %q", decimals())
	}
	key(go3270.AIDEnter, "4 oops")
	if decimals() != "1 3 2" || c.input != "4 oops" || !c.isError {
		t.Errorf("bad line: stack %q, input %q, error %v; want the stack kept and the line left to fix", decimals(), c.input, c.isError)
	}
	key(go3270.AIDPF6, "")
	if decimals() != "" {
		t.Errorf("after clear: %q", decimals())
	}
	key(go3270.AIDPF4, "")
	if !strings.Contains(c.message, "empty") {
		t.Errorf("dropping from an empty stack: message %q", c.message)
	}
	key(go3270.AIDPF5, "1")
	if !strings.Contains(c.message, "two values") || decimals() != "1" {
		t.Errorf("swapping one number: message %q, stack %q", c.message, decimals())
	}
	key(go3270.AIDPF7, "5 6")
	if c.input != "5 6" || decimals() != "1" {
		t.Errorf("an unassigned key: input %q, stack %q; want the line kept, not run", c.input, decimals())
	}
	if !key(go3270.AIDPF3, "") {
		t.Errorf("PF3 did not leave")
	}
}

func TestCalcScreen(t *testing.T) {
	c := &calcState{}
	c.stack, _ = calcRun(nil, "255 7 2 /")
	s, crow, ccol := buildCalc(24, 80, now, c)
	rows := screenText(t, s, 24, 80)
	if !strings.HasPrefix(rows[calcHeaderRow], "  Lvl") || !strings.HasSuffix(rows[calcHeaderRow], "Decimal  Hex") {
		t.Errorf("headings are %q", rows[calcHeaderRow])
	}
	// Level 1, the top, is at the bottom, just above the blank row.
	if !strings.HasPrefix(rows[19], "   1:") || !strings.HasSuffix(rows[19], " 3.5") {
		t.Errorf("level 1 row is %q", rows[19])
	}
	if !strings.HasPrefix(rows[18], "   2:") || !strings.HasSuffix(rows[18], " 255  0xFF") {
		t.Errorf("level 2 row is %q", rows[18])
	}
	if rows[17] != "" || rows[20] != "" {
		t.Errorf("rows above the stack and before the input are %q, %q; want blank", rows[17], rows[20])
	}
	if !strings.HasPrefix(rows[21], " ===>") || crow != 21 || ccol != 6 {
		t.Errorf("input row %q, cursor %d,%d", rows[21], crow, ccol)
	}

	// Deeper than the screen: the first stack row counts what is not shown.
	c.stack, _ = calcRun(nil, strings.Repeat("1 ", 30))
	s, _, _ = buildCalc(24, 80, now, c)
	rows = screenText(t, s, 24, 80)
	if !strings.Contains(rows[calcFirstRow], "(14 more)") || !strings.HasPrefix(rows[calcFirstRow+1], "  16:") {
		t.Errorf("deep stack: first rows %q, %q", rows[calcFirstRow], rows[calcFirstRow+1])
	}
}

func TestCalcSumAvg(t *testing.T) {
	for input, want := range map[string]string{
		"1 2 3 sum":       "6",
		"1 2 3 4 avg":     "2.5",
		"2 4 avg":         "3",
		"0x10 0x20 SUM":   "48",
		"1.5 2 sum":       "3.5",
		"7 sum":           "7",
		"1 2 sum 3 *":     "9",
		"1 2 3 avg 10 20": "2 10 20",
	} {
		if got := run(t, input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
	if stack, _ := calcRun(nil, "0x10 0x20 sum"); stack[0].hex() != "0x30" {
		t.Errorf("sum of integers is not an integer: %v", stack[0])
	}
	for _, input := range []string{"sum", "avg", "1 2 avg avg 3 - sum sum x"} {
		if _, err := calcRun(nil, input); err == nil {
			t.Errorf("%q: no error", input)
		}
	}
}
