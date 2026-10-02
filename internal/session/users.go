package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/google"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// User editor layout: the heading, the column headings, one user per row,
// then a blank row, the rows for adding a user (or changing a password),
// and the message and help rows.
//
// Each user's row is a one-character command field (D deletes the user, P
// changes their password), the name, and one-character admin, restricted
// and console fields, Y or N. Autoskip after each input field sends the
// cursor on to the next.
//
// The rows for adding a user have no console field, as there is no room:
// a user is made the console's once added.
const (
	usHeaderRow = 2
	usColumnRow = 3
	usFirstRow  = 4
	usNameCol   = 2  // the name's attribute byte, after the command field
	usNameWidth = 25 // the name, as shown
	usAdminCol  = usNameCol + 1 + usNameWidth
	usEndCol    = usAdminCol + 2 // the attribute byte ending the admin field
	usResCol    = usAdminCol + 6 // the restricted field, under "Restricted"
	usResEndCol = usResCol + 2
	usConCol    = usResCol + 11 // the console field, under "Console"
	usConEndCol = usConCol + 2

	usSelField      = "usel:"
	usAdminField    = "uadm:"
	usResField      = "ures:"
	usConField      = "ucon:"
	usPasswordField = "upw:" // then the ID of the user whose password is changing
	usNewName       = "unew"
	usNewAdmin      = "unewadm"
	usNewRes        = "unewres"
	usNewPassword   = "unewpw"

	usNewNameWidth  = 20
	usPasswordWidth = 30

	// usersHashWait is how long hashing a password waits for a slot
	// (see users.MaxConcurrentHashes) before giving up.
	usersHashWait = 30 * time.Second

	usPrompt         = "D deletes, P changes password; Y or N under Admin, Restricted, Console."
	usPasswordPrompt = "Type the new password and press Enter. PF3 cancels."
)

// usersState is one session's place in the user editor.
type usersState struct {
	page int

	// shown is what each input field held when last drawn, so that only
	// what has been typed over is changed; rowIDs are the users on the
	// rows, by row.
	shown  map[string]string
	rowIDs map[int]int

	// typed is what was typed when it could not be saved, drawn again to
	// be fixed. Passwords are never drawn again.
	typed map[string]string

	// passwordFor is the user whose password is being changed on the
	// bottom rows, zero while they are for adding a user.
	passwordFor int

	// cursorOnNew puts the cursor on the rows for adding, after a user has
	// been added from there.
	cursorOnNew bool

	// pending is a save deleting users, shown for confirmation in place of
	// the users.
	pending *usersPending

	// checklists keeps the checklists, a deleted user's deleted with them;
	// nil for none.
	checklists *checklist.Store

	// trelloBaseURL is the Trello API's address, for withdrawing a deleted
	// user's token; "" for Trello's own.
	trelloBaseURL string

	message string
	isError bool
}

// usersRows is how many users fit on one page of the user editor.
func usersRows(rows int) int {
	return max(rows-5-usFirstRow, 1)
}

// fieldValue records stored as what the field name shows, and returns what
// to draw in it: what was typed there if it could not be saved, else stored.
func (u *usersState) fieldValue(name, stored string) string {
	u.shown[name] = stored
	if v, ok := u.typed[name]; ok {
		return v
	}
	return stored
}

