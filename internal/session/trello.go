package session

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/web"
)

// Trello screens: linking a user's Trello account (the TRELLO command, on
// the web site), choosing which of its lists' cards are their tasks and the
// tag each list's are shown with, unlinking it, and the admin's screen for
// the API key every user links through.
//
// On the list of lists, each is a one-character field (X shows its cards),
// its tag, and its board and name.
const (
	tFirstRow   = 4
	tTagCol     = 2 // the tag's attribute byte, after the X field
	tTagWidth   = 10
	tNameCol    = tTagCol + 1 + tTagWidth // the name's attribute byte
	tSelField   = "tsel:"                 // then the list's index in trelloState's lists
	tTagField   = "ttag:"
	tKeyField   = "tkey"
	tKeyLabel   = "API key ===>"
	tChoosePrmt = "X shows a list's cards as your tasks; its tag goes in brackets before them."
)

// tasksFor is the task cache for u's Trello lists, or nil when they have
// none to show: no API key set, not linked through it, or no list chosen.
// The user is read afresh, so that lists chosen in another session show
// here too.
func (cfg Config) tasksFor(u *users.User) *tasks.Cache {
	if cfg.TaskPool == nil || cfg.Users == nil || u == nil {
		return nil
	}
	list, client, err := cfg.Users.TrelloClient()
	if err != nil {
		return nil
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == u.ID })
	if i < 0 || !list[i].TrelloLinked(client) || len(list[i].Trello.Lists) == 0 {
		return nil
	}
	link := list[i].Trello
	tc := tasks.Config{APIKey: client.APIKey, Token: link.Token, BaseURL: cfg.TrelloBaseURL}
	for _, l := range link.Lists {
		tc.Lists = append(tc.Lists, tasks.Destination{BoardID: l.BoardID, Board: l.Board, ListID: l.ListID, List: l.List, Tag: l.Tag})
	}
	// Anything that changes what is read changes the key, and so the cache.
	key := strings.Join([]string{strconv.Itoa(u.ID), tc.APIKey, tc.Token, fmt.Sprint(tc.Lists)}, "\x00")
	return cfg.TaskPool.Get(key, func() (tasks.Config, error) { return tc, nil })
}

// trelloStep is which Trello screen a session is on.
type trelloStep int

const (
	trelloConnect    trelloStep = iota // linking, on the web site
	trelloChoose                       // choosing lists
	trelloDisconnect                   // confirming unlinking
)

// trelloOffer is a list offered to choose from; gone is set for one chosen
// before that Trello no longer has.
type trelloOffer struct {
	users.TrelloList
	gone bool
}

// trelloState is one session's place in linking its user's Trello account.
type trelloState struct {
	step   trelloStep
	userID int
	apiKey string

	// baseURL is the Trello API's address; "" for Trello's own.
	baseURL string

	// connectURL is the web page the user links on; reconnecting is set
	// when they are linked already, which PF3 goes back to.
	connectURL   string
	reconnecting bool

	// token is the user's, once linked, and username their Trello name.
	token, username string

	// lists are those offered: every open list of every open board, in
	// Trello's order, then any chosen that are gone. listErr is why they
	// could not be read.
	lists   []trelloOffer
	listErr error

	// chosen and tags are the choices as they stand, by list ID; saved is
	// them as saved.
	chosen map[string]bool
	tags   map[string]string
	saved  []users.TrelloList

	page  int
	drawn []int // the lists on the page last drawn, by index

	typed      map[string]string
	leaveArmed bool

	message string
	isError bool
}

