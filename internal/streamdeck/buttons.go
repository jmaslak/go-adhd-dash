package streamdeck

import (
	_ "embed"
)

// The sd program's pictures.
var (
	//go:embed img/red-no-entry.png
	busyPNG []byte
	//go:embed img/ok.png
	freePNG []byte
	//go:embed img/green-no-entry.png
	greenPNG []byte
	//go:embed img/no-reminder.png
	noReminderPNG []byte
	//go:embed img/reminder.png
	reminderPNG []byte
)

// BusyButtons are the sd program's keys, setting the busy light by key
// with set, which reports a failure to logf: Busy (b) red, Free (o) off
// until the meetings under way end, and Green (g); two blank keys (the
// second was Agenda); and Remind, whose picture each press turns over, and
// which does nothing else.
func BusyButtons(set func(key rune) error, logf func(format string, args ...any)) []Button {
	key := func(k rune) func() {
		return func() {
			if err := set(k); err != nil {
				logf("stream deck: setting the busy light: %v", err)
			}
		}
	}
	return []Button{
		{Label: "Busy", Images: [][]byte{busyPNG}, Press: key('b')},
		{Label: "Free", Images: [][]byte{freePNG}, Press: key('o')},
		{Label: "Green", Images: [][]byte{greenPNG}, Press: key('g')},
		{},
		{},
		{Label: "Remind", Images: [][]byte{noReminderPNG, reminderPNG}},
	}
}
