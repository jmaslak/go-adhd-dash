package session

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"
)

// calcValue is one number on the calculator's stack: an integer, kept exact
// however large, or a float.
type calcValue struct {
	i *big.Int // non-nil for an integer
	f float64
}

func calcInt(i *big.Int) calcValue  { return calcValue{i: i} }
func calcFloat(f float64) calcValue { return calcValue{f: f} }
func (v calcValue) isInt() bool     { return v.i != nil }
func (v calcValue) isZero() bool    { return (v.isInt() && v.i.Sign() == 0) || (!v.isInt() && v.f == 0) }
func (v calcValue) String() string  { return v.decimal() }

// float is v as a float, which may be infinite for a huge integer.
func (v calcValue) float() float64 {
	if !v.isInt() {
		return v.f
	}
	f, _ := new(big.Float).SetInt(v.i).Float64()
	return f
}

// decimal is v in decimal.
func (v calcValue) decimal() string {
	if v.isInt() {
		return v.i.String()
	}
	return strconv.FormatFloat(v.f, 'g', -1, 64)
}

// hex is an integer in hex, with a 0x prefix after any sign, and empty for a
// float.
func (v calcValue) hex() string {
	if !v.isInt() {
		return ""
	}
	if v.i.Sign() < 0 {
		return "-0x" + strings.ToUpper(new(big.Int).Neg(v.i).Text(16))
	}
	return "0x" + strings.ToUpper(v.i.Text(16))
}