// yesNo is Y for true, N for false.
func yesNo(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// buildUsers renders one page of the users, and where the cursor goes.
func buildUsers(rows, cols int, now time.Time, list []users.User, loadErr error, u *usersState) (screen go3270.Screen, cursorRow, cursorCol int) {
	if u.pending != nil {
		return buildDeleteUsersConfirm(rows, cols, now, u.pending), rows - 1, 0
	}
	u.shown, u.rowIDs = map[string]string{}, map[int]int{}
	screen = titleFields(cols, "USERS", now, false)
	shown, totalPages, start, end := pageRange(len(list), usersRows(rows), u.page)
	u.page = shown

	header := line{{Content: "USERS", Color: go3270.Turquoise, Intense: true}}
	if loadErr != nil {
		header = append(header, go3270.Field{Content: loadErr.Error(), Color: go3270.Red})
	} else {
		admins := 0
		for _, x := range list {
			if x.Admin {
				admins++
			}
		}
		header = append(header, go3270.Field{
			Content: countText(len(list), "user", shown, totalPages) + ", " + countText(admins, "admin", 0, 1), Color: go3270.Blue,
		})
	}
	screen = append(screen, placeLine(usHeaderRow, cols, header)...)

	if end > start {
		// Aligned with a user's row: the command in column 1, the name
		// from column 3, the admin, restricted and console fields under
		// their headings. The first command field stops the underline.
		headings := fmt.Sprintf("%-2s%-*s%-*s%-*s%s", "S", usAdminCol+1-usNameCol-1, "Name", usResCol-usAdminCol, "Admin", usConCol-usResCol, "Restricted", "Console")
		screen = append(screen, go3270.Field{
			Row: usColumnRow, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
			Content: headings + strings.Repeat(" ", max(cols-1-len(headings), 0)),
		})
	}
	for i, x := range list[start:end] {
		row := usFirstRow + i
		u.rowIDs[row] = x.ID
		id := strconv.Itoa(x.ID)
		color, intense := go3270.Green, false
		switch {
		case x.Admin:
			color, intense = go3270.White, true
		case x.Restricted:
			color = go3270.Pink
		}
		screen = append(screen,
			go3270.Field{
				Row: row, Col: 0, Write: true, Name: usSelField + id, Content: u.fieldValue(usSelField+id, ""),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: usNameCol, Content: truncate(x.Name, usNameWidth), Color: color, Intense: intense, Autoskip: true},
			go3270.Field{
				Row: row, Col: usAdminCol, Write: true, Name: usAdminField + id, Content: u.fieldValue(usAdminField+id, yesNo(x.Admin)),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: usEndCol, Autoskip: true},
			go3270.Field{
				Row: row, Col: usResCol, Write: true, Name: usResField + id, Content: u.fieldValue(usResField+id, yesNo(x.Restricted)),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: usResEndCol, Autoskip: true},
			go3270.Field{
				Row: row, Col: usConCol, Write: true, Name: usConField + id, Content: u.fieldValue(usConField+id, yesNo(x.Console)),
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: usConEndCol, Autoskip: true},
		)
	}

	// The bottom rows change the password of the user picked, unless they
	// have gone; else they add a user.
	topRow, pwRow := rows-4, rows-3
	i := slices.IndexFunc(list, func(x users.User) bool { return x.ID == u.passwordFor })
	var pwName string
	if i < 0 {
		u.passwordFor = 0
		const nameLabel, adminLabel, resLabel = "New user ===>", "Admin ===>", "Restricted ===>"
		nameCol := len(nameLabel) + 1
		adminLabelCol := nameCol + 1 + usNewNameWidth + 1
		adminCol := adminLabelCol + 1 + len(adminLabel)
		resLabelCol := adminCol + 2
		resCol := resLabelCol + 1 + len(resLabel)
		screen = append(screen,
			go3270.Field{Row: topRow, Col: 0, Color: go3270.Turquoise, Content: nameLabel},
			go3270.Field{
				Row: topRow, Col: nameCol, Write: true, Name: usNewName, Content: u.typed[usNewName],
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: topRow, Col: adminLabelCol, Color: go3270.Turquoise, Content: adminLabel, Autoskip: true},
			go3270.Field{
				Row: topRow, Col: adminCol, Write: true, Name: usNewAdmin, Content: u.typed[usNewAdmin],
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: topRow, Col: resLabelCol, Color: go3270.Turquoise, Content: resLabel, Autoskip: true},
			go3270.Field{
				Row: topRow, Col: resCol, Write: true, Name: usNewRes, Content: u.typed[usNewRes],
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: topRow, Col: resCol + 2, Autoskip: true},
		)
		pwName = usNewPassword
	} else {
		screen = append(screen, placeLine(topRow, cols, line{
			{Content: "Change the password of", Color: go3270.Turquoise},
			{Content: list[i].Name, Color: go3270.White, Intense: true},
		})...)
		pwName = usPasswordField + strconv.Itoa(u.passwordFor)
	}
	const pwLabel = "Password ===>"
	pwCol := len(pwLabel) + 1
	screen = append(screen,
		go3270.Field{Row: pwRow, Col: 0, Color: go3270.Turquoise, Content: pwLabel},
		go3270.Field{
			Row: pwRow, Col: pwCol, Write: true, Hidden: true, Name: pwName,
			Color: go3270.Yellow, Highlighting: go3270.Underscore,
		},
		go3270.Field{Row: pwRow, Col: min(pwCol+1+usPasswordWidth, cols-1)},
	)

	message, color := u.message, go3270.Red
	if !u.isError {
		color = go3270.Green
		if message == "" {
			message, color = usPrompt, go3270.Blue
			if u.passwordFor != 0 {
				message = usPasswordPrompt
			}
		}
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: u.isError}})...)
	help := "PF3=Back PF7=Up PF8=Down Enter=Save"
	if u.passwordFor != 0 {
		help = "PF3=Cancel password change PF7=Up PF8=Down Enter=Save"
	}
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate(help, cols-1)})

	switch {
	case u.passwordFor != 0:
		return screen, pwRow, pwCol + 1
	case end > start && !u.cursorOnNew:
		return screen, usFirstRow, 1
	}
	return screen, topRow, len("New user ===>") + 2
}