// startTrello readies the Trello screens for the user with userID: choosing
// lists if they are linked, else linking, on the web site, which must be
// served (served) and have its address set. baseURL is the Trello API's.
// It says why not when it cannot.
func startTrello(store *users.Store, userID int, served bool, baseURL string) (t trelloState, why string) {
	if store == nil || userID == 0 {
		return t, "There is no user database to keep a Trello account in."
	}
	list, client, err := store.TrelloClient()
	if err != nil {
		return t, "Could not read the users: " + err.Error()
	}
	site, err := store.Site()
	switch {
	case err != nil:
		return t, "Could not read the web site's settings: " + err.Error()
	case client == nil:
		return t, "No Trello API key is set up; an admin sets it on the admin menu."
	case site == nil || site.BaseURL == "" || !served:
		return t, "The web site, where Trello is linked, is not set up; ask an admin."
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == userID })
	if i < 0 {
		return t, "You are no longer a user."
	}
	t = trelloState{userID: userID, apiKey: client.APIKey, baseURL: baseURL, connectURL: site.BaseURL + "trello"}
	if u := list[i]; u.TrelloLinked(client) {
		t.token, t.username, t.saved = u.Trello.Token, u.Trello.Username, slices.Clone(u.Trello.Lists)
		t.step = trelloChoose
		t.loadLists()
		return t, ""
	}
	t.step = trelloConnect
	return t, ""
}

// config is the Trello API's configuration for the user's token.
func (t *trelloState) config() tasks.Config {
	return tasks.Config{APIKey: t.apiKey, Token: t.token, BaseURL: t.baseURL}
}

// loadLists reads the user's boards and lists from Trello, keeping the
// choices as they stand, and offering any chosen that is gone.
func (t *trelloState) loadLists() {
	if t.chosen == nil {
		t.chosen, t.tags = map[string]bool{}, map[string]string{}
		for _, l := range t.saved {
			t.chosen[l.ListID], t.tags[l.ListID] = true, l.Tag
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), googleWait)
	defer cancel()
	boards, err := tasks.Boards(ctx, t.config())
	t.listErr = err
	if err != nil && len(t.lists) > 0 {
		return // keep the lists already read
	}
	t.lists = nil
	for _, b := range boards {
		for _, l := range b.Lists {
			t.lists = append(t.lists, trelloOffer{TrelloList: users.TrelloList{BoardID: b.ID, Board: b.Name, ListID: l.ID, List: l.Name}})
		}
	}
	for _, l := range t.saved {
		if !slices.ContainsFunc(t.lists, func(o trelloOffer) bool { return o.ListID == l.ListID }) {
			t.lists = append(t.lists, trelloOffer{TrelloList: l, gone: err == nil})
		}
	}
}

// choices are the lists chosen as they stand, in the order offered.
func (t *trelloState) choices() []users.TrelloList {
	var out []users.TrelloList
	for _, o := range t.lists {
		if t.chosen[o.ListID] {
			l := o.TrelloList
			l.Tag = t.tags[o.ListID]
			out = append(out, l)
		}
	}
	return out
}

// unsaved reports whether the choices differ from those saved.
func (t *trelloState) unsaved() bool {
	return !slices.EqualFunc(t.choices(), t.saved, func(a, b users.TrelloList) bool { return a.ListID == b.ListID && a.Tag == b.Tag })
}

// build renders the screen the session is on, and where the cursor goes.
func (t *trelloState) build(rows, cols int, now time.Time) (go3270.Screen, int, int) {
	switch t.step {
	case trelloConnect:
		return buildTrelloConnect(rows, cols, now, t), rows - 1, 0
	case trelloDisconnect:
		return buildTrelloDisconnect(rows, cols, now), rows - 1, 0
	}
	return buildTrelloChoose(rows, cols, now, t)
}

// handle acts on a key, returning whether to leave for the dashboard, and
// then what to say there.
func (t *trelloState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool, message string) {
	t.message, t.isError, t.typed = "", false, nil
	switch t.step {
	case trelloConnect:
		return t.handleConnect(resp, store, logf)
	case trelloDisconnect:
		return t.handleDisconnect(resp, store, logf)
	}
	return t.handleChoose(resp, store, logf)
}

