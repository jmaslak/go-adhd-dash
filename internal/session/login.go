package session

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// Logins: how many wrong tries a session gets before it is disconnected,
// and how long the login screen waits for them.
const (
	loginTries   = 3
	loginTimeout = 2 * time.Minute
)

// Login screen layout: the banner from loginBannerRow, the prompt, then
// the user name and password.
const (
	loginBannerRow = 2
	loginPromptRow = loginBannerRow + 8
	loginNameRow   = loginPromptRow + 2
	loginPassRow   = loginNameRow + 2
	loginNameLbl   = "User name ===>"
	loginPassLbl   = "Password  ===>"
	loginField     = "user"
	loginPassword  = "password"
	loginWidth     = 30
)

// bannerGlyph is one character of the login banner, drawn in # (which
// every code page has) seven rows high and five wide, each row in a color.
type bannerGlyph struct {
	rows   [7]string
	colors [7]go3270.Color
}

// solid is every row of a glyph in c.
func solid(c go3270.Color) [7]go3270.Color {
	return [7]go3270.Color{c, c, c, c, c, c, c}
}

// rainbow colors a lowercase glyph's five rows in bands, as near a rainbow
// as the 3270's colors come: red, yellow, green, turquoise, blue. (Its top
// two rows are blank.)
var rainbow = [7]go3270.Color{
	go3270.Red, go3270.Red, go3270.Red, go3270.Yellow, go3270.Green, go3270.Turquoise, go3270.Blue,
}

// Glyphs for the banner. Lowercase letters stand on the baseline, two rows
// shorter than the rest.
var (
	glyphE     = [7]string{"     ", "     ", " ### ", "#   #", "#####", "#    ", " ####"}
	glyphX     = [7]string{"     ", "     ", "#   #", " # # ", "  #  ", " # # ", "#   #"}
	glyphC     = [7]string{"     ", "     ", " ####", "#    ", "#    ", "#    ", " ####"}
	glyphSlash = [7]string{"    #", "    #", "   # ", "  #  ", " #   ", "#    ", "#    "}
	glyph3     = [7]string{"#### ", "    #", "    #", " ### ", "    #", "    #", "#### "}
	glyph2     = [7]string{" ### ", "#   #", "    #", "   # ", "  #  ", " #   ", "#####"}
	glyph7     = [7]string{"#####", "    #", "   # ", "  #  ", " #   ", " #   ", " #   "}
	glyph0     = [7]string{" ### ", "#   #", "#  ##", "# # #", "##  #", "#   #", " ### "}
)

// loginBanner is "exec/3270": the name in rainbow bands, the slash in
// white, the number in yellow.
var loginBanner = []bannerGlyph{
	{glyphE, rainbow}, {glyphX, rainbow}, {glyphE, rainbow}, {glyphC, rainbow},
	{glyphSlash, solid(go3270.White)},
	{glyph3, solid(go3270.Yellow)}, {glyph2, solid(go3270.Yellow)}, {glyph7, solid(go3270.Yellow)}, {glyph0, solid(go3270.Yellow)},
}

// bannerGlyphWidth is how many columns a glyph takes: its attribute byte,
// five columns of the glyph, and a space after it.
const bannerGlyphWidth = 7

// bannerLines are the banner's rows, each glyph a field of its own color.
func bannerLines() []line {
	out := make([]line, 7)
	for r := range out {
		for _, g := range loginBanner {
			out[r] = append(out[r], go3270.Field{Content: g.rows[r] + " ", Color: g.colors[r], Intense: true})
		}
	}
	return out
}

// consoleLU is the LU name of the console: the one session that need not
// log in, which must connect from this machine and asks for it by name.
const consoleLU = "CONSOLE"

// consoleBootedFarewell is left on a console's terminal when another
// console connects.
const consoleBootedFarewell = "Another CONSOLE session connected. Goodbye."

// errConsoleRemote refuses the console's LU name to a client not on this
// machine.
var errConsoleRemote = errors.New("the CONSOLE LU is only for connections from this machine")

// chooseLU picks the LU name for session id, whose client asked for
// requested (empty if it named none), local if it connected from this
// machine: CONSOLE (asked for in any case) if it asked for it and is local,
// refused if it asked for it and is not, and otherwise a name of the
// session's own, whatever it asked for.
func chooseLU(requested string, local bool, id uint64) (string, error) {
	if strings.EqualFold(requested, consoleLU) {
		if !local {
			return "", errConsoleRemote
		}
		return consoleLU, nil
	}
	return fmt.Sprintf("AD%06X", id&0xFFFFFF), nil
}