// usersEdit is what was typed on the user editor, checked but not yet
// saved.
type usersEdit struct {
	deletes     map[int]bool
	admin       map[int]bool // new admin flags, by ID
	restricted  map[int]bool // new restricted flags, by ID
	console     map[int]bool // new console flags, by ID
	passwordFor int          // a user picked, with P, to change the password of
	newName     string
	newAdmin    bool
	newRes      bool
	newPassword string
	setPassword string // for u.passwordFor
}

// parse checks what was typed over the fields drawn last, returning why it
// cannot be used when it cannot.
func (u *usersState) parse(values map[string]string) (e usersEdit, bad string) {
	e.deletes, e.admin, e.restricted, e.console = map[int]bool{}, map[int]bool{}, map[int]bool{}, map[int]bool{}
	for name, shown := range u.shown {
		v, ok := values[name]
		if !ok || v == shown {
			continue
		}
		kind, idText, _ := strings.Cut(name, ":")
		id, _ := strconv.Atoi(idText)
		switch v = strings.ToUpper(strings.TrimSpace(v)); kind + ":" {
		case usSelField:
			switch v {
			case "":
			case "D":
				e.deletes[id] = true
			case "P":
				if e.passwordFor != 0 {
					return e, "Change one password at a time."
				}
				e.passwordFor = id
			default:
				return e, fmt.Sprintf("Type D to delete a user or P to change their password, not %q.", v)
			}
		case usAdminField:
			switch v {
			case "Y", "N":
				if v != shown {
					e.admin[id] = v == "Y"
				}
			default:
				return e, fmt.Sprintf("Type Y or N under Admin, not %q.", v)
			}
		case usResField:
			switch v {
			case "Y", "N":
				if v != shown {
					e.restricted[id] = v == "Y"
				}
			default:
				return e, fmt.Sprintf("Type Y or N under Restricted, not %q.", v)
			}
		case usConField:
			switch v {
			case "Y", "N":
				if v != shown {
					e.console[id] = v == "Y"
				}
			default:
				return e, fmt.Sprintf("Type Y or N under Console, not %q.", v)
			}
		}
	}
	if e.consoleTo() < 0 {
		return e, "Type Y under Console for only one user."
	}

	if u.passwordFor != 0 {
		e.setPassword = values[usPasswordField+strconv.Itoa(u.passwordFor)]
		return e, ""
	}
	e.newName = strings.TrimSpace(values[usNewName])
	e.newPassword = values[usNewPassword]
	switch a := strings.ToUpper(strings.TrimSpace(values[usNewAdmin])); {
	case a == "Y":
		e.newAdmin = true
	case a != "" && a != "N":
		return e, fmt.Sprintf("Type Y or N for the new user's Admin, not %q.", a)
	}
	switch r := strings.ToUpper(strings.TrimSpace(values[usNewRes])); {
	case r == "Y":
		e.newRes = true
	case r != "" && r != "N":
		return e, fmt.Sprintf("Type Y or N for the new user's Restricted, not %q.", r)
	}
	switch {
	case e.newName == "" && (e.newPassword != "" || e.newAdmin || e.newRes):
		return e, "Type the new user's name."
	case e.newName != "" && e.newPassword == "":
		return e, "Type the new user's password."
	}
	return e, ""
}