// buildTrelloConnect renders the steps for linking, on the web site.
func buildTrelloConnect(rows, cols int, now time.Time, t *trelloState) go3270.Screen {
	screen := titleFields(cols, "LINK TRELLO", now)
	header := line{{Content: "Link your Trello account", Color: go3270.Turquoise, Intense: true}}
	if t.reconnecting {
		header = append(header, go3270.Field{Content: "(linked now; this links it again)", Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	text := func(row int, s string) {
		screen = append(screen, placeLine(row, cols, line{{Content: s, Color: go3270.Green}})...)
	}
	text(4, "1. In a web browser, go to:")
	screen = append(screen, placeLineAt(5, 4, cols, line{{Content: t.connectURL, Color: go3270.White, Intense: true}})...)
	text(7, "2. Sign in there with the user name and password you use here.")
	text(8, "3. Choose Link, then at Trello, allow access to your boards.")
	text(9, "4. When the page says Linked, come back here and press Enter, to choose")
	text(10, "   which of your Trello lists to show as your tasks.")
	return appendMessageRows(screen, rows, cols, t.message, t.isError,
		"Press Enter once the web page says Linked.", "PF3=Back Enter=Continue")
}

// handleConnect acts on a key on the linking screen: Enter goes on to
// choosing lists once the user has linked (again, if reconnecting) on the
// web site, PF3 goes back.
func (t *trelloState) handleConnect(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	switch resp.AID {
	case go3270.AIDPF3:
		if t.reconnecting {
			t.step, t.reconnecting = trelloChoose, false
			return false, ""
		}
		return true, ""
	case go3270.AIDEnter:
	default:
		return false, ""
	}
	list, client, err := store.TrelloClient()
	if err != nil {
		t.message, t.isError = "Could not read the users: "+err.Error(), true
		return false, ""
	}
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == t.userID })
	switch {
	case i < 0:
		return true, "You are no longer a user."
	case !list[i].TrelloLinked(client) || list[i].Trello.Token == t.token:
		t.message, t.isError = "Not linked yet: finish on the web page first, then press Enter.", true
		return false, ""
	}
	u := list[i]
	logf("linked a Trello account")
	t.apiKey, t.token, t.username, t.saved = client.APIKey, u.Trello.Token, u.Trello.Username, slices.Clone(u.Trello.Lists)
	t.step, t.reconnecting, t.chosen, t.lists = trelloChoose, false, nil, nil
	t.loadLists()
	t.message = "Linked."
	if len(t.saved) == 0 {
		t.message = "Linked. Choose the lists whose cards are your tasks, then press Enter to save."
	}
	return false, ""
}

// buildTrelloChoose renders one page of the lists to choose from.
func buildTrelloChoose(rows, cols int, now time.Time, t *trelloState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "TRELLO LISTS", now)
	perPage := max(rows-tFirstRow-3, 1)
	shown, total, start, end := pageRange(len(t.lists), perPage, t.page)
	t.page = shown

	header := line{{Content: "Lists to show", Color: go3270.Turquoise, Intense: true}}
	info := countText(len(t.choices()), "list", shown, total) + " chosen"
	if t.unsaved() {
		info += ", not saved"
	}
	if t.username != "" {
		info += "; Trello account " + t.username
	}
	header = append(header, go3270.Field{Content: info, Color: go3270.Blue})
	if t.listErr != nil {
		header = append(header, go3270.Field{Content: "lists unavailable: " + t.listErr.Error(), Color: go3270.Red})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	headings := "X Tag        Board / List"
	screen = append(screen, go3270.Field{
		Row: 3, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
		Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
	})

	input := func(row, col int, name, content string) go3270.Field {
		if v, ok := t.typed[name]; ok {
			content = v
		}
		return go3270.Field{
			Row: row, Col: col, Write: true, Name: name, Content: content,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		}
	}
	t.drawn = nil
	for i := start; i < end; i++ {
		o, row, k := t.lists[i], tFirstRow+i-start, strconv.Itoa(i)
		t.drawn = append(t.drawn, i)
		mark, color := "", go3270.Turquoise
		if t.chosen[o.ListID] {
			mark, color = "X", go3270.Green
		}
		name := o.Board + " / " + o.List
		if o.gone {
			name += " (gone from Trello)"
			color = go3270.Red
		}
		screen = append(screen,
			input(row, 0, tSelField+k, mark),
			input(row, tTagCol, tTagField+k, t.tags[o.ListID]),
			go3270.Field{Row: row, Col: tNameCol, Color: color, Intense: t.chosen[o.ListID], Content: truncate(name, cols-tNameCol-2)},
		)
	}
	if len(t.lists) == 0 && t.listErr == nil {
		screen = append(screen, placeLine(tFirstRow, cols, line{{Content: "Your Trello account has no open lists.", Color: go3270.Blue}})...)
	}
	screen = appendMessageRows(screen, rows, cols, t.message, t.isError, tChoosePrmt,
		"PF3=Back PF5=Reread PF6=Unlink PF7=Up PF8=Down PF9=Link again Enter=Save")
	return screen, tFirstRow, 1
}

// handleChoose takes what was typed on the lists as the choices, then acts
// on the key: Enter saves them, PF3 leaves (a second time, with choices
// not saved), PF5 reads the lists again, PF6 asks to unlink, PF7 and PF8
// page, and PF9 links again.
func (t *trelloState) handleChoose(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	armed := t.leaveArmed
	t.leaveArmed = false
	for _, i := range t.drawn {
		id, k := t.lists[i].ListID, strconv.Itoa(i)
		if v, ok := resp.Values[tSelField+k]; ok {
			t.chosen[id] = strings.TrimSpace(v) != ""
		}
		if v, ok := resp.Values[tTagField+k]; ok {
			t.tags[id] = strings.TrimSpace(v)
		}
	}
	for _, l := range t.choices() {
		if strings.ContainsAny(l.Tag, ",[] ") {
			t.message, t.isError, t.typed = fmt.Sprintf("A tag cannot have a space, comma or bracket in it: %q.", l.Tag), true, resp.Values
			return false, ""
		}
	}

	switch resp.AID {
	case go3270.AIDEnter:
		if !t.unsaved() {
			return false, ""
		}
		choices := t.choices()
		err := store.Update(func(list *[]users.User, _ func() int) error {
			i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == t.userID })
			if i < 0 {
				return errors.New("you are no longer a user")
			}
			if l := (*list)[i].Trello; l == nil || l.Token != t.token {
				return errors.New("your Trello account was unlinked or linked again in another session")
			}
			(*list)[i].Trello.Lists = choices
			return nil
		})
		if err != nil {
			t.message, t.isError = "Could not save: "+err.Error(), true
			return false, ""
		}
		t.saved = choices
		logf("chose %s of their Trello account", countText(len(choices), "list", 0, 1))
		t.message = "Saved: showing the cards of " + countText(len(choices), "list", 0, 1) + "."
	case go3270.AIDPF3:
		if t.unsaved() && !armed {
			t.leaveArmed = true
			t.message, t.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false, ""
		}
		return true, ""
	case go3270.AIDPF5:
		t.loadLists()
		if t.listErr == nil {
			t.message = "Read the lists again."
		}
	case go3270.AIDPF6:
		t.step = trelloDisconnect
	case go3270.AIDPF7:
		t.page--
	case go3270.AIDPF8:
		t.page++ // the next redraw keeps it to the pages there are
	case go3270.AIDPF9:
		t.step, t.reconnecting = trelloConnect, true
	}
	return false, ""
}

