package session

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/racingmars/go3270"
)

// chatKeep is how many messages the chat keeps, dropping the oldest.
const chatKeep = 1000

// ChatMessage is one message posted to the chat.
type ChatMessage struct {
	Seq  int // from 1, in the order posted
	Time time.Time
	From string // the poster's LU
	Text string
}

// Chat holds the messages every session of this server shares, in memory
// only, and wakes the sessions on the chat screen when one is posted, so
// that they show it at once.
type Chat struct {
	mu       sync.Mutex
	messages []ChatMessage
	seq      int // the last message's Seq
	version  int // counts every change: each message posted, and each clearing
	watchers map[uint64]chatWatcher
}

// chatWatcher is a session on the chat screen: its LU, and its connection,
// to wake it.
type chatWatcher struct {
	lu   string
	conn net.Conn
}

// NewChat returns an empty chat.
func NewChat() *Chat {
	return &Chat{watchers: map[uint64]chatWatcher{}}
}

// watch records that session id, as lu on conn, is on the chat screen, to
// be woken when a message is posted. A nil Chat does nothing, here and in
// its other methods.
func (c *Chat) watch(id uint64, lu string, conn net.Conn) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchers[id] = chatWatcher{lu: lu, conn: conn}
}

// unwatch records that session id has left the chat screen. It must be
// called before the session next sets its read deadline, so that a wake
// meant for the chat screen cannot cut short the wait for a key on another.
func (c *Chat) unwatch(id uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.watchers, id)
}

// post adds a message from lu, sent by session id, and wakes every other
// session on the chat screen: its read deadline passes, and it redraws.
func (c *Chat) post(id uint64, lu, text string, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.messages = append(c.messages, ChatMessage{Seq: c.seq, Time: now, From: lu, Text: text})
	if len(c.messages) > chatKeep {
		c.messages = slices.Delete(c.messages, 0, len(c.messages)-chatKeep)
	}
	c.changed(id)
}

// clear deletes every message, returning how many there were, and wakes
// every session on the chat screen, so that it shows them gone.
func (c *Chat) clear() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.messages)
	c.messages = nil
	c.changed(0)
	return n
}

// count is how many messages are kept.
func (c *Chat) count() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// changed records a change and wakes every session on the chat screen but
// session except, whose read deadline passes, so that it redraws. c.mu must
// be held.
func (c *Chat) changed(except uint64) {
	c.version++
	for id, w := range c.watchers {
		if id != except {
			_ = w.conn.SetReadDeadline(time.Now())
		}
	}
}

