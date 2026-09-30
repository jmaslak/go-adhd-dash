package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/racingmars/go3270"
)

// The admin menu: its options, one per row, then the option field.
const (
	adminOptionField = "option"
	adminOptionLabel = "Option ===>"
	adminFirstRow    = 4
)

// adminOptions are the admin menu's options, by the number typed to choose
// one.
var adminOptions = []struct{ number, what string }{
	{"1", "Shut down the server"},
	{"2", "Activity viewer: every session, its LU, screen and IP address"},
	{"3", "Clear the chat"},
	{"4", "Users: add, remove, change passwords and admins"},
}

// buildAdmin renders the admin menu, with message below the option field,
// in green if ok says it reports something done, and where the cursor goes:
// the option field. sessions is how many are connected.
func buildAdmin(rows, cols int, now time.Time, message string, ok bool, sessions int) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "ADMIN", now, false)
	screen = append(screen, placeLine(2, cols, line{
		{Content: "ADMIN MENU", Color: go3270.Turquoise, Intense: true},
		{Content: countText(sessions, "session", 0, 1) + " connected", Color: go3270.Blue},
	})...)
	for i, o := range adminOptions {
		screen = append(screen, placeLine(adminFirstRow+i, cols, line{
			{Content: fmt.Sprintf("%3s", o.number), Color: go3270.White, Intense: true},
			{Content: o.what, Color: go3270.Green},
		})...)
	}

	optionRow, optionCol := rows-3, len(adminOptionLabel)+1
	screen = append(screen,
		go3270.Field{Row: optionRow, Col: 0, Color: go3270.Turquoise, Content: adminOptionLabel},
		go3270.Field{
			Row: optionRow, Col: optionCol, Write: true, Name: adminOptionField,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the option field after a few columns.
		go3270.Field{Row: optionRow, Col: optionCol + 4},
	)
	switch {
	case message == "":
		screen = append(screen, placeLine(rows-2, cols, line{{Content: "Type an option's number and press Enter.", Color: go3270.Blue}})...)
	case ok:
		screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: go3270.Green}})...)
	default:
		screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: go3270.Red, Intense: true}})...)
	}
	screen = append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back Enter=Select", cols-1)})
	return screen, optionRow, optionCol + 1
}

// adminAction is what a key on the admin menu asks for.
type adminAction int

const (
	adminStay      adminAction = iota // the menu again
	adminLeave                        // the dashboard
	adminShutdown                     // the confirmation for shutting down
	adminActivity                     // the activity viewer
	adminClearChat                    // the confirmation for clearing the chat
	adminUsers                        // the user editor
)

// adminChoice is what the admin menu's key asks for, and when it is to stay
// because of what was typed, a message saying why.
func adminChoice(resp go3270.Response) (action adminAction, message string) {
	if resp.AID == go3270.AIDPF3 {
		return adminLeave, ""
	}
	if resp.AID != go3270.AIDEnter {
		return adminStay, ""
	}
	switch typed := strings.TrimSpace(resp.Values[adminOptionField]); typed {
	case "":
		return adminStay, ""
	case "1":
		return adminShutdown, ""
	case "2":
		return adminActivity, ""
	case "3":
		return adminClearChat, ""
	case "4":
		return adminUsers, ""
	default:
		return adminStay, fmt.Sprintf("There is no option %q.", typed)
	}
}

// buildShutdownConfirm renders the confirmation for shutting the server
// down, which disconnects sessions sessions, this one among them.
func buildShutdownConfirm(rows, cols int, now time.Time, sessions int) go3270.Screen {
	screen := titleFields(cols, "SHUT DOWN", now, false)
	screen = append(screen, placeLine(2, cols, line{{Content: "Shut down the server?", Color: go3270.Yellow, Intense: true}})...)
	others := countText(max(sessions-1, 0), "other", 0, 1)
	screen = append(screen, placeLine(4, cols, line{{
		Content: "This disconnects this session and " + others + ", finishes any file",
	}})...)
	screen = append(screen, placeLine(5, cols, line{{Content: "being written, and stops the server."}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to shut down, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Shut down", cols-1)})
}

// buildClearChatConfirm renders the confirmation for clearing the chat,
// which holds n messages.
func buildClearChatConfirm(rows, cols int, now time.Time, n int) go3270.Screen {
	screen := titleFields(cols, "CLEAR CHAT", now, false)
	screen = append(screen, placeLine(2, cols, line{{Content: "Clear the chat?", Color: go3270.Yellow, Intense: true}})...)
	screen = append(screen, placeLine(4, cols, line{{
		Content: "This deletes " + countText(n, "message", 0, 1) + ", for everyone. It cannot be undone.",
	}})...)
	screen = append(screen, placeLine(rows-2, cols, line{{Content: "Press PF4 to clear it, or PF3 to go back.", Color: go3270.White, Intense: true}})...)
	return append(screen, go3270.Field{Row: rows - 1, Col: 0, Color: go3270.Blue, Content: truncate("PF3=Back PF4=Clear", cols-1)})
}

// shutdownFarewell is left on a terminal disconnected by shutdown.
const shutdownFarewell = "The server is shutting down. Goodbye."
