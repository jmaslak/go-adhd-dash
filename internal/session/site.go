package session

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/web"
)

// The admin's screen for the web site: its public address, and the
// organization and contact its privacy policy and terms name.
const (
	siteURLField     = "siteurl"
	siteOrgField     = "siteorg"
	siteContactField = "sitecontact"
	siteLabelWidth   = 20 // "Contact email  ===>", and the attribute byte after it
	siteFirstField   = 8
	siteMaxText      = 80 - siteLabelWidth - 2 // what a field holds on an 80-column screen
)

// siteText explains the screen.
var siteText = []string{
	"This server's web pages: a home page, a privacy policy and terms of service,",
	"which Google asks for before it verifies the app. A web server in front of",
	"this one should serve them over HTTPS at the public address set here.",
}

// siteState is one session's place on the web site screen.
type siteState struct {
	// typed is what was typed when it could not be saved, drawn again.
	typed map[string]string

	// leaveArmed is set by PF3 with something typed: a second PF3 leaves.
	leaveArmed bool

	message string
	isError bool
}

// buildSite renders the web site screen; listen is where the pages are
// served, "" for nowhere.
func buildSite(rows, cols int, now time.Time, site *users.Site, loadErr error, listen string, s *siteState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "WEB SITE", now)
	header := line{{Content: "Web site", Color: go3270.Turquoise, Intense: true}}
	switch {
	case loadErr != nil:
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	case listen == "":
		header = append(header, go3270.Field{Content: "not served (-http-port 0)", Color: go3270.Yellow})
	default:
		header = append(header, go3270.Field{Content: "served over HTTP at " + listen, Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	for i, t := range siteText {
		screen = append(screen, placeLine(4+i, cols, line{{Content: t, Color: go3270.Green}})...)
	}

	var current users.Site
	if site != nil {
		current = *site
	}
	value := func(name, stored string) string {
		if v, ok := s.typed[name]; ok {
			return v
		}
		return stored
	}
	fields := []struct{ label, name, value string }{
		{"Public address ===>", siteURLField, current.BaseURL},
		{"Organization   ===>", siteOrgField, current.Organization},
		{"Contact email  ===>", siteContactField, current.Contact},
	}
	for i, f := range fields {
		row := siteFirstField + i
		screen = append(screen,
			go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: f.label},
			go3270.Field{
				Row: row, Col: siteLabelWidth, Write: true, Name: f.name, Content: cutRunes(value(f.name, f.value), cols-siteLabelWidth-2),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: cols - 1},
		)
	}

	row := siteFirstField + len(fields) + 1
	if current.BaseURL != "" {
		screen = append(screen, placeLine(row, cols, line{{Content: "The pages, for Google's consent screen (Branding):", Color: go3270.Blue}})...)
		for i, p := range [][2]string{{"Home page", ""}, {"Privacy policy", "privacy"}, {"Terms of service", "terms"}} {
			screen = append(screen, placeLine(row+1+i, cols, line{
				{Content: fmt.Sprintf("  %-17s", p[0]), Color: go3270.Turquoise},
				{Content: current.BaseURL + p[1], Color: go3270.White, Intense: true},
			})...)
		}
	} else {
		screen = append(screen, placeLine(row, cols, line{{Content: "Until an address is set, the pages link to each other by path alone.", Color: go3270.Blue}})...)
	}

	screen = appendGoogleMessage(screen, rows, cols, s.message, s.isError,
		"Type the address, e.g. https://adhd.example.com/, and press Enter.", "PF3=Back Enter=Save")
	return screen, siteFirstField, siteLabelWidth + 1
}

// handle acts on a key on the web site screen, returning whether to leave
// for the admin menu: Enter saves what was typed, blanking every field
// removes the settings, and PF3 leaves, a second time with something typed.
func (s *siteState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool) {
	s.message, s.isError = "", false
	armed := s.leaveArmed
	s.leaveArmed, s.typed = false, nil

	site, err := store.Site()
	if err != nil {
		s.message, s.isError = "Could not read the settings: "+err.Error(), true
		return false
	}
	var current users.Site
	if site != nil {
		current = *site
	}
	typed := users.Site{
		BaseURL:      strings.TrimSpace(resp.Values[siteURLField]),
		Organization: strings.TrimSpace(resp.Values[siteOrgField]),
		Contact:      strings.TrimSpace(resp.Values[siteContactField]),
	}
	keep := func() {
		s.typed = map[string]string{siteURLField: resp.Values[siteURLField], siteOrgField: resp.Values[siteOrgField], siteContactField: resp.Values[siteContactField]}
	}

	switch resp.AID {
	case go3270.AIDPF3:
		if typed != current && !armed {
			keep()
			s.leaveArmed = true
			s.message, s.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}

	if typed == (users.Site{}) {
		if site == nil {
			return false
		}
		if err := store.SetSite(nil); err != nil {
			s.message, s.isError = "Could not save: "+err.Error(), true
			return false
		}
		logf("removed the web site's settings")
		s.message = "Removed the web site's settings."
		return false
	}
	if bad := checkSite(&typed); bad != "" {
		keep()
		s.message, s.isError = bad, true
		return false
	}
	if typed == current {
		return false
	}
	if err := store.SetSite(&typed); err != nil {
		keep()
		s.message, s.isError = "Could not save: "+err.Error(), true
		return false
	}
	logf("set the web site to %s, for %q, contact %q", typed.BaseURL, typed.Organization, typed.Contact)
	s.message = "Saved."
	return false
}

// checkSite says why site cannot be saved, or with "", puts its address in
// the form kept.
func checkSite(site *users.Site) string {
	if site.BaseURL == "" {
		return "Type the public address too."
	}
	base, err := web.NormalizeBaseURL(site.BaseURL)
	if err != nil {
		return "That address will not do: " + err.Error() + "."
	}
	site.BaseURL = base
	for _, t := range []string{site.BaseURL, site.Organization, site.Contact} {
		if utf8.RuneCountInString(t) > siteMaxText || strings.ContainsFunc(t, unicode.IsControl) {
			return fmt.Sprintf("%q is too long, or has a control character in it.", t)
		}
	}
	if c := site.Contact; c != "" && (strings.Count(c, "@") != 1 || strings.HasPrefix(c, "@") || strings.HasSuffix(c, "@") || strings.ContainsAny(c, " <>\"")) {
		return fmt.Sprintf("%q is not an email address.", c)
	}
	return ""
}
