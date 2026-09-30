package session

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// dbmKind is what a value on the dBm calculator's stack measures.
type dbmKind int

const (
	// dbmPower is an absolute power, entered in dBm or mW and kept in mW.
	dbmPower dbmKind = iota
	// dbmGain is a gain or loss, entered and kept in dB.
	dbmGain
	// dbmNumber is a plain number, for scaling a power or a gain.
	dbmNumber
)

// dbmValue is one value on the dBm calculator's stack: mW for a power, dB
// for a gain, or a plain number.
type dbmValue struct {
	kind dbmKind
	v    float64
}

func dbmFromMW(mw float64) dbmValue    { return dbmValue{dbmPower, mw} }
func dbmFromDBm(d float64) dbmValue    { return dbmFromMW(math.Pow(10, d/10)) }
func dbmFromDB(db float64) dbmValue    { return dbmValue{dbmGain, db} }
func dbmFromNumber(n float64) dbmValue { return dbmValue{dbmNumber, n} }

// dbm is a power in dBm.
func (p dbmValue) dbm() float64 { return 10 * math.Log10(p.v) }

// ratio is a gain as a power ratio.
func (g dbmValue) ratio() float64 { return math.Pow(10, g.v/10) }

// formatLog renders a dB or dBm figure to four decimal places, dropping
// trailing zeros.
func formatLog(x float64) string {
	s := strconv.FormatFloat(x, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		s = "0"
	}
	return s
}

// formatLinear renders a mW figure or a ratio to six significant digits.
func formatLinear(x float64) string {
	return strconv.FormatFloat(x, 'g', 6, 64)
}

// logText is v's first display column: a power in dBm, a gain in dB, or
// the plain number.
func (v dbmValue) logText() string {
	switch v.kind {
	case dbmPower:
		return formatLog(v.dbm()) + " dBm"
	case dbmGain:
		return formatLog(v.v) + " dB"
	default:
		return strconv.FormatFloat(v.v, 'g', -1, 64)
	}
}

// linearText is v's second display column: a power in mW, a gain as the
// ratio it multiplies by, and nothing for a plain number.
func (v dbmValue) linearText() string {
	switch v.kind {
	case dbmPower:
		return formatLinear(v.v) + " mW"
	case dbmGain:
		return "x" + formatLinear(v.ratio())
	default:
		return ""
	}
}

// dbmToken matches a number with an optional unit. The units are dBm, dB
// and mW; the B and the W may be either case.
var dbmToken = regexp.MustCompile(`^([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)(d[bB]m|d[bB]|m[wW])?$`)

// parseDBMValue reads a value typed on the dBm calculator.
func parseDBMValue(tok string) (dbmValue, error) {
	m := dbmToken.FindStringSubmatch(tok)
	if m == nil {
		return dbmValue{}, fmt.Errorf("%q is not a value: use a dBm, dB or mW suffix, or + - * / ^ sum avg", tok)
	}
	x, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsInf(x, 0) {
		return dbmValue{}, fmt.Errorf("%q is out of range", tok)
	}
	switch strings.ToLower(m[2]) {
	case "dbm":
		p := dbmFromDBm(x)
		if p.v == 0 || math.IsInf(p.v, 0) {
			return dbmValue{}, fmt.Errorf("%q is out of range", tok)
		}
		return p, nil
	case "db":
		return dbmFromDB(x), nil
	case "mw":
		if x <= 0 {
			return dbmValue{}, fmt.Errorf("%s: a power in mW must be more than zero", tok)
		}
		return dbmFromMW(x), nil
	default:
		return dbmFromNumber(x), nil
	}
}

// errDBMUnits reports an operator given values it has no meaning for.
func errDBMUnits(op string, a, b dbmValue) error {
	return fmt.Errorf("%s has no meaning for %s and %s", op, a.logText(), b.logText())
}

