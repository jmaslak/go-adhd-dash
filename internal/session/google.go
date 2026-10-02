package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmaslak/go-busy-indicator/gauth"
	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/google"
	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/web"
)

// Google calendar screens: connecting a user's calendar (the GOOGLE
// command), choosing which of its calendars to show, disconnecting it, and
// the admin's screen for the OAuth client every user connects through.
//
// On the list of calendars, each is a one-character field (X shows it), its
// alias, and its name, on two rows when it is long. After the last, the page
// is filled out with blank entries for calendars not on the list, each an
// alias and an ID typed across two rows.
const (
	gFirstRow   = 4
	gAliasCol   = 2 // the alias's attribute byte, after the X field
	gAliasWidth = 10
	gNameCol    = gAliasCol + 1 + gAliasWidth // the name's attribute byte

	// Field names: a kind, then the calendar's index in googleState's
	// calendars, or which blank entry.
	gSelField   = "gsel:"
	gAliasField = "galias:"
	gNewField   = "gnew:"  // a blank entry's ID, its first row
	gNewMore    = "gnewx:" // and its second
	gNewAlias   = "gnewalias:"

	gClientIDField     = "gcid"
	gClientIDMore      = "gcidx"
	gClientSecretField = "gcsecret"
	gClientLabelWidth  = 19 // "Client secret ===>", and the attribute byte after it

	// googleWait bounds a call to Google, which holds up the session.
	googleWait = 30 * time.Second

	gChoosePrompt = "X shows a calendar, its alias before its events. Blank rows take an ID."
)

// agendaFor is the agenda u sees: the one shown to everyone, if there is
// one, else their own Google calendars', or nil when they have none to show:
// no client set up, not connected through it, or no calendar chosen. The
// user is read afresh, so that a calendar connected or chosen in another
// session shows here too.
func (cfg Config) agendaFor(u *users.User) *agenda.Cache {
	if cfg.Agenda != nil {
		return cfg.Agenda
	}
	if cfg.Agendas == nil || cfg.Users == nil || u == nil {
		return nil
	}
	list, client, err := cfg.Users.GoogleClient()
	if err != nil {
		return nil
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == u.ID })
	if i < 0 || !list[i].Connected(client) || len(list[i].Google.Calendars) == 0 {
		return nil
	}
	link := list[i].Google
	ids, aliases := make([]string, len(link.Calendars)), make([]string, len(link.Calendars))
	for j, c := range link.Calendars {
		ids[j], aliases[j] = c.ID, c.Alias
	}
	// Anything that changes what is read changes the key, and so the cache.
	key := strings.Join(append([]string{strconv.Itoa(u.ID), client.ClientID, client.ClientSecret, link.RefreshToken},
		fmt.Sprint(ids), fmt.Sprint(aliases)), "\x00")
	return cfg.Agendas.Get(key, func() agenda.Source {
		src := agenda.NewGoogle(google.Tokens(google.Client{ID: client.ClientID, Secret: client.ClientSecret}, link.RefreshToken), ids, aliases)
		src.Client.BaseURL = google.APIBase
		return src
	})
}

// agendaErrorText is why the agenda could not be read, saying first what to
// do when Google has refused the user's authorization, so that it survives
// being cut to fit.
func agendaErrorText(err error) string {
	var rejected *gauth.RejectedError
	if errors.As(err, &rejected) {
		return "type GOOGLE to reconnect (Google refused your authorization)"
	}
	return err.Error()
}

// googleStep is which Google calendar screen a session is on.
type googleStep int

const (
	googleConnect    googleStep = iota // authorizing
	googleChoose                       // choosing calendars
	googleDisconnect                   // confirming disconnecting
)

// googleCalendar is a calendar offered to choose from.
type googleCalendar struct {
	ID, Name string
	Primary  bool
}

