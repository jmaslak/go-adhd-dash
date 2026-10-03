package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The sign-up screen: all a new-user account (users.KindNewUser) can do,
// on logging in, is make a user of one's own with it: an ordinary user,
// not an admin nor restricted, nor the console's, nor controlling the busy
// light. Then the session ends, to log in as that user.
const (
	suNameField  = "suname"
	suPassField  = "supw"
	suAgainField = "suagain"
	suFirstRow   = 4
	suLabelWidth = 25 // a label, and the attribute byte after it
	suPassWidth  = 30
	suPrompt     = "Type a user name, and a password twice, then press Enter."
)

// signupState is one session's sign-up screen.
type signupState struct {
	name string // as typed, drawn again when it could not be used

	// onPassword puts the cursor on the password, which is what the
	// message is about.
	onPassword bool

	message string
	isError bool
}

// buildSignup renders the sign-up screen for the new-user account called
// account, and where the cursor goes.
func buildSignup(rows, cols int, now time.Time, account string, s *signupState) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "SIGN UP", now)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "Make your own user", Color: go3270.Turquoise, Intense: true},
		{Content: "(signed in as " + account + ", for signing up)", Color: go3270.Blue},
	})...)
	for i, f := range []struct {
		label, name string
		hidden      bool
		width       int
	}{
		{"User name           ===>", suNameField, false, users.MaxNameLength},
		{"Password            ===>", suPassField, true, suPassWidth},
		{"Password, again     ===>", suAgainField, true, suPassWidth},
	} {
		row := suFirstRow + 2*i
		content := ""
		if !f.hidden {
			content = cutRunes(s.name, f.width)
		}
		screen = append(screen,
			go3270.Field{Row: row, Col: 0, Color: go3270.Turquoise, Content: f.label},
			go3270.Field{
				Row: row, Col: suLabelWidth, Write: true, Hidden: f.hidden, Name: f.name, Content: content,
				Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
			},
			go3270.Field{Row: row, Col: min(suLabelWidth+1+f.width, cols-1)},
		)
	}
	for i, text := range []string{
		fmt.Sprintf("A user name has up to %d letters, digits, '.', '-' or '_', and does not start", users.MaxNameLength),
		"with '.' or '-'. A password has at least 8 characters, and not the user name.",
		"Once made, you are logged off: connect again, and log in as your new user.",
	} {
		screen = append(screen, placeLine(suFirstRow+7+i, cols, line{{Content: text, Color: go3270.Green}})...)
	}
	screen = appendMessageRows(screen, rows, cols, s.message, s.isError, suPrompt, "PF3=Log off Enter=Make the user")
	if s.onPassword {
		return screen, suFirstRow + 2, suLabelWidth + 1
	}
	return screen, suFirstRow, suLabelWidth + 1
}

// handle acts on a key on the sign-up screen for the new-user account
// account, returning whether to end the session, saying farewell: on PF3,
// or once the user typed is made. Enter makes it, if its name and password
// will do, and the account is still a new-user account; created is then
// called with its name.
func (s *signupState) handle(resp go3270.Response, store *users.Store, account users.User, logf func(string, ...any), created func(name string)) (quit bool, farewell string) {
	s.message, s.isError, s.onPassword = "", false, false
	switch resp.AID {
	case go3270.AIDPF3:
		return true, "Logged off. Goodbye."
	case go3270.AIDEnter:
	default:
		return false, ""
	}
	s.name = strings.TrimSpace(resp.Values[suNameField])
	password, again := resp.Values[suPassField], resp.Values[suAgainField]
	fail := func(onPassword bool, msg string) (bool, string) {
		s.message, s.isError, s.onPassword = msg, true, onPassword
		return false, ""
	}
	switch {
	case s.name == "":
		return fail(false, "Type a user name.")
	case utf8.RuneCountInString(s.name) > users.MaxNameLength:
		return fail(false, fmt.Sprintf("A user name has at most %d characters.", users.MaxNameLength))
	case users.CheckName(s.name) != nil:
		return fail(false, fmt.Sprintf("That user name %s.", users.CheckName(s.name)))
	case password == "" || again == "":
		return fail(true, "Type the password twice.")
	case password != again:
		return fail(true, "The password was not typed the same twice.")
	case users.IsDefaultLogin(s.name, password):
		return fail(true, "That is the default password; choose another.")
	}
	if err := users.CheckNewPassword(s.name, password); err != nil {
		return fail(true, "That password will not do: "+err.Error()+".")
	}

	ctx, cancel := context.WithTimeout(context.Background(), usersHashWait)
	defer cancel()
	hash, err := users.HashPassword(ctx, password)
	switch {
	case errors.Is(err, users.ErrBusy):
		return fail(true, "The server is busy; press Enter to try again.")
	case err != nil:
		return fail(true, "Could not make the password: "+err.Error())
	}
	errNotNew := errors.New("this account can no longer sign users up")
	err = store.Update(func(list *[]users.User, nextID func() int) error {
		// Checked again as saved: the account may have been changed since
		// it logged in.
		if i := slices.IndexFunc(*list, func(x users.User) bool { return x.ID == account.ID }); i < 0 || !(*list)[i].NewUser {
			return errNotNew
		}
		*list = append(*list, users.User{ID: nextID(), Name: s.name, Password: hash})
		return nil
	})
	switch {
	case errors.Is(err, errNotNew):
		logf("sign-up of %q by %q refused: %v", s.name, account.Name, err)
		return true, "This account can no longer sign users up. Goodbye."
	case err != nil:
		return fail(false, editError(err))
	}
	logf("%q signed up %q", account.Name, s.name)
	created(s.name)
	return true, fmt.Sprintf("Made the user %s. Connect again, and log in as %s.", s.name, s.name)
}
