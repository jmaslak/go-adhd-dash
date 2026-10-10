// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/certs"
)

// TLSControl is the server's TLS, which the admin turns on by setting its
// domain, and off by blanking it (see certs.Service).
type TLSControl interface {
	// Domain is the domain names TLS is on for, comma-separated; "" when
	// it is off.
	Domain() string
	// SetDomain turns TLS on for the domain names typed, or with none,
	// off, keeping them, and returns them as kept.
	SetDomain(typed string) (string, error)
	// File is where the domain is kept.
	File() string
	// HTTPSListen is where the web pages are served over HTTPS; "" when
	// they are not.
	HTTPSListen() string
	// Certificates are what is kept for each domain.
	Certificates() []certs.Status
	// Retry asks the certificate authority again, at once, for any
	// certificate that could not be got.
	Retry()
}

// webListen is where the web pages are served, over HTTP and HTTPS,
// comma-separated; "" when they are not.
func (cfg Config) webListen() string {
	served := []string{}
	if cfg.HTTPListen != "" {
		served = append(served, cfg.HTTPListen)
	}
	if cfg.TLS != nil {
		if h := cfg.TLS.HTTPSListen(); h != "" {
			served = append(served, h)
		}
	}
	return strings.Join(served, ", ")
}

// The admin's screen for the TLS domain.
const (
	tlsDomainField = "tlsdomain"
	tlsDomainLabel = "Domain ===>"
)

// tlsText explains the screen.
var tlsText = []string{
	"The domain name this server is reached at, such as adhd.example.com. With",
	"one, it keeps a Let's Encrypt certificate for it, TN3270 connections must",
	"use TLS, and the web pages are served over HTTPS. The name must resolve to",
	"this machine, and Let's Encrypt must reach it on port 443 or 80. Blank it",
	"to turn TLS off.",
}

// tlsState is one session's place on the TLS screen.
type tlsState struct {
	// confirming shows the confirmation for turning TLS on or off, which
	// changes how every terminal connects; pending is what to set.
	confirming bool
	pending    string

	typed      *string
	leaveArmed bool

	message string
	isError bool
}

// buildTLS renders the admin's screen for the TLS domain, or the
// confirmation for turning TLS on or off.
func buildTLS(rows, cols int, now time.Time, ctl TLSControl, s *tlsState) (screen go3270.Screen, cursorRow, cursorCol int) {
	if s.confirming {
		return buildTLSConfirm(rows, cols, now, s.pending, ctl.File()), rows - 1, 0
	}
	current := ctl.Domain()
	screen = titleFields(cols, "TLS", now)
	header := line{{Content: "TLS", Color: go3270.Turquoise, Intense: true}}
	if current == "" {
		header = append(header, go3270.Field{Content: "off: terminals and web pages unencrypted", Color: go3270.Yellow})
	} else {
		header = append(header, go3270.Field{Content: "on for " + current, Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	for i, t := range tlsText {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t, Color: go3270.Green}})...)
	}
	row := 5 + len(tlsText)
	screen = append(screen, placeLine(row, cols, line{
		{Content: "Kept in", Color: go3270.Blue},
		{Content: ctl.File(), Color: go3270.White},
	})...)

	value := current
	if s.typed != nil {
		value = *s.typed
	}
	row += 2
	col := len(tlsDomainLabel) + 1
	fieldRow := row
	screen = append(screen,
		go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: tlsDomainLabel},
		go3270.Field{
			Row: row, Col: col, Write: true, Name: tlsDomainField, Content: cutRunes(value, cols-col-2),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: row, Col: cols - 1},
	)

	help := "PF3=Back Enter=Save"
	if current != "" {
		row += 2
		for _, c := range ctl.Certificates() {
			l := line{{Content: "Certificate for " + c.Domain + ":", Color: go3270.Turquoise}}
			if c.Failed {
				help = "PF3=Back Enter=Save PF5=Retry"
				l = append(l, go3270.Field{Content: "none: " + c.Err.Error(), Color: go3270.Red})
			} else if c.Err != nil {
				l = append(l, go3270.Field{Content: "none yet (" + c.Err.Error() + ")", Color: go3270.Yellow})
			} else {
				color := go3270.White
				if c.NotAfter.Sub(now) < 14*24*time.Hour {
					color = go3270.Red
				}
				l = append(l, go3270.Field{Content: "expires " + c.NotAfter.In(now.Location()).Format(time.DateTime), Color: color})
			}
			if row < rows-3 {
				screen = append(screen, placeLine(row, cols, l)...)
			}
			row++
		}
		https := ctl.HTTPSListen()
		if https == "" {
			https = "not served (-https-port 0)"
		}
		if row < rows-3 {
			screen = append(screen, placeLine(row, cols, line{
				{Content: "Web pages over HTTPS:", Color: go3270.Turquoise},
				{Content: https, Color: go3270.White},
			})...)
		}
	}

	screen = appendMessageRows(screen, rows, cols, s.message, s.isError,
		"Type the domain name, and press Enter.", help)
	return screen, fieldRow, col + 1
}