// buildTrelloDisconnect renders the confirmation for unlinking.
func buildTrelloDisconnect(rows, cols int, now time.Time) go3270.Screen {
	screen := titleFields(cols, "UNLINK TRELLO", now)
	screen = append(screen, placeLine(2, cols, line{{Content: "Unlink your Trello account?", Color: go3270.Yellow, Intense: true}})...)
	screen = append(screen, placeLine(4, cols, line{{Content: "This removes your Trello token and your choice of lists from this server,"}})...)
	screen = append(screen, placeLine(5, cols, line{{Content: "asks Trello to withdraw the token, and stops showing your Trello tasks."}})...)
	screen = append(screen, placeLine(6, cols, line{{Content: "Your cards stay on Trello as they are."}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to unlink, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Unlink", cols-1)})
}

// handleDisconnect acts on a key on the confirmation for unlinking.
func (t *trelloState) handleDisconnect(resp go3270.Response, store *users.Store, logf func(string, ...any)) (bool, string) {
	switch resp.AID {
	case go3270.AIDPF3:
		t.step = trelloChoose
	case go3270.AIDPF4:
		err := store.Update(func(list *[]users.User, _ func() int) error {
			if i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == t.userID }); i >= 0 {
				(*list)[i].Trello = nil
			}
			return nil
		})
		if err != nil {
			t.step, t.message, t.isError = trelloChoose, "Could not save: "+err.Error(), true
			return false, ""
		}
		logf("unlinked their Trello account")
		ctx, cancel := context.WithTimeout(context.Background(), googleWait)
		err = tasks.Revoke(ctx, t.config())
		cancel()
		if err != nil {
			return true, "Unlinked; Trello could not be told (" + err.Error() + ")."
		}
		return true, "Unlinked your Trello account."
	}
	return false, ""
}

// trelloKeyState is one session's place on the admin's screen for the
// Trello API key.
type trelloKeyState struct {
	// confirming shows the confirmation for replacing or removing the key,
	// which would make linked users link again; pending is the key to
	// set, nil to remove it.
	confirming bool
	pending    *users.TrelloClient

	typed      map[string]string
	leaveArmed bool

	message string
	isError bool
}

// trelloKeySteps tell an admin how to make the key, for a web site at base
// ("" when its address is not set).
func trelloKeySteps(base string) []string {
	origin := "    (set the web site's address first, on admin menu option 6)"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		origin = "    " + u.Scheme + "://" + u.Host
	}
	return []string{
		"Users link their Trello accounts through this Trello API key. To make one:",
		" 1. Signed in to Trello, go to trello.com/power-ups/admin.",
		" 2. New: make a Power-Up called " + web.AppName + " in your workspace,",
		"    with your email as its contact.",
		" 3. On its API key tab, Generate a new API key.",
		" 4. Under Allowed origins, add the web site's address:",
		origin,
		" 5. Copy the API key below. (Its secret is not needed.)",
	}
}