// consoleTo is the user given Y under Console, zero for none, or -1 for
// more than one.
func (e usersEdit) consoleTo() int {
	to := 0
	for id, c := range e.console {
		switch {
		case !c:
		case to != 0:
			return -1
		default:
			to = id
		}
	}
	return to
}

// changesFlags reports whether e changes any user's flags.
func (e usersEdit) changesFlags() bool {
	return len(e.admin) > 0 || len(e.restricted) > 0 || len(e.console) > 0
}

// usersPending is a save holding deletions, awaiting confirmation: the
// edit, the hash of any password in it, and what to go back to if it is
// not confirmed.
type usersPending struct {
	edit        usersEdit
	hash        string
	passwordFor int
	aid         go3270.AID // the key that saved, to page after
	typed       map[string]string
	deleting    []users.User // as they were when asked
	others      bool         // the save holds other changes too

	// checklists is how many checklists each user deleting has, by ID.
	checklists map[int]int
}

// usersResult is what applying an edit did.
type usersResult struct {
	removed         []string
	removedIDs      []int
	revoke          []string // the Google refresh tokens of the users removed
	revokeTrello    []tasks.Config
	changedPassword string
	added           string
	console         string // the user the console was handed to
}

// applyEdit applies e to list, with hash for the password it sets or the
// user it adds, passwordFor the user whose password is being changed.
func applyEdit(list *[]users.User, e usersEdit, passwordFor int, hash string, nextID func() int) (r usersResult, err error) {
	*list = slices.DeleteFunc(*list, func(x users.User) bool {
		if e.deletes[x.ID] {
			r.removed, r.removedIDs = append(r.removed, x.Name), append(r.removedIDs, x.ID)
			if x.Google != nil && x.Google.RefreshToken != "" {
				r.revoke = append(r.revoke, x.Google.RefreshToken)
			}
			if x.Trello != nil && x.Trello.Token != "" {
				r.revokeTrello = append(r.revokeTrello, tasks.Config{APIKey: x.Trello.APIKey, Token: x.Trello.Token})
			}
		}
		return e.deletes[x.ID]
	})
	// Y under Console for one user takes it from whoever had it.
	to := e.consoleTo()
	for i := range *list {
		x := &(*list)[i]
		switch c, ok := e.console[x.ID]; {
		case to > 0:
			x.Console = x.ID == to
			if x.Console {
				r.console = x.Name
			}
		case ok:
			x.Console = c
		}
		if admin, ok := e.admin[x.ID]; ok {
			x.Admin = admin
		}
		if restricted, ok := e.restricted[x.ID]; ok {
			x.Restricted = restricted
		}
		if x.ID == passwordFor && hash != "" {
			x.Password, r.changedPassword = hash, x.Name
		}
	}
	if passwordFor != 0 && hash != "" && r.changedPassword == "" {
		return r, errors.New("that user has been removed")
	}
	if e.newName != "" {
		*list = append(*list, users.User{ID: nextID(), Name: e.newName, Admin: e.newAdmin, Restricted: e.newRes, Password: hash})
		r.added = e.newName
	}
	return r, nil
}

// editError is the message for err, from applying an edit.
func editError(err error) string {
	switch {
	case errors.Is(err, users.ErrNoAdmin):
		return "At least one user must be an admin: that would leave none."
	case errors.Is(err, users.ErrNoConsole):
		return "The console must log in as someone: type Y under Console for another user."
	}
	msg := err.Error()
	return strings.ToUpper(msg[:1]) + msg[1:] + "."
}

