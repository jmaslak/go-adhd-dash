package session

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"
)

func TestWrapText(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  []string
	}{
		{"", 10, []string{""}},
		{"short", 10, []string{"short"}},
		{"exactly10!", 10, []string{"exactly10!"}},
		{"the quick brown fox jumps", 10, []string{"the quick", "brown fox", "jumps"}},
		{"abcdefghijklmnop", 10, []string{"abcdefghij", "klmnop"}},
		{"a   b", 2, []string{"a", "b"}},
	} {
		if got := wrapText(c.in, c.width); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", c.want) {
			t.Errorf("%q at %d: %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestChatPostWakesWatchers(t *testing.T) {
	c := NewChat()
	poster, posterPeer := net.Pipe()
	watcher, watcherPeer := net.Pipe()
	defer posterPeer.Close()  //nolint:errcheck
	defer watcherPeer.Close() //nolint:errcheck
	c.watch(1, "AD000001", poster)
	c.watch(2, "AD000002", watcher)

	woken := make(chan error, 2)
	for _, conn := range []net.Conn{poster, watcher} {
		go func() {
			_, err := conn.Read(make([]byte, 1))
			woken <- err
		}()
	}
	c.post(1, "AD000001", "hello", now)
	select {
	case err := <-woken:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("read ended with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher not woken")
	}
	select {
	case <-woken:
		t.Error("poster woken by its own message")
	case <-time.After(50 * time.Millisecond):
	}

	messages, seq, here := c.snapshot()
	if seq != 1 || len(messages) != 1 || messages[0].From != "AD000001" || messages[0].Text != "hello" || fmt.Sprint(here) != "[AD000001 AD000002]" {
		t.Errorf("snapshot %+v, %d, %v", messages, seq, here)
	}
	c.unwatch(2)
	if _, _, here := c.snapshot(); fmt.Sprint(here) != "[AD000001]" {
		t.Errorf("after unwatching: %v", here)
	}

	for i := range chatKeep + 5 {
		c.post(1, "AD000001", fmt.Sprint(i), now)
	}
	if messages, seq, _ := c.snapshot(); len(messages) != chatKeep || seq != chatKeep+6 || messages[0].Text != "5" || c.latest() != seq {
		t.Errorf("kept %d, seq %d, first %q", len(messages), seq, messages[0].Text)
	}

	var nilChat *Chat
	nilChat.watch(1, "x", poster)
	nilChat.post(1, "x", "y", now)
	nilChat.unwatch(1)
	if m, seq, _ := nilChat.snapshot(); m != nil || seq != 0 || nilChat.latest() != 0 {
		t.Error("nil chat has messages")
	}
}

func TestChatScreen(t *testing.T) {
	c := NewChat()
	conn, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	c.watch(2, "AD000002", conn)
	c.post(1, "AD000001", "hello there", now.Add(-time.Minute))
	c.post(2, "AD000002", "hi! "+strings.Repeat("word ", 20), now)

	var st chatState
	s, crow, ccol := buildChat(24, 80, now, c, "AD000002", &st)
	rows := screenText(t, s, 24, 80)
	for i, want := range map[int]string{
		chatHeaderRow: " CHAT as AD000002; 1 here: AD000002",
		18:            " 09:59 AD000001: hello there",
		19:            " 10:00 AD000002: hi! word word word word word word word word word word word word",
		20:            "                 word word word word word word word word",
		21:            " Message ===>",
	} {
		if rows[i] != want {
			t.Errorf("row %d is %q, want %q", i, rows[i], want)
		}
	}
	if crow != 21 || ccol != len(chatLabel)+2 || st.shown != 2 {
		t.Errorf("cursor at %d,%d, shown %d", crow, ccol, st.shown)
	}
	var own go3270.Field
	for _, f := range s {
		if f.Row == 19 && strings.HasPrefix(f.Content, "AD000002") {
			own = f
		}
	}
	if own.Color != go3270.White {
		t.Errorf("own name not marked: %+v", own)
	}

	s, _, _ = buildChat(24, 80, now, NewChat(), "AD000001", &chatState{})
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "No messages yet.") {
		t.Errorf("empty chat:\n%s", text)
	}
}

