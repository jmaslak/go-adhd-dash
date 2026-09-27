package busy

import (
	"context"
	"net/http"
	"net/http/httptest"
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