// linkedCount is how many of list are linked through client.
func linkedCount(list []users.User, client *users.TrelloClient) int {
	n := 0
	for _, u := range list {
		if u.TrelloLinked(client) {
			n++
		}
	}
	return n
}

// buildTrelloKey renders the admin's screen for the Trello API key, for the
// web site site (nil if not set up), or the confirmation for replacing or
// removing it.
func buildTrelloKey(rows, cols int, now time.Time, list []users.User, client *users.TrelloClient, site *users.Site, loadErr error, k *trelloKeyState) (screen go3270.Screen, cursorRow, cursorCol int) {
	if k.confirming {
		return buildTrelloKeyConfirm(rows, cols, now, linkedCount(list, client), k.pending == nil), rows - 1, 0
	}
	screen = titleFields(cols, "TRELLO API KEY", now)
	header := line{{Content: "Trello API key", Color: go3270.Turquoise, Intense: true}}
	switch {
	case loadErr != nil:
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	case client == nil:
		header = append(header, go3270.Field{Content: "none set: no one can link Trello", Color: go3270.Yellow})
	default:
		header = append(header, go3270.Field{Content: "set; " + countText(linkedCount(list, client), "user", 0, 1) + " linked", Color: go3270.Blue})
	}
	screen = append(screen, placeLine(2, cols, header)...)
	base := ""
	if site != nil {
		base = site.BaseURL
	}
	steps := trelloKeySteps(base)
	for i, s := range steps {
		screen = append(screen, placeLine(4+i, cols, line{{Content: s, Color: go3270.Green}})...)
	}
	key := ""
	if client != nil {
		key = client.APIKey
	}
	if v, ok := k.typed[tKeyField]; ok {
		key = v
	}
	row, col := 5+len(steps), len(tKeyLabel)+1
	screen = append(screen,
		go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: tKeyLabel},
		go3270.Field{
			Row: row, Col: col, Write: true, Name: tKeyField, Content: cutRunes(key, cols-col-2),
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: row, Col: cols - 1},
	)
	screen = append(screen, placeLine(row+1, cols, line{{Content: "Blank the key to remove it.", Color: go3270.Blue}})...)
	screen = appendMessageRows(screen, rows, cols, k.message, k.isError,
		"Type the Power-Up's API key, then press Enter.", "PF3=Back Enter=Save")
	return screen, row, col + 1
}

