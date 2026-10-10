// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/web3270"
)

// Web3270Control is web3270, the web terminal, which the admin enables
// and disables (see web3270.Supervisor).
type Web3270Control interface {
	Status() web3270.Status
	SetEnabled(on bool) error
	// URL is where browsers reach it at host, "" while it is not running.
	URL(host string) string
}

// The admin's screen for the web terminal.
const (
	webTermField = "webterm"
	webTermLabel = "Enabled (Y/N) ===>"
)

// webTermText explains the screen.
var webTermText = []string{
	"web3270 is a TN3270 terminal in the browser. Enabled, while there is a TLS",
	"domain (option 9), it runs locked to this server (kiosk mode), and browsers",
	"reach it over HTTPS on its own port. It is restarted when the domain changes.",
	"install-ubuntu.sh installs it.",
}

// webTermState is one session's place on the web terminal screen.
type webTermState struct {
	typed      *string
	leaveArmed bool

	message string
	isError bool
}

// buildWebTerm renders the admin's screen for the web terminal.
func buildWebTerm(rows, cols int, now time.Time, ctl Web3270Control, s *webTermState) (screen go3270.Screen, cursorRow, cursorCol int) {
	st := ctl.Status()
	screen = titleFields(cols, "WEB TERMINAL", now)
	header := line{{Content: "Web terminal", Color: go3270.Turquoise, Intense: true}}
	switch {
	case st.Running:
		header = append(header, go3270.Field{Content: "running for " + st.Domain, Color: go3270.Blue})
	case !st.Enabled:
		header = append(header, go3270.Field{Content: "disabled", Color: go3270.Yellow})
	default:
		header = append(header, go3270.Field{Content: "enabled, not running", Color: go3270.Red, Intense: true})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	for i, t := range webTermText {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t, Color: go3270.Green}})...)
	}
	row := 5 + len(webTermText)
	program := line{{Content: "Program:", Color: go3270.Turquoise}}
	if st.Bin == "" {
		program = append(program, go3270.Field{Content: "not installed", Color: go3270.Yellow})
	} else {
		program = append(program, go3270.Field{Content: st.Bin, Color: go3270.White})
	}
	screen = append(screen, placeLine(row, cols, program)...)
	row++
	switch {
	case st.Running:
		screen = append(screen, placeLine(row, cols, line{
			{Content: "Address:", Color: go3270.Turquoise},
			{Content: ctl.URL(firstDomain(st.Domain)), Color: go3270.White, Intense: true},
		})...)
	case st.Problem != "":
		screen = append(screen, placeLine(row, cols, line{
			{Content: "Not running:", Color: go3270.Turquoise},
			{Content: st.Problem, Color: go3270.Red},
		})...)
	}

	value := "N"
	if st.Enabled {
		value = "Y"
	}
	if s.typed != nil {
		value = *s.typed
	}
	row += 2
	col := len(webTermLabel) + 1
	screen = append(screen,
		go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: webTermLabel},
		go3270.Field{
			Row: row, Col: col, Write: true, Name: webTermField, Content: cutRunes(value, 3),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the field after room for YES.
		go3270.Field{Row: row, Col: col + 4},
	)
	screen = appendMessageRows(screen, rows, cols, s.message, s.isError,
		"Type Y or N, and press Enter.", "PF3=Back Enter=Save")
	return screen, row, col + 1
}

// firstDomain is the first of a comma-separated list of domain names.
func firstDomain(domains string) string {
	first, _, _ := strings.Cut(domains, ",")
	return strings.TrimSpace(first)
}

// handle acts on a key on the web terminal screen, returning whether to
// leave for the admin menu.
func (s *webTermState) handle(resp go3270.Response, ctl Web3270Control, logf func(string, ...any)) (leave bool) {
	s.message, s.isError = "", false
	armed := s.leaveArmed
	s.leaveArmed = false
	raw := resp.Values[webTermField]
	typed := strings.ToUpper(strings.TrimSpace(raw))
	enabled := ctl.Status().Enabled
	current := "N"
	if enabled {
		current = "Y"
	}
	s.typed = &raw
	switch resp.AID {
	case go3270.AIDPF3:
		if typed != current && !armed {
			s.leaveArmed = true
			s.message, s.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}
	var on bool
	switch typed {
	case "Y", "YES":
		on = true
	case "N", "NO":
	default:
		s.message, s.isError = "Type Y or N.", true
		return false
	}
	if on == enabled {
		s.message, s.typed = "Nothing changed.", nil
		return false
	}
	if err := ctl.SetEnabled(on); err != nil {
		s.message, s.isError = "Could not save: "+err.Error(), true
		return false
	}
	s.typed = nil
	if on {
		logf("enabled the web terminal")
		if st := ctl.Status(); !st.Running && st.Problem != "" {
			s.message, s.isError = "Enabled, but not running: "+st.Problem, true
		} else {
			s.message = "Enabled the web terminal."
		}
		return false
	}
	logf("disabled the web terminal")
	s.message = "Disabled the web terminal."
	return false
}