// isLocal reports whether addr is this machine's loopback address,
// 127.0.0.1 or ::1 (or 127.0.0.1 mapped into IPv6), the only place the
// console may connect from.
func isLocal(addr net.Addr) bool {
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host, _, _ = strings.Cut(host, "%") // an IPv6 zone
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

// loginState is one session's login screen.
type loginState struct {
	name     string // as typed, drawn again after a wrong try
	failures int
	expires  time.Time // when the screen gives up waiting

	message string
}

// buildLogin renders the login screen for a session with LU name lu (empty
// for a client without TN3270E), and where the cursor goes: the user name,
// or after a wrong try with one typed, the password.
func buildLogin(rows, cols int, now time.Time, lu string, l *loginState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "LOGIN", now, false)
	bannerCol := max((cols-len(loginBanner)*bannerGlyphWidth)/2, 0)
	for i, l := range bannerLines() {
		screen = append(screen, placeLineAt(loginBannerRow+i, bannerCol, cols, l)...)
	}
	screen = append(screen, placeLine(loginPromptRow, cols, line{{Content: "Log in to continue.", Color: go3270.White, Intense: true}})...)

	fieldCol := len(loginNameLbl) + 1
	end := min(fieldCol+1+loginWidth, cols-1)
	screen = append(screen,
		go3270.Field{Row: loginNameRow, Col: 0, Color: go3270.Turquoise, Content: loginNameLbl},
		go3270.Field{
			Row: loginNameRow, Col: fieldCol, Write: true, Name: loginField, Content: cutRunes(l.name, loginWidth),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: loginNameRow, Col: end},
		go3270.Field{Row: loginPassRow, Col: 0, Color: go3270.Turquoise, Content: loginPassLbl},
		go3270.Field{
			Row: loginPassRow, Col: fieldCol, Write: true, Hidden: true, Name: loginPassword,
			Color: go3270.Yellow, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: loginPassRow, Col: end},
	)

	message, color := l.message, go3270.Red
	if message == "" {
		message, color = "Type your user name and password, then press Enter.", go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: l.message != ""}})...)
	help := "PF3=Disconnect Enter=Log in"
	if lu != "" {
		help += "   LU " + lu
	}
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)})

	if l.name != "" {
		return screen, loginPassRow, fieldCol + 1
	}
	return screen, loginNameRow, fieldCol + 1
}

// handle acts on a key pressed on the login screen, returning the user
// logged in as, if any, and whether to disconnect: on PF3, or after
// loginTries wrong tries, with farewell saying why.
func (l *loginState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (user *users.User, quit bool, farewell string) {
	l.message = ""
	switch resp.AID {
	case go3270.AIDPF3:
		return nil, true, "Goodbye."
	case go3270.AIDEnter:
	default:
		return nil, false, ""
	}
	l.name = strings.TrimSpace(resp.Values[loginField])
	password := resp.Values[loginPassword]
	if l.name == "" || password == "" {
		l.message = "Type both your user name and your password."
		return nil, false, ""
	}
	if store == nil {
		logf("login as %q refused: no user database", l.name)
		return nil, true, "Logins are not available."
	}
	u, ok, err := store.Authenticate(l.name, password)
	switch {
	case err != nil:
		logf("login as %q: %v", l.name, err)
		l.message = "Could not check the password: " + err.Error()
		return nil, false, ""
	case ok:
		logf("logged in as %q", u.Name)
		return &u, false, ""
	}
	l.failures++
	logf("login as %q failed (%d of %d)", l.name, l.failures, loginTries)
	if l.failures >= loginTries {
		return nil, true, "Too many failed logins. Goodbye."
	}
	l.message = "Wrong user name or password."
	return nil, false, ""
}

// buildFarewell is the screen left on a terminal as it is disconnected,
// saying why.
func buildFarewell(cols int, now time.Time, text string) go3270.Screen {
	screen := titleFields(cols, "EXECUTIVE FUNCTION DASHBOARD", now, false)
	return append(screen, placeLine(2, cols, line{{Content: text, Color: go3270.Yellow, Intense: true}})...)
}

// auditName is how the audit log names user: by name, or for the console,
// which does not log in, as (console).
func auditName(user *users.User) string {
	if user == nil {
		return "(console)"
	}
	return user.Name
}