// buildTLSConfirm renders the confirmation for turning TLS on for domain,
// or with "", off; file is where the domain is kept.
func buildTLSConfirm(rows, cols int, now time.Time, domain, file string) go3270.Screen {
	screen := titleFields(cols, "TLS", now)
	var question string
	var text []string
	if domain == "" {
		question = "Turn TLS off?"
		text = []string{
			"New TN3270 connections are then unencrypted, and terminals set to use TLS",
			"cannot connect. The web pages are no longer served over HTTPS. Sessions",
			"connected now, this one included, stay as they are.",
		}
	} else {
		question = "Turn TLS on for " + domain + "?"
		text = []string{
			"New TN3270 connections must then use TLS (with x3270, L:host:port), and",
			"the web pages are served over HTTPS. Sessions connected now, this one",
			"included, stay as they are. Until a certificate is got for the name, no",
			"terminal can connect; if none can be, turn TLS off by removing",
			"  " + file,
			"and restarting the server.",
		}
	}
	screen = append(screen, placeLine(2, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)
	for i, t := range text {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t}})...)
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to go ahead, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	key := "PF4=Turn on"
	if domain == "" {
		key = "PF4=Turn off"
	}
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back "+key, cols-1)})
}

// handle acts on a key on the TLS screen, returning whether to leave for
// the admin menu: Enter sets the domain typed, asking first when that
// turns TLS on or off, and PF3 leaves, a second time with something typed.
func (s *tlsState) handle(resp go3270.Response, ctl TLSControl, logf func(string, ...any)) (leave bool) {
	s.message, s.isError = "", false
	armed := s.leaveArmed
	s.leaveArmed = false
	if s.confirming {
		switch resp.AID {
		case go3270.AIDPF3:
			s.confirming = false
		case go3270.AIDPF4:
			s.confirming = false
			s.save(ctl, s.pending, logf)
		}
		return false
	}

	raw := resp.Values[tlsDomainField]
	typed := strings.TrimSpace(raw)
	current := ctl.Domain()
	s.typed = &raw
	switch resp.AID {
	case go3270.AIDPF3:
		if typed != current && !armed {
			s.leaveArmed = true
			s.message, s.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDPF5:
		failed := false
		for _, c := range ctl.Certificates() {
			failed = failed || c.Failed
		}
		if !failed {
			s.message = "No certificate is waiting to be tried again."
			return false
		}
		ctl.Retry()
		logf("asked for the TLS certificates again")
		s.message = "Asking for the certificate again; the log says how it went."
		return false
	case go3270.AIDEnter:
	default:
		return false
	}

	domains, err := certs.ParseDomains(typed)
	switch {
	case err != nil:
		s.message, s.isError = "That will not do: "+err.Error()+".", true
	case strings.Join(domains, ", ") == current:
		s.message, s.typed = "Nothing changed.", nil
	case (len(domains) == 0) != (current == ""):
		s.confirming, s.pending = true, strings.Join(domains, ", ")
	default:
		s.save(ctl, typed, logf)
	}
	return false
}

// save sets the domain, saying what was done.
func (s *tlsState) save(ctl TLSControl, typed string, logf func(string, ...any)) {
	got, err := ctl.SetDomain(typed)
	if err != nil {
		s.message, s.isError = "Could not set it: "+err.Error(), true
		return
	}
	s.typed = nil
	if got == "" {
		logf("turned TLS off")
		s.message = "TLS is off."
		return
	}
	logf("set the TLS domain to %s", got)
	s.message = "TLS is on for " + got + "."
}