// googleState is one session's place in connecting its user's Google
// calendar.
type googleState struct {
	step   googleStep
	userID int
	client google.Client

	// connectURL is the web page the user connects on; reconnecting is set
	// when they are connected already, which PF3 goes back to.
	connectURL   string
	reconnecting bool

	// refreshToken is the user's authorization, once connected.
	refreshToken string

	// calendars are those offered: the user's calendar list, then those
	// chosen by ID. listErr is why the list could not be read.
	calendars []googleCalendar
	listErr   error

	// chosen and aliases are the choices as they stand, by calendar ID;
	// saved is them as saved.
	chosen  map[string]bool
	aliases map[string]string
	saved   []users.Calendar

	page int

	// drawn are the calendars on the page last drawn, by index, and blanks
	// how many blank entries followed them.
	drawn  []int
	blanks int

	// typed is what was typed when it could not be used, drawn again to be
	// fixed.
	typed map[string]string

	// leaveArmed is set by PF3 with choices not saved: a second PF3 leaves
	// without saving them.
	leaveArmed bool

	message string
	isError bool
}

// startGoogle readies the Google calendar screens for the user with userID:
// choosing calendars if they are connected, else connecting, on the web
// site, which must be served (served) and have its address set. It says why
// not when it cannot.
func startGoogle(store *users.Store, userID int, served bool) (g googleState, why string) {
	if store == nil || userID == 0 {
		return g, "There is no user database to keep a Google calendar in."
	}
	list, client, err := store.GoogleClient()
	if err != nil {
		return g, "Could not read the users: " + err.Error()
	}
	site, err := store.Site()
	switch {
	case err != nil:
		return g, "Could not read the web site's settings: " + err.Error()
	case client == nil:
		return g, "No Google client is set up; an admin sets it on the admin menu."
	case site == nil || site.BaseURL == "" || !served:
		return g, "The web site, where calendars are connected, is not set up; ask an admin."
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == userID })
	if i < 0 {
		return g, "You are no longer a user."
	}
	g = googleState{userID: userID, client: google.Client{ID: client.ClientID, Secret: client.ClientSecret}, connectURL: site.BaseURL + "google"}
	if u := list[i]; u.Connected(client) {
		g.refreshToken, g.saved = u.Google.RefreshToken, slices.Clone(u.Google.Calendars)
		g.step = googleChoose
		g.loadCalendars()
		return g, ""
	}
	g.step = googleConnect
	return g, ""
}

// tokens gives access tokens for the user's authorization.
func (g *googleState) tokens() google.TokenSource {
	return google.Tokens(g.client, g.refreshToken)
}

// loadCalendars reads the user's calendar list from Google, keeping the
// choices as they stand, and offering any calendar chosen that is not on it.
func (g *googleState) loadCalendars() {
	if g.chosen == nil {
		g.chosen, g.aliases = map[string]bool{}, map[string]string{}
		for _, c := range g.saved {
			g.chosen[c.ID], g.aliases[c.ID] = true, c.Alias
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), googleWait)
	defer cancel()
	list, err := google.ListCalendars(ctx, g.tokens())
	g.listErr = err
	if err != nil && len(g.calendars) > 0 {
		return // keep the list already read
	}
	others := g.calendars
	g.calendars = nil
	for _, c := range list {
		g.calendars = append(g.calendars, googleCalendar{ID: c.ID, Name: c.Name(), Primary: c.Primary})
	}
	add := func(c googleCalendar) {
		if !slices.ContainsFunc(g.calendars, func(x googleCalendar) bool { return x.ID == c.ID }) {
			g.calendars = append(g.calendars, c)
		}
	}
	for _, c := range g.saved {
		add(googleCalendar{ID: c.ID, Name: c.Name})
	}
	for _, c := range others {
		if g.chosen[c.ID] {
			add(c)
		}
	}
}

// choices are the calendars chosen as they stand, in the order offered.
func (g *googleState) choices() []users.Calendar {
	var out []users.Calendar
	for _, c := range g.calendars {
		if g.chosen[c.ID] {
			out = append(out, users.Calendar{ID: c.ID, Name: c.Name, Alias: g.aliases[c.ID]})
		}
	}
	return out
}

// unsaved reports whether the choices differ from those saved.
func (g *googleState) unsaved() bool {
	return !slices.EqualFunc(g.choices(), g.saved, func(a, b users.Calendar) bool { return a.ID == b.ID && a.Alias == b.Alias })
}

