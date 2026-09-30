package session

import (
	"strings"
	"testing"

	"github.com/racingmars/go3270"
)

// dbmShow runs input on an empty stack and renders it, bottom first, as
// "log|linear" pairs joined by commas.
func dbmShow(t *testing.T, input string) string {
	t.Helper()
	stack, err := dbmRun(nil, input)
	if err != nil {
		t.Fatalf("%q: %v", input, err)
	}
	var out []string
	for _, v := range stack {
		out = append(out, v.logText()+"|"+v.linearText())
	}
	return strings.Join(out, ",")
}

func TestDBMValues(t *testing.T) {
	for input, want := range map[string]string{
		"10dBm":         "10 dBm|10 mW",
		"10dbm":         "10 dBm|10 mW",
		"0dBm":          "0 dBm|1 mW",
		"-30dBm":        "-30 dBm|0.001 mW",
		"1mW":           "0 dBm|1 mW",
		"20mw":          "13.0103 dBm|20 mW",
		"3dB":           "3 dB|x1.99526",
		"-3db":          "-3 dB|x0.501187",
		"2":             "2|",
		"0dBm 0dBm +":   "3.0103 dBm|2 mW",
		"10dBm 3dB +":   "13 dBm|19.9526 mW",
		"3dB 10dBm +":   "13 dBm|19.9526 mW",
		"3dB 4dB +":     "7 dB|x5.01187",
		"2mW 1mW -":     "0 dBm|1 mW",
		"10dBm 3dB -":   "7 dBm|5.01187 mW",
		"5dB 2dB -":     "3 dB|x1.99526",
		"10mW 2 *":      "13.0103 dBm|20 mW",
		"2 10mW *":      "13.0103 dBm|20 mW",
		"0.5dB 10 *":    "5 dB|x3.16228",
		"10mW 4 /":      "3.9794 dBm|2.5 mW",
		"20dBm 10dBm /": "10 dB|x10",
		"6dB 2 /":       "3 dB|x1.99526",
		"10mW 2 ^":      "20 dBm|100 mW",
		"2 3 ^ 4 *":     "32|",
	} {
		if got := dbmShow(t, input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
}

func TestDBMErrors(t *testing.T) {
	start, _ := dbmRun(nil, "1mW 3dB")
	for input, want := range map[string]string{
		"10DBm":             "is not a value",
		"10dBM":             "is not a value",
		"10MW":              "is not a value",
		"10 mW":             "is not a value",
		"10W":               "is not a value",
		"0mW":               "more than zero",
		"-1mW":              "more than zero",
		"1 2 +":             "needs values with units",
		"1mW 2 +":           "needs values with units",
		"1mW 1mW -":         "leaves no power",
		"3dB 1mW -":         "has no meaning",
		"1mW 1mW *":         "has no meaning",
		"3dB 1mW /":         "has no meaning",
		"1mW 0 /":           "division by zero",
		"3dB 2 ^":           "has no meaning",
		"4000dBm":           "out of range",
		"-4000dBm":          "out of range",
		"1e308mW 1e308mW +": "out of range",
		"+ +":               "needs two values",
	} {
		stack, err := dbmRun(start, input)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", input, err, want)
		}
		if len(stack) != 2 {
			t.Errorf("%q changed the stack", input)
		}
	}
}

func TestCalcModeSwitch(t *testing.T) {
	c := &calcState{}
	key := func(aid go3270.AID, input string) {
		c.handle(go3270.Response{AID: aid, Values: map[string]string{calcInputField: input}})
	}
	key(go3270.AIDEnter, "1 2")
	key(go3270.AIDPF9, "10dBm")
	if !c.dbm || c.input != "10dBm" {
		t.Fatalf("PF9: dBm %v, input %q; want dBm mode with the line kept", c.dbm, c.input)
	}
	key(go3270.AIDEnter, "10dBm 3dB +")
	if len(c.dbmStack) != 1 || len(c.stack) != 2 {
		t.Errorf("stacks: dBm %d, numbers %d; want each mode's own", len(c.dbmStack), len(c.stack))
	}

	s, _, _ := buildCalc(24, 80, now, c)
	rows := screenText(t, s, 24, 80)
	if !strings.Contains(rows[0], "dBm CALCULATOR") || !strings.HasSuffix(rows[calcHeaderRow], "dBm / dB  mW") {
		t.Errorf("title %q, headings %q", rows[0], rows[calcHeaderRow])
	}
	if !strings.HasPrefix(rows[19], "   1:") || !strings.HasSuffix(rows[19], "13 dBm  19.9526 mW") {
		t.Errorf("level 1 row is %q", rows[19])
	}
	if !strings.Contains(rows[23], "PF9=Numbers") {
		t.Errorf("help row is %q", rows[23])
	}

	key(go3270.AIDPF9, "")
	s, _, _ = buildCalc(24, 80, now, c)
	if rows := screenText(t, s, 24, 80); !strings.HasSuffix(rows[19], "2  0x2") || !strings.Contains(rows[23], "PF9=dBm") {
		t.Errorf("back in numbers: level 1 %q, help %q", rows[19], rows[23])
	}
}

func TestDBMSumAvg(t *testing.T) {
	for input, want := range map[string]string{
		"1mW 3mW sum":        "6.0206 dBm|4 mW",
		"1mW 3mW avg":        "3.0103 dBm|2 mW",
		"0dBm 0dBm 0dBm sum": "4.7712 dBm|3 mW",
		"3dB 5dB AVG":        "4 dB|x2.51189",
		"3dB 5dB sum":        "8 dB|x6.30957",
		"2 4 avg":            "3|",
	} {
		if got := dbmShow(t, input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{
		"sum":         "empty",
		"1mW 3dB sum": "all of one kind",
		"1mW 2 avg":   "all of one kind",
	} {
		if _, err := dbmRun(nil, input); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", input, err, want)
		}
	}
}