// snapshot is the messages kept, the version they are of (see latest), and
// the LUs of the sessions on the chat screen, sorted.
func (c *Chat) snapshot() (messages []ChatMessage, version int, here []string) {
	if c == nil {
		return nil, 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.watchers {
		here = append(here, w.lu)
	}
	slices.SortFunc(here, cmp.Compare[string])
	return slices.Clone(c.messages), c.version, here
}

// latest counts the changes to the messages, each posted and each clearing,
// so that a session can tell whether what it drew is out of date.
func (c *Chat) latest() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Chat screen layout: the heading, the messages from the row below it down
// to above the input row, then the input, message and help rows.
const (
	chatHeaderRow = 1
	chatFirstRow  = 2
	chatField     = "chat"
	chatLabel     = "Message ===>"
	chatPrompt    = "Type a message and press Enter to send it."
)

// chatState is one session's place on the chat screen.
type chatState struct {
	// scroll is how many lines back from the newest the bottom line shown
	// is, zero for the newest. lines is how many lines the messages made
	// when last drawn, so that new ones arriving while scrolled back do not
	// move what is shown.
	scroll, lines int

	// shown is the chat's version (see Chat.latest) when last drawn.
	shown int

	message string
	isError bool
}

// chatRows is how many lines of messages fit on the chat screen.
func chatRows(rows int) int {
	return max(rows-3-chatFirstRow, 1)
}

// chatLine is one line of a message: the time and poster on its first line,
// then text.
type chatLine struct {
	when, from, text string
	own              bool
}

// chatPrefixWidth is the width of a message's time and poster, "15:04
// AD000001:", with the space after it.
const chatPrefixWidth = 16

// chatLines wraps messages into lines of at most width runes, the text after
// the time and poster, continuation lines indented to it. Lines are broken
// at spaces where they can be. own is the LU whose messages are marked.
func chatLines(messages []ChatMessage, width int, own string) []chatLine {
	textWidth := max(width-chatPrefixWidth, 10)
	var out []chatLine
	for _, m := range messages {
		first := true
		for _, part := range wrapText(m.Text, textWidth) {
			l := chatLine{text: part, own: m.From == own}
			if first {
				l.when, l.from, first = m.Time.Format("15:04"), m.From, false
			}
			out = append(out, l)
		}
	}
	return out
}

// wrapText breaks s into lines of at most width runes, at the last space
// that fits, or mid-word when none does. It always returns at least one
// line.
func wrapText(s string, width int) []string {
	var out []string
	r := []rune(s)
	for len(r) > width {
		cut := width
		for i := width; i > 0; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = r[cut:]
		for len(r) > 0 && r[0] == ' ' {
			r = r[1:]
		}
	}
	return append(out, string(r))
}

// buildChat renders the chat as lu sees it, and where the cursor goes: the
// input field.
func buildChat(rows, cols int, now time.Time, chat *Chat, lu string, c *chatState) (screen go3270.Screen, cursorRow, cursorCol int) {
	messages, version, here := chat.snapshot()
	c.shown = version
	lines := chatLines(messages, cols-1, lu)
	if c.scroll > 0 {
		c.scroll += len(lines) - c.lines // keep what is shown in place
	}
	c.lines = len(lines)
	perPage := chatRows(rows)
	c.scroll = min(max(c.scroll, 0), max(len(lines)-perPage, 0))
	end := len(lines) - c.scroll
	start := max(end-perPage, 0)

	screen = titleFields(cols, "CHAT", now, false)
	info := fmt.Sprintf("as %s; %d here: %s", lu, len(here), strings.Join(here, " "))
	if c.scroll > 0 {
		info += fmt.Sprintf("; %d newer lines, PF8", c.scroll)
	}
	screen = append(screen, placeLine(chatHeaderRow, cols, line{
		{Content: "CHAT", Color: go3270.Turquoise, Intense: true},
		{Content: info, Color: go3270.Blue},
	})...)

	if len(lines) == 0 {
		screen = append(screen, placeLine(chatFirstRow, cols, line{{Content: "No messages yet.", Color: go3270.Blue}})...)
	}
	// The newest lines at the bottom, just above the input row.
	row := chatFirstRow + perPage - (end - start)
	for _, l := range lines[start:end] {
		fromColor := go3270.Turquoise
		if l.own {
			fromColor = go3270.White
		}
		var ln line
		if l.from != "" {
			ln = line{
				{Content: l.when, Color: go3270.Blue},
				{Content: fmt.Sprintf("%-*s", chatPrefixWidth-7, l.from+":"), Color: fromColor, Intense: l.own},
				{Content: l.text, Color: go3270.Green, Intense: true},
			}
		} else {
			ln = line{{Content: strings.Repeat(" ", chatPrefixWidth-1)}, {Content: l.text, Color: go3270.Green, Intense: true}}
		}
		screen = append(screen, placeLine(row, cols, ln)...)
		row++
	}

	inputRow, inputCol := rows-3, len(chatLabel)+1
	screen = append(screen,
		go3270.Field{Row: inputRow, Col: 0, Color: go3270.Turquoise, Content: chatLabel},
		go3270.Field{
			Row: inputRow, Col: inputCol, Write: true, Name: chatField,
			Color: go3270.Yellow, Intense: true, Highlighting: go3270.Underscore,
		},
		// Ends the input field at the edge of the screen.
		go3270.Field{Row: inputRow, Col: cols - 1},
	)
	message, color := c.message, go3270.Red
	if message == "" {
		message, color = chatPrompt, go3270.Blue
	}
	screen = append(screen, placeLine(rows-2, cols, line{{Content: message, Color: color, Intense: c.isError}})...)
	screen = append(screen, go3270.Field{
		Row: rows - 1, Col: 0, Color: go3270.Blue,
		Content: truncate("PF3=Back PF7=Older PF8=Newer Enter=Send", cols-1),
	})
	return screen, inputRow, inputCol + 1
}

// handle acts on a key pressed on the chat screen, reporting whether to
// leave for the dashboard. Enter sends what was typed, if anything, and
// goes back to the newest messages; PF7 and PF8 scroll a page older and
// newer.
func (c *chatState) handle(resp go3270.Response, chat *Chat, id uint64, lu string, rows int) (leave bool) {
	c.message, c.isError = "", false
	page := max(chatRows(rows)-1, 1)
	switch resp.AID {
	case go3270.AIDPF3:
		return true
	case go3270.AIDPF7:
		c.scroll += page // the next redraw keeps it to the lines there are
	case go3270.AIDPF8:
		c.scroll = max(c.scroll-page, 0)
	case go3270.AIDEnter:
		text := strings.TrimSpace(resp.Values[chatField])
		if text == "" {
			break
		}
		chat.post(id, lu, text, time.Now())
		c.scroll = 0
	}
	return false
}
