package bigtext

import (
	"strings"
	"testing"
)

// TestFont checks every glyph's shape: Height rows of Width columns, of #
// and spaces only, with ink in it (but the space), and lowercase letters
// on the capitals' baseline, below their top row unless they have an
// ascender.
func TestFont(t *testing.T) {
	for r, g := range font {
		ink := false
		for i, row := range g {
			if len(row) != Width || strings.Trim(row, "# ") != "" {
				t.Errorf("%q row %d is %q", r, i, row)
			}
			ink = ink || strings.Contains(row, "#")
		}
		if ink != (r != ' ') {
			t.Errorf("%q: ink %v", r, ink)
		}
		if strings.TrimSpace(g[Height-1]) == "" && !strings.ContainsRune(" :'-", r) {
			t.Errorf("%q does not reach the baseline", r)
		}
	}
	for r := 'A'; r <= 'Z'; r++ {
		if _, ok := Lookup(r); !ok {
			t.Errorf("no %q", r)
		}
		if _, ok := Lookup(r - 'A' + 'a'); !ok {
			t.Errorf("no %q", r-'A'+'a')
		}
	}
	for r := '0'; r <= '9'; r++ {
		if _, ok := Lookup(r); !ok {
			t.Errorf("no %q", r)
		}
	}
	for _, r := range "acemnorsuvwxz" { // no ascender
		if g, _ := Lookup(r); strings.TrimSpace(g[0]) != "" {
			t.Errorf("%q reaches the top row", r)
		}
	}
}

func TestRows(t *testing.T) {
	rows := Rows("1:0")
	one, _ := Lookup('1')
	colon, _ := Lookup(':')
	zero, _ := Lookup('0')
	for i := range Height {
		if want := one[i] + " " + colon[i] + " " + zero[i]; rows[i] != want {
			t.Errorf("Rows(1:0) row %d is %q, want %q", i, rows[i], want)
		}
	}
	for i, row := range rows {
		if len(row) != TextWidth("1:0") {
			t.Errorf("row %d is %d wide, want %d", i, len(row), TextWidth("1:0"))
		}
	}
	if TextWidth("") != 0 || TextWidth("A") != Width || TextWidth("AB") != 2*Width+1 {
		t.Errorf("widths %d %d %d", TextWidth(""), TextWidth("A"), TextWidth("AB"))
	}
	// Unknown characters are blank, as wide as any.
	a, _ := Lookup('A')
	b, _ := Lookup('B')
	if got := Rows("A~B")[0]; got != a[0]+" "+blank[0]+" "+b[0] {
		t.Errorf("unknown character row %q", got)
	}
	if !Supports("DONE 12:30") || Supports("A~") || !Supports("") {
		t.Error("Supports wrong")
	}
	if got := Rows(""); got != ([Height]string{}) {
		t.Errorf("empty text: %q", got)
	}
}