// buildTrelloKeyConfirm renders the confirmation for replacing (or with
// remove, removing) the key, which n users are linked through.
func buildTrelloKeyConfirm(rows, cols int, now time.Time, n int, remove bool) go3270.Screen {
	screen := titleFields(cols, "TRELLO API KEY", now)
	question, key := "Replace the Trello API key?", "PF4=Replace"
	if remove {
		question, key = "Remove the Trello API key?", "PF4=Remove"
	}
	screen = append(screen, placeLine(2, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)
	screen = append(screen, placeLine(4, cols, line{{
		Content: countText(n, "user", 0, 1) + " linked through it will need to link again (TRELLO command).",
	}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to go ahead, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back "+key, cols-1)})
}

// handle acts on a key on the Trello API key screen, returning whether to
// leave for the admin menu.
func (k *trelloKeyState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool) {
	k.message, k.isError = "", false
	armed := k.leaveArmed
	k.leaveArmed = false
	if k.confirming {
		switch resp.AID {
		case go3270.AIDPF3:
			k.confirming = false
		case go3270.AIDPF4:
			k.confirming = false
			k.save(store, k.pending, logf)
		}
		return false
	}
	list, client, err := store.TrelloClient()
	if err != nil {
		k.message, k.isError = "Could not read the users: "+err.Error(), true
		return false
	}
	key := strings.TrimSpace(resp.Values[tKeyField])
	current := ""
	if client != nil {
		current = client.APIKey
	}
	k.typed = map[string]string{tKeyField: resp.Values[tKeyField]}

	switch resp.AID {
	case go3270.AIDPF3:
		if key != current && !armed {
			k.leaveArmed = true
			k.message, k.isError = "Not saved: press Enter to save, or PF3 again to leave without saving.", true
			return false
		}
		return true
	case go3270.AIDEnter:
	default:
		return false
	}
	switch {
	case key == current:
		k.message, k.typed = "Nothing changed.", nil
	case strings.ContainsFunc(key, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') }):
		k.message, k.isError = "A Trello API key is letters a to f and digits.", true
	case key == "" || linkedCount(list, client) > 0:
		k.confirming = true
		k.pending = nil
		if key != "" {
			k.pending = &users.TrelloClient{APIKey: key}
		}
	default:
		k.save(store, &users.TrelloClient{APIKey: key}, logf)
	}
	return false
}

// save sets the key, or with nil removes it, saying what was done.
func (k *trelloKeyState) save(store *users.Store, client *users.TrelloClient, logf func(string, ...any)) {
	if err := store.SetTrelloClient(client); err != nil {
		k.message, k.isError = "Could not save: "+err.Error(), true
		return
	}
	k.typed = nil
	if client == nil {
		logf("removed the Trello API key")
		k.message = "Removed the Trello API key."
		return
	}
	logf("set the Trello API key %s", client.APIKey)
	k.message = "Saved the Trello API key."
}
