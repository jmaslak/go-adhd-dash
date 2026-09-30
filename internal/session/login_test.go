package session

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestIsLocal(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":        true,
		"[::1]:5000":            true,
		"[::ffff:127.0.0.1]:50": true,
		"[::1%lo0]:5000":        true,
		"127.0.0.2:5000":        false,
		"10.0.0.1:5000":         false,
		"[fe80::1]:5000":        false,
		"192.168.1.5:3270":      false,
	} {
		host, port, _ := net.SplitHostPort(addr)
		a := fakeAddr(net.JoinHostPort(host, port))
		if got := isLocal(a); got != want {
			t.Errorf("%s: isLocal %v, want %v", addr, got, want)
		}
	}
}

// fakeAddr is an address as a connection reports it.
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func TestLoginScreen(t *testing.T) {
	var l loginState
	s, row, col := buildLogin(24, 80, now, &l)
	rows := screenText(t, s, 24, 80)
	if !strings.HasPrefix(rows[titleRow], " LOGIN ") || strings.Contains(rows[titleRow], "DASHBOARD") {
		t.Errorf("title row %q", rows[titleRow])
	}
	for i := range 7 {
		var want []string
		for _, g := range loginBanner {
			want = append(want, g.rows[i])
		}
		if got := strings.TrimSpace(rows[loginBannerRow+i]); got != strings.TrimSpace(strings.Join(want, "  ")) {
			t.Errorf("banner row %d is %q", i, got)
		}
	}
	// The bottom row starts with the first glyph's first column, at the
	// banner's left edge: centered, it is 9 or 10 columns in.
	if lead := len(rows[loginBannerRow+6]) - len(strings.TrimLeft(rows[loginBannerRow+6], " ")); lead < 9 || lead > 11 {
		t.Errorf("banner starts at column %d, not centered", lead)
	}
	colors := map[go3270.Color]int{}
	for _, f := range s {
		if f.Row >= loginBannerRow && f.Row < loginBannerRow+7 {
			colors[f.Color]++
		}
	}
	if colors[go3270.Turquoise] != 4*7 || colors[go3270.White] != 7 || colors[go3270.Yellow] != 4*7 {
		t.Errorf("banner fields by color %v; want exec turquoise, / white, 3270 yellow", colors)
	}
	if !strings.Contains(rows[loginPromptRow], "Log in to continue.") || !strings.HasPrefix(rows[loginNameRow], " User name ===>") || !strings.HasPrefix(rows[loginPassRow], " Password  ===>") {
		t.Errorf("login screen:\n%s", strings.Join(rows, "\n"))
	}
	if row != loginNameRow || col != len(loginNameLbl)+2 {
		t.Errorf("cursor at %d,%d, want the user name", row, col)
	}
	for _, f := range s {
		if f.Name == loginPassword && !f.Hidden {
			t.Error("password shown as typed")
		}
	}

	// After a wrong try, the name is kept and the cursor goes to the
	// password.
	l.name, l.message = "joelle", "Wrong user name or password."
	s, row, _ = buildLogin(24, 80, now, &l)
	if row != loginPassRow || !strings.Contains(strings.Join(screenText(t, s, 24, 80), "\n"), "Wrong user name or password.") {
		t.Errorf("after a wrong try: cursor row %d", row)
	}
}

func TestLoginHandle(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	enter := func(l *loginState, name, password string) (*users.User, bool, string) {
		return l.handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{loginField: name, loginPassword: password}}, store, noLog)
	}

	var l loginState
	if u, quit, _ := enter(&l, "admin", ""); u != nil || quit || l.message != "Type both your user name and your password." {
		t.Errorf("no password: %v %v %q", u, quit, l.message)
	}
	if u, quit, _ := enter(&l, " Admin ", "admin"); u == nil || quit || u.Name != "admin" || !u.Admin {
		t.Errorf("right password: %+v %v %q", u, quit, l.message)
	}

	l = loginState{}
	for i := 1; i < loginTries; i++ {
		if u, quit, _ := enter(&l, "admin", "wrong"); u != nil || quit || l.message != "Wrong user name or password." || l.name != "admin" {
			t.Fatalf("wrong try %d: %v %v %q", i, u, quit, l.message)
		}
	}
	if u, quit, farewell := enter(&l, "nobody", "admin"); u != nil || !quit || !strings.Contains(farewell, "Too many failed logins") {
		t.Errorf("last wrong try: %v %v %q", u, quit, farewell)
	}

	if _, quit, _ := (&loginState{}).handle(go3270.Response{AID: go3270.AIDPF3}, store, noLog); !quit {
		t.Error("PF3 does not disconnect")
	}
	if u, quit, _ := (&loginState{}).handle(go3270.Response{AID: go3270.AIDPF8}, store, noLog); u != nil || quit {
		t.Error("PF8 did something")
	}
	if u, quit, farewell := (&loginState{}).handle(go3270.Response{AID: go3270.AIDEnter, Values: map[string]string{loginField: "a", loginPassword: "b"}}, nil, noLog); u != nil || !quit || farewell != "Logins are not available." {
		t.Errorf("no user database: %v %v %q", u, quit, farewell)
	}
}

func TestChooseLU(t *testing.T) {
	for _, c := range []struct {
		requested string
		local     bool
		want      string
		refused   bool
	}{
		{"", true, "AD00002A", false},
		{"", false, "AD00002A", false},
		{"CONSOLE", true, "CONSOLE", false},
		{"console", true, "CONSOLE", false},
		{"CONSOLE", false, "", true},
		{"LU0001", true, "AD00002A", false},
		{"LU0001", false, "AD00002A", false},
	} {
		got, err := chooseLU(c.requested, c.local, 42)
		if got != c.want || (err != nil) != c.refused {
			t.Errorf("%q local %v: %q, %v", c.requested, c.local, got, err)
		}
	}
}