// build renders the screen the session is on, and where the cursor goes.
func (g *googleState) build(rows, cols int, now time.Time) (go3270.Screen, int, int) {
	switch g.step {
	case googleConnect:
		return buildGoogleConnect(rows, cols, now, g)
	case googleDisconnect:
		return buildGoogleDisconnect(rows, cols, now), rows - 1, 0
	}
	return buildGoogleChoose(rows, cols, now, g)
}

// handle acts on a key, returning whether to leave for the dashboard, and
// then what to say there.
func (g *googleState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool, message string) {
	g.message, g.isError, g.typed = "", false, nil
	switch g.step {
	case googleConnect:
		return g.handleConnect(resp, store, logf)
	case googleDisconnect:
		return g.handleDisconnect(resp, store, logf)
	}
	return g.handleChoose(resp, store, logf)
}

// buildGoogleConnect renders the steps for connecting, on the web site.
func buildGoogleConnect(rows, cols int, now time.Time, g *googleState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "CONNECT GOOGLE CALENDAR", now)
	header := line{{Content: "Connect your Google calendar", Color: go3270.Turquoise, Intense: true}}
	if g.reconnecting {
		header = append(header, go3270.Field{Content: "(connected now; this connects it again)", Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	text := func(row int, s string) {
		screen = append(screen, placeLine(row, cols, line{{Content: s, Color: go3270.Green}})...)
	}
	text(4, "1. In a web browser, go to:")
	screen = append(screen, placeLineAt(5, 4, cols, line{{Content: g.connectURL, Color: go3270.White, Intense: true}})...)
	text(7, "2. Sign in there with the user name and password you use here.")
	text(8, "3. Choose Connect, then at Google, allow read-only access to your calendars.")
	text(9, "   If Google warns that the app is unverified, choose Advanced, then go on.")
	text(10, "4. When the page says Connected, come back here and press Enter, to choose")
	text(11, "   which of your calendars to show.")
	screen = appendMessageRows(screen, rows, cols, g.message, g.isError,
		"Press Enter once the web page says Connected.", "PF3=Back Enter=Continue")
	return screen, rows - 1, 0
}

// handleConnect acts on a key on the connecting screen: Enter goes on to
// choosing calendars once the user has connected (again, if reconnecting)
// on the web site, PF3 goes back.
func (g *googleState) handleConnect(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	switch resp.AID {
	case go3270.AIDPF3:
		if g.reconnecting {
			g.step, g.reconnecting = googleChoose, false
			return false, ""
		}
		return true, ""
	case go3270.AIDEnter:
	default:
		return false, ""
	}

	list, client, err := store.GoogleClient()
	if err != nil {
		g.message, g.isError = "Could not read the users: "+err.Error(), true
		return false, ""
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == g.userID })
	switch {
	case i < 0:
		return true, "You are no longer a user."
	case !list[i].Connected(client) || list[i].Google.RefreshToken == g.refreshToken:
		g.message, g.isError = "Not connected yet: finish on the web page first, then press Enter.", true
		return false, ""
	}
	u := list[i]
	logf("connected a Google calendar")
	g.client = google.Client{ID: client.ClientID, Secret: client.ClientSecret}
	g.refreshToken, g.saved = u.Google.RefreshToken, slices.Clone(u.Google.Calendars)
	g.step, g.reconnecting, g.chosen, g.calendars = googleChoose, false, nil, nil
	g.loadCalendars()
	g.message = "Connected."
	if len(g.saved) == 0 {
		// The calendar of one's own is the usual choice: offer it.
		for _, c := range g.calendars {
			if c.Primary {
				g.chosen[c.ID] = true
			}
		}
		g.message = "Connected. Choose the calendars to show, then press Enter to save."
	}
	return false, ""
}

// googleEntry is one entry on a page of the calendar list: a calendar, by
// index, or a blank entry (cal -1), starting on row and height rows tall.
type googleEntry struct {
	cal, row, height int
}

// googleNameLines is a calendar's name as listed, on one row or two.
func googleNameLines(c googleCalendar, cols int) []string {
	name := c.Name
	switch {
	case name == "":
		name = c.ID
	case c.Primary:
		name += " (your own)"
	case name != c.ID && !strings.HasSuffix(c.ID, "calendar.google.com"):
		name += " (" + c.ID + ")"
	}
	width := cols - gNameCol - 1
	r := []rune(name)
	if len(r) <= width {
		return []string{name}
	}
	return []string{string(r[:width]), truncate(strings.TrimSpace(string(r[width:])), width)}
}

// googlePages lays the calendars out on pages perPage rows tall, the last
// filled out with blank entries, two rows each; a page of them follows a
// last page with no room for one.
func googlePages(cals []googleCalendar, perPage, cols int) [][]googleEntry {
	pages := [][]googleEntry{nil}
	used := 0
	place := func(cal, height int) {
		if used+height > perPage {
			pages, used = append(pages, nil), 0
		}
		n := len(pages) - 1
		pages[n] = append(pages[n], googleEntry{cal: cal, row: gFirstRow + used, height: height})
		used += height
	}
	for i, c := range cals {
		place(i, len(googleNameLines(c, cols)))
	}
	if perPage-used < 2 {
		pages, used = append(pages, nil), 0
	}
	for perPage-used >= 2 {
		place(-1, 2)
	}
	return pages
}

// buildGoogleChoose renders one page of the calendars to choose from.
func buildGoogleChoose(rows, cols int, now time.Time, g *googleState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "GOOGLE CALENDARS", now)
	pages := googlePages(g.calendars, max(rows-gFirstRow-3, 2), cols)
	g.page = min(max(g.page, 0), len(pages)-1)

	header := line{{Content: "Calendars to show", Color: go3270.Turquoise, Intense: true}}
	info := countText(len(g.choices()), "calendar", g.page, len(pages)) + " chosen"
	if g.unsaved() {
		info += ", not saved"
	}
	header = append(header, go3270.Field{Content: info, Color: go3270.Blue})
	if g.listErr != nil {
		header = append(header, go3270.Field{Content: "list unavailable: " + g.listErr.Error(), Color: go3270.Red})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	headings := "X Alias      Calendar"
	screen = append(screen, go3270.Field{
		Row: 3, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
		Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
	})

	input := func(row, col int, name, content string) go3270.Field {
		if v, ok := g.typed[name]; ok {
			content = v
		}
		return go3270.Field{
			Row: row, Col: col, Write: true, Name: name, Content: content,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		}
	}
	g.drawn, g.blanks = nil, 0
	cursorRow, cursorCol = gFirstRow, 1
	for n, e := range pages[g.page] {
		if e.cal < 0 {
			k := strconv.Itoa(g.blanks)
			g.blanks++
			// The ID runs across two rows, each its own field, aligned.
			screen = append(screen,
				go3270.Field{Row: e.row, Col: 0},
				input(e.row, gAliasCol, gNewAlias+k, ""),
				input(e.row, gNameCol, gNewField+k, ""),
				go3270.Field{Row: e.row + 1, Col: 0},
				input(e.row+1, gNameCol, gNewMore+k, ""),
				go3270.Field{Row: e.row + 1, Col: cols - 1},
			)
			if n == 0 {
				cursorRow, cursorCol = e.row, gNameCol+1
			}
			continue
		}
		c := g.calendars[e.cal]
		g.drawn = append(g.drawn, e.cal)
		i := strconv.Itoa(e.cal)
		mark, color := "", go3270.Turquoise
		if g.chosen[c.ID] {
			mark, color = "X", go3270.Green
		}
		screen = append(screen,
			input(e.row, 0, gSelField+i, mark),
			input(e.row, gAliasCol, gAliasField+i, g.aliases[c.ID]),
		)
		for j, text := range googleNameLines(c, cols) {
			screen = append(screen, go3270.Field{Row: e.row + j, Col: gNameCol, Color: color, Intense: g.chosen[c.ID], Content: text})
		}
	}
	screen = appendMessageRows(screen, rows, cols, g.message, g.isError,
		gChoosePrompt,
		"PF3=Back PF5=Reread PF6=Disconnect PF7=Up PF8=Down PF9=Reconnect Enter=Save")
	return screen, cursorRow, cursorCol
}

// handleChoose takes what was typed on the list of calendars as the
// choices, adding any calendar typed by ID once Google says it can be read,
// then acts on the key: Enter saves the choices, PF3 leaves (a second time,
// with choices not saved), PF5 reads the list again, PF6 asks to disconnect,
// PF7 and PF8 page, and PF9 authorizes again.
func (g *googleState) handleChoose(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	values := resp.Values
	armed := g.leaveArmed
	g.leaveArmed = false

	for _, i := range g.drawn {
		id, k := g.calendars[i].ID, strconv.Itoa(i)
		if v, ok := values[gSelField+k]; ok {
			g.chosen[id] = strings.TrimSpace(v) != ""
		}
		if v, ok := values[gAliasField+k]; ok {
			g.aliases[id] = strings.TrimSpace(v)
		}
	}
	for k := range g.blanks {
		n := strconv.Itoa(k)
		id := strings.Join(strings.Fields(values[gNewField+n]+" "+values[gNewMore+n]), "")
		if id == "" {
			continue
		}
		if !slices.ContainsFunc(g.calendars, func(c googleCalendar) bool { return c.ID == id }) {
			ctx, cancel := context.WithTimeout(context.Background(), googleWait)
			c, err := google.LookupCalendar(ctx, g.tokens(), id)
			cancel()
			if err != nil {
				g.message, g.isError, g.typed = fmt.Sprintf("Cannot read the calendar %s: %v", id, err), true, values
				return false, ""
			}
			g.calendars = append(g.calendars, googleCalendar{ID: id, Name: c.Name()})
		}
		g.chosen[id] = true
		if alias := strings.TrimSpace(values[gNewAlias+n]); alias != "" {
			g.aliases[id] = alias
		}
	}
	for _, c := range g.choices() {
		if strings.ContainsAny(c.Alias, ",[]") {
			g.message, g.isError = fmt.Sprintf("An alias cannot have a comma or a bracket in it: %q.", c.Alias), true
			return false, ""
		}
	}

	switch resp.AID {
	case go3270.AIDEnter:
		if !g.unsaved() {
			return false, ""
		}
		choices := g.choices()
		err := store.Update(func(list *[]users.User, _ func() int) error {
			i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == g.userID })
			if i < 0 {
				return errors.New("you are no longer a user")
			}
			if l := (*list)[i].Google; l == nil || l.RefreshToken != g.refreshToken {
				return errors.New("your Google calendar was disconnected or reconnected in another session")
			}
			(*list)[i].Google.Calendars = choices
			return nil
		})
		if err != nil {
			g.message, g.isError = "Could not save: "+err.Error(), true
			return false, ""
		}
		g.saved = choices
		logf("chose %s of their Google calendar", countText(len(choices), "calendar", 0, 1))
		g.message = "Saved: showing " + countText(len(choices), "calendar", 0, 1) + "."
	case go3270.AIDPF3:
		if g.unsaved() && !armed {
			g.leaveArmed = true
			g.message, g.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false, ""
		}
		return true, ""
	case go3270.AIDPF5:
		g.loadCalendars()
		if g.listErr == nil {
			g.message = "Read the list again."
		}
	case go3270.AIDPF6:
		g.step = googleDisconnect
	case go3270.AIDPF7:
		g.page--
	case go3270.AIDPF8:
		g.page++ // the next redraw keeps it to the pages there are
	case go3270.AIDPF9:
		g.step, g.reconnecting = googleConnect, true
	}
	return false, ""
}