// handle acts on a key pressed on the user editor, returning whether to
// leave for the admin menu. PF3 leaves without saving, or while changing a
// password, cancels that. Any other key saves what was typed first: every
// change at once, or, when any cannot be made, none, with what was typed
// left to fix (passwords aside). A save deleting users asks first: PF4 on
// the confirmation saves it all, PF3 goes back with nothing saved and what
// was typed left as it was. Then PF7 and PF8 page.
func (u *usersState) handle(resp go3270.Response, store *users.Store, logf func(string, ...any)) (leave bool) {
	u.message, u.isError, u.typed, u.cursorOnNew = "", false, nil, false
	if p := u.pending; p != nil {
		switch resp.AID {
		case go3270.AIDPF3:
			u.pending, u.typed = nil, p.typed
			u.message, u.isError = "Nothing was saved.", true
		case go3270.AIDPF4:
			u.pending = nil
			u.commit(store, p.edit, p.passwordFor, p.hash, p.aid, p.typed, logf)
		}
		return false
	}
	if resp.AID == go3270.AIDPF3 {
		if u.passwordFor != 0 {
			u.passwordFor = 0
			return false
		}
		return true
	}
	typed := map[string]string{}
	for k, v := range resp.Values {
		if k != usNewPassword && !strings.HasPrefix(k, usPasswordField) {
			typed[k] = v
		}
	}
	fail := func(msg string) {
		u.message, u.isError, u.typed = msg, true, typed
	}
	e, bad := u.parse(resp.Values)
	if bad == "" && resp.AID == go3270.AIDEnter && u.passwordFor != 0 && e.setPassword == "" && len(e.deletes) == 0 && !e.changesFlags() && e.passwordFor == 0 {
		bad = "Type the new password, or press PF3 to cancel."
	}
	if bad != "" {
		fail(bad)
		return false
	}

	// Hashed before taking the file's lock: Argon2id takes a while, on
	// purpose.
	var hash string
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), usersHashWait)
	defer cancel()
	switch {
	case e.setPassword != "":
		hash, err = users.HashPassword(ctx, e.setPassword)
	case e.newName != "":
		hash, err = users.HashPassword(ctx, e.newPassword)
	}
	if err != nil {
		fail(err.Error())
		return false
	}

	if len(e.deletes) > 0 {
		// Tried on a copy first, so that a save that would be refused is
		// refused now, not after it is confirmed.
		list, _, err := store.Load()
		if err == nil {
			trial := slices.Clone(list)
			if _, err = applyEdit(&trial, e, u.passwordFor, hash, func() int { return -1 }); err == nil {
				err = users.Validate(trial)
			}
		}
		if err != nil {
			fail(editError(err))
			return false
		}
		p := &usersPending{
			edit: e, hash: hash, passwordFor: u.passwordFor, aid: resp.AID, typed: typed,
			others: e.changesFlags() || hash != "" || e.passwordFor != 0,
		}
		for _, x := range list {
			if e.deletes[x.ID] {
				p.deleting = append(p.deleting, x)
			}
		}
		if u.checklists != nil {
			lists, err := u.checklists.Load()
			if err != nil {
				fail("Could not read the checklists, which go with the users deleted: " + err.Error())
				return false
			}
			p.checklists = map[int]int{}
			for _, l := range lists {
				p.checklists[l.Owner]++
			}
		}
		u.pending = p
		return false
	}
	u.commit(store, e, u.passwordFor, hash, resp.AID, typed, logf)
	return false
}

