package session

import (
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/racingmars/go3270"
)

// command is one command that can be typed on the dashboard or the command
// list.
type command struct {
	name    string
	aliases []string
	key     string // the PF key that does the same, if any
	what    string
}

// commands are the commands, in the order the command list shows them.
var commands = []command{
	{name: "tasks", aliases: []string{"task"}, key: "PF10", what: "List every open task, to archive them"},
	{name: "cal", aliases: []string{"calendar"}, key: "PF9", what: "Show the calendar"},
	{name: "calc", key: "PF4", what: "RPN calculator"},
	{name: "dbm", what: "RPN calculator, in dBm mode"},
	{name: "checklist", aliases: []string{"cl"}, what: "Checklists"},
	{name: "busy", aliases: []string{"red"}, key: "PF1", what: "Set the busy indicator to busy (red)"},
	{name: "green", what: "Set the busy indicator to green"},
	{name: "off", key: "PF2", what: "Turn the busy indicator off until the next meeting"},
	{name: "auto", key: "PF5", what: "Turn auto-refresh off or on"},
	{name: "up", aliases: []string{"prev"}, key: "PF7", what: "Previous page of tasks"},
	{name: "down", aliases: []string{"next"}, key: "PF8", what: "Next page of tasks"},
	{name: "refresh", key: "Enter", what: "Redraw the dashboard now"},
	{name: "chat", key: "PF11", what: "Chat with the other sessions"},
	{name: "admin", what: "Admin menu: shut down the server"},
	{name: "help", aliases: []string{"?"}, what: "This list of commands"},
	{name: "exit", aliases: []string{"quit", "logoff"}, key: "PF3", what: "Disconnect"},
}

// lookupCommand finds the command typed, by name or alias, ignoring case.
func lookupCommand(typed string) (command, bool) {
	typed = strings.ToLower(strings.TrimSpace(typed))
	for _, c := range commands {
		if c.name == typed || slices.Contains(c.aliases, typed) {
			return c, true
		}
	}
	return command{}, false
}

// The command line: a label, the input field, then the last message.
const (
	commandField    = "command"
	commandLabel    = "Command ===>"
	commandInputCol = len(commandLabel) + 1 // the input field's attribute byte
	commandWidth    = 20
	commandMsgCol   = commandInputCol + 1 + commandWidth
)

// commandLine is the command line on row, with message after the input
// field. The message stops a column short of the edge, which may hold the
// attribute byte of a field on the row below.
func commandLine(row, cols int, message string) []go3270.Field {
	fields := []go3270.Field{
		{Row: row, Col: 0, Color: go3270.Turquoise, Content: commandLabel},
		{
			Row: row, Col: commandInputCol, Write: true, Name: commandField,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the input field, and holds the message if there is one.
		{
			Row: row, Col: commandMsgCol, Color: go3270.Red, Intense: true,
			Content: truncate(message, max(cols-1-commandMsgCol-1, 0)),
		},
	}
	return fields
}

// buildHelp renders the list of commands, with a command line to run one,
// and where the cursor goes: the command field.
func buildHelp(rows, cols int, now time.Time, message string) (screen go3270.Screen, cursorRow, cursorCol int) {
	screen = titleFields(cols, "COMMANDS", now, false)

	format := func(name, key, what string) string {
		return truncate(name+strings.Repeat(" ", max(22-utf8.RuneCountInString(name), 1))+key+strings.Repeat(" ", max(7-len(key), 1))+what, cols-1)
	}
	heading := format("Command", "Key", "What it does")
	screen = append(screen, go3270.Field{
		Row: 2, Col: 0, Color: go3270.Turquoise, Highlighting: go3270.Underscore,
		Content: heading + strings.Repeat(" ", max(cols-1-len(heading), 0)),
	})

	row := 3
	for _, c := range commands {
		if row > rows-5 {
			break
		}
		name := c.name
		if len(c.aliases) > 0 {
			names := append([]string(nil), c.aliases...)
			sort.Strings(names)
			name += " (" + strings.Join(names, ", ") + ")"
		}
		screen = append(screen, go3270.Field{Row: row, Col: 0, Color: go3270.Green, Content: format(name, c.key, c.what)})
		row++
	}

	screen = append(screen, commandLine(rows-3, cols, message)...)
	screen = append(screen, go3270.Field{
		Row: rows - 1, Col: 0, Color: go3270.Blue,
		Content: truncate("PF3=Back Enter=Run the command typed", cols-1),
	})
	return screen, rows - 3, commandInputCol + 1
}

// fillScreen pads every protected field with spaces to the next attribute
// byte, so that the screen written without first being erased replaces
// everything shown before. Input fields are left as they are: written with
// their content dropped, whatever has been typed in them stays. The screen
// must have a field at the top left corner.
func fillScreen(screen go3270.Screen, rows, cols int) go3270.Screen {
	out := slices.Clone(screen)
	addr := func(f go3270.Field) int { return f.Row*cols + f.Col }
	sort.SliceStable(out, func(i, j int) bool { return addr(out[i]) < addr(out[j]) })
	for i := range out {
		if out[i].Write {
			out[i].Content = ""
			continue
		}
		next := rows * cols
		if i+1 < len(out) {
			next = addr(out[i+1])
		}
		if pad := next - addr(out[i]) - 1 - utf8.RuneCountInString(out[i].Content); pad > 0 {
			out[i].Content += strings.Repeat(" ", pad)
		}
	}
	return out
}