func TestChatScrolling(t *testing.T) {
	c := NewChat()
	for i := range 40 {
		c.post(1, "AD000001", fmt.Sprint("message ", i), now)
	}
	var st chatState
	enter := func(aid go3270.AID, typed string) bool {
		return st.handle(go3270.Response{AID: aid, Values: map[string]string{chatField: typed}}, c, 2, "AD000002", 24)
	}
	buildChat(24, 80, now, c, "AD000002", &st)

	enter(go3270.AIDPF7, "")
	s, _, _ := buildChat(24, 80, now, c, "AD000002", &st)
	rows := screenText(t, s, 24, 80)
	if st.scroll != 18 || !strings.HasSuffix(rows[20], "message 21") || !strings.Contains(rows[chatHeaderRow], "18 newer lines, PF8") {
		t.Errorf("after PF7: scroll %d, bottom %q, header %q", st.scroll, rows[20], rows[chatHeaderRow])
	}

	// A message arriving while scrolled back leaves the view where it is.
	c.post(1, "AD000001", "late", now)
	s, _, _ = buildChat(24, 80, now, c, "AD000002", &st)
	if rows := screenText(t, s, 24, 80); !strings.HasSuffix(rows[20], "message 21") {
		t.Errorf("view moved by a new message: bottom %q", rows[20])
	}

	enter(go3270.AIDPF7, "")
	enter(go3270.AIDPF7, "")
	enter(go3270.AIDPF7, "")
	s, _, _ = buildChat(24, 80, now, c, "AD000002", &st)
	if rows := screenText(t, s, 24, 80); !strings.HasSuffix(rows[chatFirstRow], "message 0") {
		t.Errorf("scrolled past the oldest: top %q", rows[chatFirstRow])
	}

	// Sending goes back to the newest; a blank Enter sends nothing.
	if enter(go3270.AIDEnter, "   ") || c.latest() != 41 {
		t.Error("blank message sent")
	}
	enter(go3270.AIDEnter, " mine ")
	if messages, _, _ := c.snapshot(); st.scroll != 0 || messages[len(messages)-1].Text != "mine" || messages[len(messages)-1].From != "AD000002" {
		t.Errorf("after sending: scroll %d, last %+v", st.scroll, messages[len(messages)-1])
	}
	enter(go3270.AIDPF8, "")
	if st.scroll != 0 {
		t.Errorf("PF8 past the newest: scroll %d", st.scroll)
	}
	if !enter(go3270.AIDPF3, "") {
		t.Error("PF3 does not leave")
	}
}

func TestChatClear(t *testing.T) {
	c := NewChat()
	watcher, peer := net.Pipe()
	defer peer.Close() //nolint:errcheck
	c.watch(2, "AD000002", watcher)
	c.post(1, "AD000001", "one", now)
	c.post(1, "AD000001", "two", now)
	_ = watcher.SetReadDeadline(time.Time{}) // as the session does before waiting

	var st chatState
	buildChat(24, 80, now, c, "AD000002", &st)
	woken := make(chan error, 1)
	go func() {
		_, err := watcher.Read(make([]byte, 1))
		woken <- err
	}()
	if n := c.clear(); n != 2 || c.count() != 0 {
		t.Errorf("cleared %d, %d left", n, c.count())
	}
	select {
	case err := <-woken:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("read ended with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher not woken by clearing")
	}
	// A session that drew the chat before it was cleared can tell.
	if c.latest() == st.shown {
		t.Error("clearing not seen as a change")
	}
	s, _, _ := buildChat(24, 80, now, c, "AD000002", &st)
	if text := strings.Join(screenText(t, s, 24, 80), "\n"); !strings.Contains(text, "No messages yet.") || strings.Contains(text, "one") {
		t.Errorf("after clearing:\n%s", text)
	}
	// Numbering goes on from where it was.
	c.post(1, "AD000001", "three", now)
	if m, _, _ := c.snapshot(); m[0].Seq != 3 {
		t.Errorf("first message after clearing numbered %d", m[0].Seq)
	}

	text := strings.Join(screenText(t, buildClearChatConfirm(24, 80, now, 2), 24, 80), "\n")
	for _, want := range []string{"Clear the chat?", "This deletes 2 messages, for everyone.", "PF4 to clear", "PF4=Clear"} {
		if !strings.Contains(text, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, text)
		}
	}
	var nilChat *Chat
	if nilChat.clear() != 0 || nilChat.count() != 0 {
		t.Error("nil chat has messages")
	}
}