// buildGoogleDisconnect renders the confirmation for disconnecting.
func buildGoogleDisconnect(rows, cols int, now time.Time) go3270.Screen {
	screen := titleFields(cols, "DISCONNECT GOOGLE", now)
	screen = append(screen, placeLine(2, cols, line{{Content: "Disconnect your Google calendar?", Color: go3270.Yellow, Intense: true}})...)
	screen = append(screen, placeLine(4, cols, line{{Content: "This removes your authorization and your choice of calendars from this"}})...)
	screen = append(screen, placeLine(5, cols, line{{Content: "server, asks Google to withdraw it, and stops showing your calendar."}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to disconnect, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Disconnect", cols-1)})
}

// handleDisconnect acts on a key on the confirmation for disconnecting.
func (g *googleState) handleDisconnect(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	switch resp.AID {
	case go3270.AIDPF3:
		g.step = googleChoose
	case go3270.AIDPF4:
		err := store.Update(func(list *[]users.User, _ func() int) error {
			if i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == g.userID }); i >= 0 {
				(*list)[i].Google = nil
			}
			return nil
		})
		if err != nil {
			g.step, g.message, g.isError = googleChoose, "Could not save: "+err.Error(), true
			return false, ""
		}
		logf("disconnected their Google calendar")
		ctx, cancel := context.WithTimeout(context.Background(), googleWait)
		err = google.Revoke(ctx, g.refreshToken)
		cancel()
		if err != nil {
			return true, "Disconnected; Google could not be told (" + err.Error() + ")."
		}
		return true, "Disconnected your Google calendar."
	}
	return false, ""
}

// googleClientState is one session's place on the admin's screen for the
// Google OAuth client.
type googleClientState struct {
	// confirming shows the confirmation for replacing or removing the
	// client, which would make connected users connect again; pending is
	// the client to set, nil to remove it.
	confirming bool
	pending    *users.GoogleClient

	// typed is what was typed (but the secret), drawn again.
	typed map[string]string

	// leaveArmed is set by PF3 with something typed: a second PF3 leaves.
	leaveArmed bool

	message string
	isError bool
}

// googleClientSteps tell an admin how to make the client, for a web site
// at base ("" when its address is not set).
func googleClientSteps(base string) []string {
	redirect := "    " + base + "google/callback"
	if base == "" {
		redirect = "    (set the web site's address first, on admin menu option 6)"
	}
	return []string{
		"Users connect their calendars through this Google OAuth client. To make one:",
		" 1. At console.cloud.google.com, create a project, or choose one.",
		" 2. APIs & Services > Library: enable the Google Calendar API.",
		" 3. Google Auth Platform > Branding: app name " + web.AppName + ", your email, and the",
		"    home page, privacy and terms addresses shown on admin menu option 6.",
		" 4. Audience: Internal, for a Google Workspace domain's users only. Otherwise",
		"    External, then Publish app (in Testing, authorizations last 7 days;",
		"    published but not verified, Google warns each user before allowing).",
		" 5. Clients > Create client > Web application, with the redirect URI:",
		redirect,
		" 6. Copy the client's ID and secret below.",
	}
}

// connectedCount is how many of list are connected through client.
func connectedCount(list []users.User, client *users.GoogleClient) int {
	n := 0
	for _, u := range list {
		if u.Connected(client) {
			n++
		}
	}
	return n
}

// buildGoogleClient renders the admin's screen for the Google client, for
// the web site site (nil if not set up), or the confirmation for replacing
// or removing it.
func buildGoogleClient(rows, cols int, now time.Time, list []users.User, client *users.GoogleClient, site *users.Site, loadErr error, g *googleClientState) (screen go3270.Screen, cursorRow, cursorCol int) {
	if g.confirming {
		return buildGoogleClientConfirm(rows, cols, now, connectedCount(list, client), g.pending == nil), rows - 1, 0
	}
	screen = titleFields(cols, "GOOGLE CLIENT", now)
	header := line{{Content: "Google OAuth client", Color: go3270.Turquoise, Intense: true}}
	switch {
	case loadErr != nil:
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	case client == nil:
		header = append(header, go3270.Field{Content: "none set: no one can connect a calendar", Color: go3270.Yellow})
	default:
		header = append(header, go3270.Field{Content: "set; " + countText(connectedCount(list, client), "user", 0, 1) + " connected", Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	base := ""
	if site != nil {
		base = site.BaseURL
	}
	steps := googleClientSteps(base)
	for i, s := range steps {
		screen = append(screen, placeLine(4+i, cols, line{{Content: s, Color: go3270.Green}})...)
	}

	id := ""
	if client != nil {
		id = client.ClientID
	}
	if v, ok := g.typed[gClientIDField]; ok {
		id = v + g.typed[gClientIDMore]
	}
	width := cols - gClientLabelWidth - 2
	r := []rune(id)
	first, more := string(r[:min(len(r), width)]), string(r[min(len(r), width):])
	row := 5 + len(steps)
	input := func(row int, name, content string, hidden bool) go3270.Field {
		return go3270.Field{
			Row: row, Col: gClientLabelWidth, Write: true, Name: name, Content: content, Hidden: hidden,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		}
	}
	screen = append(screen,
		go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: "Client ID     ===>"},
		input(row, gClientIDField, first, false),
		go3270.Field{Row: row + 1, Col: 0},
		input(row+1, gClientIDMore, more, false),
		go3270.Field{Row: row + 2, Col: 0, Color: go3270.Turquoise, Content: "Client secret ===>"},
		input(row+2, gClientSecretField, "", true),
		go3270.Field{Row: row + 2, Col: cols - 1},
	)
	note := "The secret is not shown. Blank, it keeps the one set; blank both to remove."
	if client == nil {
		note = "The secret is not shown as it is typed."
	}
	screen = append(screen, placeLine(row+3, cols, line{{Content: note, Color: go3270.Blue}})...)
	screen = appendMessageRows(screen, rows, cols, g.message, g.isError,
		"Type the client's ID and secret, then press Enter.", "PF3=Back Enter=Save")
	return screen, row, gClientLabelWidth + 1
}

// buildGoogleClientConfirm renders the confirmation for replacing (or with
// remove, removing) the client, which n users are connected through.
func buildGoogleClientConfirm(rows, cols int, now time.Time, n int, remove bool) go3270.Screen {
	screen := titleFields(cols, "GOOGLE CLIENT", now)
	question, key := "Replace the Google client?", "PF4=Replace"
	if remove {
		question, key = "Remove the Google client?", "PF4=Remove"
	}
	screen = append(screen, placeLine(2, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)
	screen = append(screen, placeLine(4, cols, line{{
		Content: countText(n, "user", 0, 1) + " connected through it will need to connect again (GOOGLE command).",
	}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to go ahead, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back "+key, cols-1)})
}

// handle acts on a key on the Google client screen, returning whether to
// leave for the admin menu.
func (g *googleClientState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool) {
	g.message, g.isError = "", false
	armed := g.leaveArmed
	g.leaveArmed = false
	if g.confirming {
		switch resp.AID {
		case go3270.AIDPF3:
			g.confirming = false
		case go3270.AIDPF4:
			g.confirming = false
			g.save(store, g.pending, logf)
		}
		return false
	}

	_, client, err := store.GoogleClient()
	if err != nil {
		g.message, g.isError = "Could not read the users: "+err.Error(), true
		return false
	}
	id := strings.Join(strings.Fields(resp.Values[gClientIDField]+" "+resp.Values[gClientIDMore]), "")
	secret := strings.TrimSpace(resp.Values[gClientSecretField])
	current := ""
	if client != nil {
		current = client.ClientID
	}
	g.typed = map[string]string{gClientIDField: resp.Values[gClientIDField], gClientIDMore: resp.Values[gClientIDMore]}

	switch resp.AID {
	case go3270.AIDPF3:
		if (id != current || secret != "") && !armed {
			g.leaveArmed = true
			g.message, g.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}

	list, _, _ := store.GoogleClient()
	switch {
	case id == "" && secret == "" && client == nil:
		g.message, g.isError = "Type the client's ID and secret.", true
	case id == "" && secret == "":
		g.confirming, g.pending = true, nil
	case id == "":
		g.message, g.isError = "Type the client ID too.", true
	case id == current && secret == "":
		g.message, g.typed = "Nothing changed.", nil
	case id != current && secret == "":
		g.message, g.isError = "Type the secret of this client too.", true
	case id != current && connectedCount(list, client) > 0:
		g.confirming, g.pending = true, &users.GoogleClient{ClientID: id, ClientSecret: secret}
	default:
		g.save(store, &users.GoogleClient{ClientID: id, ClientSecret: secret}, logf)
	}
	return false
}

// save sets the client, or with nil removes it, saying what was done.
func (g *googleClientState) save(store *users.Store, client *users.GoogleClient, logf func(string, ...any)) {
	if err := store.SetGoogleClient(client); err != nil {
		g.message, g.isError = "Could not save: "+err.Error(), true
		return
	}
	g.typed = nil
	if client == nil {
		logf("removed the Google client")
		g.message = "Removed the Google client."
		return
	}
	logf("set the Google client %s", client.ClientID)
	g.message = "Saved the Google client."
}
