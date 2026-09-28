package busy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestWatcherFollowsFeed(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		minutes := 12
		_ = wsjson.Write(r.Context(), conn, map[string]any{"type": "echo"})
		_ = wsjson.Write(r.Context(), conn, message{Type: "status", Status: "red", MinutesToNext: &minutes})
		<-release
	}))
	defer srv.Close()
	defer close(release)

	w := NewWatcher("ws" + strings.TrimPrefix(srv.URL, "http") + "/feed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := w.Status()
		if s.Connected {
			if s.Light != "red" || s.MinutesToNext == nil || *s.MinutesToNext != 12 {
				t.Fatalf("status = %+v", s)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never connected: %+v", w.Status())
}

func TestWatcherReportsUnreachable(t *testing.T) {
	w := NewWatcher("ws://127.0.0.1:1/feed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := w.Status(); s.Problem != "connecting" {
			if s.Connected || s.Err == nil || s.Problem != "not reachable at ws://127.0.0.1:1/feed" {
				t.Fatalf("status = %+v", s)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dial failure never reported")
}

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.json")
	f := File{Path: path}
	if s := f.Status(); s.Connected || s.Err == nil || !strings.Contains(s.Problem, "unreadable") {
		t.Errorf("missing file: %+v", s)
	}

	if err := os.WriteFile(path, []byte(`{"status": "red", "minutes-to-next": 20}`), 0o644); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	s := f.Status()
	if !s.Connected || s.Light != "red" || s.MinutesToNext == nil || *s.MinutesToNext != 20 || !s.Updated.Equal(written) {
		t.Errorf("status = %+v", s)
	}

	if err := os.WriteFile(path, []byte(`{"status": "green"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := f.Status(); s.Light != "green" || s.MinutesToNext != nil {
		t.Errorf("after edit, status = %+v; want green with no meetings left", s)
	}

	if err := os.WriteFile(path, []byte(`red`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := f.Status(); s.Connected || !strings.Contains(s.Problem, "not valid JSON") {
		t.Errorf("malformed file: %+v", s)
	}
}

func TestControlSend(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck

	if err := (Control{Addr: conn.LocalAddr().String()}).Send('b'); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "KEY b" {
		t.Errorf("sent %q, want \"KEY b\"", got)
	}

	if err := (Control{Addr: "no-such-host.invalid:1"}).Send('o'); err == nil {
		t.Errorf("sending to an unresolvable host succeeded")
	}
}