// applyDBMOp works out a op b by what the values measure:
//
//   - power + power combines them (in mW); power + dB, or dB + power,
//     is a gain; dB + dB adds the gains
//   - power - power takes one from the other (in mW); power - dB is a
//     loss; dB - dB subtracts the gains
//   - scales a power (in mW) or a gain (in dB) by a plain number
//     /  power / number and dB / number scale down; power / power is their
//     ratio, in dB
//     ^  raises a power (in mW) to a plain number
//
// Plain numbers combine with each other under * / ^ as ordinary numbers.
func applyDBMOp(op string, a, b dbmValue) (dbmValue, error) {
	pair := [2]dbmKind{a.kind, b.kind}
	var r dbmValue
	switch op {
	case "+":
		switch pair {
		case [2]dbmKind{dbmPower, dbmPower}:
			r = dbmFromMW(a.v + b.v)
		case [2]dbmKind{dbmPower, dbmGain}:
			r = dbmFromDBm(a.dbm() + b.v)
		case [2]dbmKind{dbmGain, dbmPower}:
			r = dbmFromDBm(b.dbm() + a.v)
		case [2]dbmKind{dbmGain, dbmGain}:
			r = dbmFromDB(a.v + b.v)
		default:
			return dbmValue{}, fmt.Errorf("+ needs values with units: add a dBm, dB or mW suffix")
		}
	case "-":
		switch pair {
		case [2]dbmKind{dbmPower, dbmPower}:
			if a.v <= b.v {
				return dbmValue{}, fmt.Errorf("%s - %s leaves no power", a.logText(), b.logText())
			}
			r = dbmFromMW(a.v - b.v)
		case [2]dbmKind{dbmPower, dbmGain}:
			r = dbmFromDBm(a.dbm() - b.v)
		case [2]dbmKind{dbmGain, dbmGain}:
			r = dbmFromDB(a.v - b.v)
		case [2]dbmKind{dbmGain, dbmPower}:
			return dbmValue{}, errDBMUnits(op, a, b)
		default:
			return dbmValue{}, fmt.Errorf("- needs values with units: add a dBm, dB or mW suffix")
		}
	case "*":
		switch pair {
		case [2]dbmKind{dbmPower, dbmNumber}, [2]dbmKind{dbmNumber, dbmPower}:
			r = dbmFromMW(a.v * b.v)
		case [2]dbmKind{dbmGain, dbmNumber}, [2]dbmKind{dbmNumber, dbmGain}:
			r = dbmFromDB(a.v * b.v)
		case [2]dbmKind{dbmNumber, dbmNumber}:
			r = dbmFromNumber(a.v * b.v)
		default:
			return dbmValue{}, errDBMUnits(op, a, b)
		}
	case "/":
		if b.v == 0 {
			return dbmValue{}, errors.New("division by zero")
		}
		switch pair {
		case [2]dbmKind{dbmPower, dbmNumber}:
			r = dbmFromMW(a.v / b.v)
		case [2]dbmKind{dbmPower, dbmPower}:
			r = dbmFromDB(a.dbm() - b.dbm())
		case [2]dbmKind{dbmGain, dbmNumber}:
			r = dbmFromDB(a.v / b.v)
		case [2]dbmKind{dbmNumber, dbmNumber}:
			r = dbmFromNumber(a.v / b.v)
		default:
			return dbmValue{}, errDBMUnits(op, a, b)
		}
	case "^":
		switch pair {
		case [2]dbmKind{dbmPower, dbmNumber}:
			r = dbmFromMW(math.Pow(a.v, b.v))
		case [2]dbmKind{dbmNumber, dbmNumber}:
			r = dbmFromNumber(math.Pow(a.v, b.v))
		default:
			return dbmValue{}, errDBMUnits(op, a, b)
		}
	default:
		return dbmValue{}, fmt.Errorf("unknown operator %q", op)
	}

	switch {
	case math.IsNaN(r.v) || math.IsInf(r.v, 0):
		return dbmValue{}, fmt.Errorf("%s %s %s is out of range", a.logText(), b.logText(), op)
	case r.kind == dbmPower && r.v <= 0:
		return dbmValue{}, fmt.Errorf("%s %s %s is not a power", a.logText(), b.logText(), op)
	}
	return r, nil
}

// dbmTotal is the sum of every value on stack, or with avg their mean. The
// values must all be of one kind: powers add in mW, so their mean is the
// average power; gains add in dB; plain numbers as numbers.
func dbmTotal(stack []dbmValue, avg bool) (dbmValue, error) {
	if len(stack) == 0 {
		return dbmValue{}, errors.New("the stack is empty")
	}
	total := stack[0]
	for _, v := range stack[1:] {
		if v.kind != total.kind {
			return dbmValue{}, errors.New("sum and avg need values all of one kind: all powers, all dB, or all plain numbers")
		}
		total.v += v.v
	}
	if avg {
		total.v /= float64(len(stack))
	}
	if math.IsInf(total.v, 0) {
		return dbmValue{}, errors.New("the total is out of range")
	}
	return total, nil
}

// dbmRun applies a line of input to stack, as calcRun does for plain
// numbers. It works on a copy, so a line with an error changes nothing.
func dbmRun(stack []dbmValue, input string) ([]dbmValue, error) {
	out := append([]dbmValue(nil), stack...)
	for _, tok := range strings.Fields(input) {
		if word := strings.ToLower(tok); word == "sum" || word == "avg" {
			r, err := dbmTotal(out, word == "avg")
			if err != nil {
				return stack, err
			}
			out = []dbmValue{r}
			continue
		}
		if len(tok) == 1 && strings.Contains(calcOps, tok) {
			if len(out) < 2 {
				return stack, fmt.Errorf("%s needs two values on the stack", tok)
			}
			r, err := applyDBMOp(tok, out[len(out)-2], out[len(out)-1])
			if err != nil {
				return stack, err
			}
			out = append(out[:len(out)-2], r)
			continue
		}
		v, err := parseDBMValue(tok)
		if err != nil {
			return stack, err
		}
		out = append(out, v)
	}
	return out, nil
}
