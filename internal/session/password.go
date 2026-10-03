package session

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The screen for changing one's own password (the PASSWORD command): the
// current password, then the new one twice, each typed hidden.
const (
	pwCurrentField = "pwcur"
	pwNewField     = "pwnew"
	pwAgainField   = "pwagain"
	pwFirstRow     = 4
	pwLabelWidth   = 25 // "New password, again ===>", and the attribute byte after it
	pwWidth        = 30

	// pwTries is how many wrong current passwords are allowed before going
	// back to the dashboard.
	pwTries = 3
)

// passwordState is one session's place on the password screen.
type passwordState struct {
	// forced is set for a user logged in with the default password, who
	// must change it to go on (leaving logs off); changed once they have.
	forced, changed bool

	failures int
	message  string
	isError  bool
}

// buildPassword renders the password screen for the user called name, and
// where the cursor goes: the current password.
func buildPassword(rows, cols int, now time.Time, name string, p *passwordState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "CHANGE PASSWORD", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "Change your password", Color: go3270.Turquoise, Intense: true},
		{Content: "(" + name + ")", Color: go3270.Blue},
	})...)
	for i, f := range []struct{ label, name string }{
		{"Current password    ===>", pwCurrentField},
		{"New password        ===>", pwNewField},
		{"New password, again ===>", pwAgainField},
	} {
		row := pwFirstRow + 2*i
		screen = append(screen,
			go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: f.label},
			go3270.Field{
				Row: row, Col: pwLabelWidth, Write: true, Hidden: true, Name: f.name,
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: pwLabelWidth + 1 + pwWidth},
		)
	}
	prompt, help := "Type your current password, then the new one twice, and press Enter.", "PF3=Back Enter=Change"
	if p.forced {
		screen = append(screen, placeLine(pwFirstRow+7, cols, line{{
			Content: "You logged in with the default password: choose a new one to go on.", Color: go3270.Yellow, Intense: true,
		}})...)
		help = "PF3=Log off Enter=Change"
	}
	screen = appendMessageRows(screen, rows, cols, p.message, p.isError, prompt, help)
	return screen, pwFirstRow, pwLabelWidth + 1
}

// handle acts on a key on the password screen for user u, returning whether
// to leave for the dashboard, and what to say there. Enter changes the
// password, if the current one is right, the new one typed the same twice,
// and not the first user's default. Too many wrong current passwords go
// back to the dashboard (or for a forced change, log off).
func (p *passwordState) handle(resp go3270.Response, store *users.Store, u *users.User, logf func(string, ...any)) (leave bool, message string) {
	p.message, p.isError = "", false
	switch resp.AID {
	case go3270.AIDPF3:
		return true, ""
	case go3270.AIDEnter:
	default:
		return false, ""
	}
	current, next, again := resp.Values[pwCurrentField], resp.Values[pwNewField], resp.Values[pwAgainField]
	fail := func(msg string) (bool, string) {
		p.message, p.isError = msg, true
		return false, ""
	}
	switch {
	case current == "" || next == "" || again == "":
		return fail("Type your current password, and the new one twice.")
	case next != again:
		return fail("The new password was not typed the same twice.")
	case next == current:
		return fail("The new password is the same as the old one.")
	case users.IsDefaultLogin(u.Name, next):
		return fail("That is the default password; choose another.")
	}
	if err := users.CheckNewPassword(u.Name, next); err != nil {
		return fail("That password will not do: " + err.Error() + ".")
	}

	ctx, cancel := context.WithTimeout(context.Background(), usersHashWait)
	defer cancel()
	found, ok, err := store.Authenticate(ctx, u.Name, current)
	switch {
	case errors.Is(err, users.ErrBusy):
		return fail("The server is busy; press Enter to try again.")
	case err != nil:
		return fail("Could not check the password: " + err.Error())
	case !ok || found.ID != u.ID:
		p.failures++
		logf("password change by %q refused: wrong current password (%d of %d)", u.Name, p.failures, pwTries)
		if p.failures >= pwTries {
			return true, "Too many wrong passwords; your password is unchanged."
		}
		return fail("That is not your current password.")
	}
	hash, err := users.HashPassword(ctx, next)
	if err != nil {
		return fail("Could not make the new password: " + err.Error())
	}
	err = store.Update(func(list *[]users.User, _ func() int) error {
		i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == u.ID })
		if i < 0 {
			return users.ErrNoUser
		}
		(*list)[i].Password = hash
		return nil
	})
	if err != nil {
		return fail("Could not save: " + err.Error())
	}
	logf("%q changed their password", u.Name)
	p.changed = true
	return true, "Your password is changed."
}
