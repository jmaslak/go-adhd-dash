package session

import (
	"strings"

	"github.com/racingmars/go3270"
)

// A screen's text comes from people other than the one looking at it:
// calendar invitations, Trello cards, chat, checklists, user names. go3270
// passes each character to the terminal as its code page encodes it, and
// many encode to control bytes: 3270 orders (1D starts a field, 11 moves
// the buffer address, 13 places the cursor), other terminal controls, and
// FF, the telnet IAC byte, which go3270 does not escape in text. Any of
// those in text would let whoever wrote it redraw, add fields to, or end
// the screen of whoever reads it. So before a screen is sent, every
// character that would not encode to something the terminal simply shows
// is replaced.

// sanitizeReplacement stands in for a character that cannot be shown
// safely.
const sanitizeReplacement = '?'

// geOrder is the 3270 graphic escape order, which the code page puts before
// a character from its second character set (box drawing, APL); the byte
// after it is shown, not obeyed.
const geOrder = 0x08

// shown reports whether b is a byte the terminal shows rather than obeys:
// 40 (space) to FE. Below 40 are orders and controls; FF is the telnet IAC.
func shown(b byte) bool {
	return b >= 0x40 && b != 0xFF
}

// safeText is s with every character that cp would not encode to shown
// bytes replaced.
func safeText(s string, cp go3270.Codepage) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			clean = false
			break
		}
	}
	if clean {
		return s // printable ASCII, shown as it is in every EBCDIC code page
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r <= 0x7E {
			b.WriteRune(r)
			continue
		}
		switch enc := cp.Encode(string(r)); {
		case len(enc) == 1 && shown(enc[0]),
			len(enc) == 2 && enc[0] == geOrder && shown(enc[1]):
			b.WriteRune(r)
		default:
			b.WriteRune(sanitizeReplacement)
		}
	}
	return b.String()
}

// sanitizeScreen is screen with every field's text made safe to send in cp.
func sanitizeScreen(screen go3270.Screen, cp go3270.Codepage) go3270.Screen {
	out := make(go3270.Screen, len(screen))
	for i, f := range screen {
		f.Content = safeText(f.Content, cp)
		out[i] = f
	}
	return out
}