// commit saves e, reporting what it did, or why it could not with what was
// typed left to fix, then pages as aid asks.
func (u *usersState) commit(store *users.Store, e usersEdit, passwordFor int, hash string, aid go3270.AID, typed map[string]string, logf func(string, ...any)) {
	var r usersResult
	if len(e.deletes) > 0 || e.changesFlags() || hash != "" {
		err := store.Update(func(list *[]users.User, nextID func() int) (err error) {
			r, err = applyEdit(list, e, passwordFor, hash, nextID)
			return err
		})
		if err != nil {
			u.message, u.isError, u.typed = editError(err), true, typed
			return
		}
	}

	var said []string
	if n := len(r.removed); n > 0 {
		for _, name := range r.removed {
			logf("removed user %q", name)
		}
		said = append(said, "Removed "+countText(n, "user", 0, 1)+".")
	}
	// A user's checklists go with them.
	if u.checklists != nil && len(r.removedIDs) > 0 {
		n, err := u.checklists.RemoveOwned(func(owner int) bool { return slices.Contains(r.removedIDs, owner) })
		switch {
		case err != nil:
			logf("could not remove the removed users' checklists: %v", err)
			said = append(said, "Could not remove their checklists: "+err.Error()+".")
		case n > 0:
			logf("removed %s of the removed users", countText(n, "checklist", 0, 1))
			said = append(said, "Removed "+countText(n, "checklist", 0, 1)+".")
		}
	}
	// A user's Google calendar goes with them: the link was in their entry,
	// and Google is asked to withdraw the authorization it held.
	for _, token := range r.revoke {
		ctx, cancel := context.WithTimeout(context.Background(), googleWait)
		err := google.Revoke(ctx, token)
		cancel()
		if err != nil {
			logf("could not withdraw a removed user's Google authorization: %v", err)
			said = append(said, "Google could not be told to withdraw a removed user's calendar access ("+err.Error()+").")
			continue
		}
		logf("withdrew a removed user's Google authorization")
	}
	for _, tc := range r.revokeTrello {
		tc.BaseURL = u.trelloBaseURL
		ctx, cancel := context.WithTimeout(context.Background(), googleWait)
		err := tasks.Revoke(ctx, tc)
		cancel()
		if err != nil {
			logf("could not withdraw a removed user's Trello token: %v", err)
			said = append(said, "Trello could not be told to withdraw a removed user's token ("+err.Error()+").")
			continue
		}
		logf("withdrew a removed user's Trello token")
	}
	if r.changedPassword != "" {
		logf("changed the password of %q", r.changedPassword)
		said = append(said, "Changed the password of "+r.changedPassword+".")
		u.passwordFor = 0
	}
	if r.added != "" {
		logf("added user %q", r.added)
		said = append(said, "Added "+r.added+".")
		u.cursorOnNew = true
		u.page = 1 << 30 // the last page, where the new user is
	}
	if n := len(e.admin); n > 0 {
		for id, admin := range e.admin {
			logf("user %d admin set to %v", id, admin)
		}
		said = append(said, "Changed "+countText(n, "admin setting", 0, 1)+".")
	}
	if n := len(e.restricted); n > 0 {
		for id, restricted := range e.restricted {
			logf("user %d restricted set to %v", id, restricted)
		}
		said = append(said, "Changed "+countText(n, "restricted setting", 0, 1)+".")
	}
	if r.console != "" {
		logf("console user set to %q", r.console)
		said = append(said, "The console now logs in as "+r.console+".")
	}
	u.message = strings.Join(said, " ")
	if e.passwordFor != 0 {
		u.passwordFor = e.passwordFor
	}

	switch aid {
	case go3270.AIDPF7:
		u.page = max(u.page-1, 0)
	case go3270.AIDPF8:
		u.page++ // the next redraw keeps it to the pages there are
	}
}

// buildDeleteUsersConfirm renders the confirmation for p's deletions.
func buildDeleteUsersConfirm(rows, cols int, now time.Time, p *usersPending) go3270.Screen {
	screen := titleFields(cols, "DELETE USERS", now, false)
	question := "Delete this user?"
	if len(p.deleting) != 1 {
		question = "Delete these " + countText(len(p.deleting), "user", 0, 1) + "?"
	}
	screen = append(screen, placeLine(usHeaderRow, cols, line{{Content: question, Color: go3270.Yellow, Intense: true}})...)

	// The list runs from below the question to above the note on the other
	// changes.
	limit := max(rows-4-(usHeaderRow+2), 1)
	shown := p.deleting
	if len(shown) > limit {
		shown = shown[:limit-1]
	}
	row := usHeaderRow + 2
	for _, x := range shown {
		l := line{{Content: x.Name, Color: go3270.Green}}
		if x.Admin {
			l = line{{Content: x.Name, Color: go3270.White, Intense: true}, {Content: "(admin)", Color: go3270.Blue}}
		}
		var with []string
		if n := p.checklists[x.ID]; n > 0 {
			with = append(with, countText(n, "checklist", 0, 1))
		}
		if x.Google != nil {
			with = append(with, "their Google calendar")
		}
		if x.Trello != nil {
			with = append(with, "their Trello link")
		}
		if len(with) > 0 {
			last := len(with) - 1
			list := with[last]
			if last > 0 {
				list = strings.Join(with[:last], ", ") + " and " + list
			}
			l = append(l, go3270.Field{Content: "with " + list, Color: go3270.Blue})
		}
		screen = append(screen, placeLineAt(row, usNameCol, cols, l)...)
		row++
	}
	if len(shown) < len(p.deleting) {
		screen = append(screen, placeLineAt(row, usNameCol, cols, line{{Content: fmt.Sprintf("... and %d more", len(p.deleting)-len(shown)), Color: go3270.Blue}})...)
	}

	if p.others {
		screen = append(screen, placeLine(rows-3, cols, line{{Content: "The other changes typed with it are saved with it.", Color: go3270.Turquoise}})...)
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to delete, or PF3 to go back without saving anything.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Delete", cols-1)})
}