// calcDecimal matches a decimal number: an integer, or a float with a
// fraction or an exponent. Words ParseFloat would take, such as "inf", are
// not numbers here.
var calcDecimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// parseCalcNumber reads a number typed on the calculator: hex as 0x1F (an
// integer, optionally signed), or decimal.
func parseCalcNumber(tok string) (calcValue, bool) {
	sign, digits := "", tok
	if tok != "" && (tok[0] == '-' || tok[0] == '+') {
		sign, digits = tok[:1], tok[1:]
	}
	if hex, ok := strings.CutPrefix(strings.ToLower(digits), "0x"); ok {
		i, ok := new(big.Int).SetString(hex, 16)
		if !ok || strings.ContainsAny(hex, "+-_") {
			return calcValue{}, false
		}
		if sign == "-" {
			i.Neg(i)
		}
		return calcInt(i), true
	}
	if !calcDecimal.MatchString(tok) {
		return calcValue{}, false
	}
	if i, ok := new(big.Int).SetString(strings.TrimPrefix(tok, "+"), 10); ok {
		return calcInt(i), true
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil || math.IsInf(f, 0) {
		return calcValue{}, false
	}
	return calcFloat(f), true
}

// calcMaxPowerBits bounds the size of an exact integer power; beyond it the
// power is worked out as a float, so a typo like 99 99999 ^ cannot tie up the
// server building a number with millions of digits.
const calcMaxPowerBits = 1 << 16

// calcOps are the operators, each taking two numbers off the stack.
const calcOps = "+-*/^"

// applyCalcOp works out a op b. Integers stay exact where the result is an
// integer; anything else is a float.
func applyCalcOp(op string, a, b calcValue) (calcValue, error) {
	if a.isInt() && b.isInt() {
		switch op {
		case "+":
			return calcInt(new(big.Int).Add(a.i, b.i)), nil
		case "-":
			return calcInt(new(big.Int).Sub(a.i, b.i)), nil
		case "*":
			return calcInt(new(big.Int).Mul(a.i, b.i)), nil
		case "/":
			if b.i.Sign() == 0 {
				return calcValue{}, errors.New("division by zero")
			}
			q, r := new(big.Int).QuoRem(a.i, b.i, new(big.Int))
			if r.Sign() == 0 {
				return calcInt(q), nil
			}
		case "^":
			if b.i.Sign() >= 0 && b.i.IsInt64() && int64(a.i.BitLen())*b.i.Int64() <= calcMaxPowerBits {
				return calcInt(new(big.Int).Exp(a.i, b.i, nil)), nil
			}
		}
	}

	x, y := a.float(), b.float()
	var r float64
	switch op {
	case "+":
		r = x + y
	case "-":
		r = x - y
	case "*":
		r = x * y
	case "/":
		if b.isZero() {
			return calcValue{}, errors.New("division by zero")
		}
		r = x / y
	case "^":
		r = math.Pow(x, y)
	default:
		return calcValue{}, fmt.Errorf("unknown operator %q", op)
	}
	switch {
	case math.IsNaN(r):
		return calcValue{}, fmt.Errorf("%s %s %s is not a real number", a, b, op)
	case math.IsInf(r, 0):
		return calcValue{}, fmt.Errorf("%s %s %s is out of range", a, b, op)
	}
	return calcFloat(r), nil
}

// calcRun applies a line of input to stack: numbers are pushed and
// operators applied, in order, separated by spaces. It works on a copy, so a
// line with an error changes nothing.
func calcRun(stack []calcValue, input string) ([]calcValue, error) {
	out := append([]calcValue(nil), stack...)
	for _, tok := range strings.Fields(input) {
		if word := strings.ToLower(tok); word == "sum" || word == "avg" {
			r, err := calcTotal(out, word == "avg")
			if err != nil {
				return stack, err
			}
			out = []calcValue{r}
			continue
		}
		if len(tok) == 1 && strings.Contains(calcOps, tok) {
			if len(out) < 2 {
				return stack, fmt.Errorf("%s needs two numbers on the stack", tok)
			}
			r, err := applyCalcOp(tok, out[len(out)-2], out[len(out)-1])
			if err != nil {
				return stack, err
			}
			out = append(out[:len(out)-2], r)
			continue
		}
		v, ok := parseCalcNumber(tok)
		if !ok {
			return stack, fmt.Errorf("%q is not a number (decimal, or hex like 0x1F), + - * / ^, sum or avg", tok)
		}
		out = append(out, v)
	}
	return out, nil
}

// calcTotal is the sum of every number on stack, or with avg their mean,
// kept exact as + and / keep it.
func calcTotal(stack []calcValue, avg bool) (calcValue, error) {
	if len(stack) == 0 {
		return calcValue{}, errors.New("the stack is empty")
	}
	total := stack[0]
	for _, v := range stack[1:] {
		var err error
		if total, err = applyCalcOp("+", total, v); err != nil {
			return calcValue{}, err
		}
	}
	if avg {
		return applyCalcOp("/", total, calcInt(big.NewInt(int64(len(stack)))))
	}
	return total, nil
}

// calcState is one session's calculator: its mode, a stack for each mode,
// the input left in the field when a line could not be used, and the last
// key's outcome.
type calcState struct {
	// dbm is set in dBm mode, which works in powers and gains rather than
	// plain numbers. Each mode has its own stack.
	dbm      bool
	stack    []calcValue
	dbmStack []dbmValue

	input   string
	message string
	isError bool

	// logOff is set for a restricted user, for whom leaving the
	// calculator logs off, as its help row says.
	logOff bool
}

// calcInputField names the calculator's input field.
const calcInputField = "input"

// handle acts on a key pressed on the calculator, returning whether to leave
// it. Enter runs the line typed; PF4, PF5 and PF6 run it and then drop the
// top value, swap the top two, or clear the stack. PF9 switches between
// plain numbers and dBm, keeping what was typed. A line with an error is
// left in the field to be fixed, and the stack is not changed.
func (c *calcState) handle(resp go3270.Response) (leave bool) {
	c.message, c.isError = "", false
	input := resp.Values[calcInputField]
	switch resp.AID {
	case go3270.AIDPF3:
		c.input = ""
		return true
	case go3270.AIDPF9:
		c.dbm = !c.dbm
		c.input = input
		return false
	case go3270.AIDEnter, go3270.AIDPF4, go3270.AIDPF5, go3270.AIDPF6:
	default:
		c.input = input // redraw, keeping what was typed
		return false
	}

	var err error
	if c.dbm {
		var stack []dbmValue
		if stack, err = dbmRun(c.dbmStack, input); err == nil {
			c.dbmStack, c.message = stackKey(stack, resp.AID)
		}
	} else {
		var stack []calcValue
		if stack, err = calcRun(c.stack, input); err == nil {
			c.stack, c.message = stackKey(stack, resp.AID)
		}
	}
	if err != nil {
		c.input, c.message, c.isError = input, err.Error(), true
		return false
	}
	c.input, c.isError = "", c.message != ""
	return false
}

// stackKey does what aid does to a stack once the line typed has been run:
// PF4 drops the top value, PF5 swaps the top two, PF6 clears it. It returns
// the stack, and why the key could not be done if it could not.
func stackKey[T any](stack []T, aid go3270.AID) ([]T, string) {
	n := len(stack)
	switch aid {
	case go3270.AIDPF4:
		if n == 0 {
			return stack, "The stack is empty."
		}
		return stack[:n-1], ""
	case go3270.AIDPF5:
		if n < 2 {
			return stack, "Swapping needs two values on the stack."
		}
		stack[n-1], stack[n-2] = stack[n-2], stack[n-1]
	case go3270.AIDPF6:
		return nil, ""
	}
	return stack, ""
}

// calcMode is how the calculator's current mode is shown: its title, its
// column headings, its stack as rows of two columns (the bottom of the stack
// first), the prompt when nothing has gone wrong, and the help row.
type calcMode struct {
	title, heading1, heading2 string
	stack                     [][2]string
	prompt, help              string
}

// mode describes the current mode.
func (c *calcState) mode() calcMode {
	m := c.modeFor()
	if c.logOff {
		m.help = strings.Replace(m.help, "PF3=Back", "PF3=Log off", 1)
	}
	return m
}

// modeFor describes the current mode, with PF3 going back.
func (c *calcState) modeFor() calcMode {
	if c.dbm {
		m := calcMode{
			title: "dBm CALCULATOR", heading1: "dBm / dB", heading2: "mW",
			prompt: "Values with units (10dBm, 3dB, 5mW) and + - * / ^ sum avg, space-separated.",
			help:   "PF3=Back PF4=Drop PF5=Swap PF6=Clear PF9=Numbers Enter=Run",
		}
		for _, v := range c.dbmStack {
			m.stack = append(m.stack, [2]string{v.logText(), v.linearText()})
		}
		return m
	}
	m := calcMode{
		title: "CALCULATOR", heading1: "Decimal", heading2: "Hex",
		prompt: "Numbers (decimal, or hex like 0x1F) and + - * / ^ sum avg, space-separated.",
		help:   "PF3=Back PF4=Drop PF5=Swap PF6=Clear PF9=dBm Enter=Run",
	}
	for _, v := range c.stack {
		m.stack = append(m.stack, [2]string{v.decimal(), v.hex()})
	}
	return m
}

// Calculator screen layout: the column headings, the stack with level 1 (the
// top) at the bottom, a blank row, the input row, then the message and help
// rows.
const (
	calcHeaderRow = 2
	calcFirstRow  = 3
)

// calcInputRow is the input row on a screen of rows rows.
func calcInputRow(rows int) int { return rows - 3 }

// buildCalc renders the calculator, and where the cursor goes: the start of
// the input field.
func buildCalc(rows, cols int, now time.Time, c *calcState) (screen go3270.Screen, cursorRow, cursorCol int) {
	m := c.mode()
	screen = titleFields(cols, m.title, now)

	// Level, then the first column right aligned, then the second. The two
	// columns split the width right of the level evenly, so the gap between
	// them is centered there.
	col1Width := max((cols-1-5-2)/2, 10)
	format := func(level, col1, col2 string) string {
		return truncate(fmt.Sprintf("%4s %*s  %s", level, col1Width, col1, col2), cols-1)
	}

	// The headings are underlined to the end of the row; the stack rows
	// below always have fields of their own, so the underline stops there.
	heading := format("Lvl", m.heading1, m.heading2)
	screen = append(screen, go3270.Field{
		Row: calcHeaderRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
		Content: heading + strings.Repeat(" ", max(cols-1-len(heading), 0)),
	})

	// Level 1 is on the last stack row, deeper levels above it; when they
	// do not all fit, the first stack row counts the rest.
	lastRow := calcInputRow(rows) - 2
	shown := lastRow - calcFirstRow + 1
	for row := calcFirstRow; row <= lastRow; row++ {
		level := lastRow - row + 1
		f := go3270.Field{Row: row, Col: 0, Color: go3270.Green}
		switch i := len(m.stack) - level; {
		case row == calcFirstRow && len(m.stack) > shown:
			f.Content, f.Color = format("", fmt.Sprintf("(%d more)", len(m.stack)-shown+1), ""), go3270.Blue
		case i >= 0:
			f.Content = format(fmt.Sprintf("%d:", level), m.stack[i][0], m.stack[i][1])
			if level == 1 {
				f.Color, f.Intense = go3270.White, true
			}
		}
		screen = append(screen, f)
	}

	inputRow := calcInputRow(rows)
	screen = append(screen,
		go3270.Field{Row: inputRow, Col: 0, Color: go3270.Turquoise, Content: "===>"},
		go3270.Field{
			Row: inputRow, Col: 5, Write: true, Name: calcInputField, Content: c.input,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the input field at the edge of the screen.
		go3270.Field{Row: inputRow, Col: cols - 1},
	)

	message, color := c.message, go3270.Red
	if !c.isError {
		message, color = m.prompt, go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: c.isError}})...)
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate(m.help, cols-1)})
	return screen, inputRow, 6
}
