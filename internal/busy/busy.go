// Package busy follows the status feed of a busy indicator
// (github.com/jmaslak/go-busy-indicator, run with --ws-port), keeping the
// latest status for the dashboard to show.
package busy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// reconnectDelay is how long to wait before redialing a feed that failed or
// closed.
const reconnectDelay = 5 * time.Second

// Status is the indicator's state as last reported.
type Status struct {
	// Connected is false while the feed is unreachable; the other fields
	// then describe the last status received, if any.
	Connected bool

	// Light is "red", "green" or "off", or empty before the first message.
	Light string

	// MinutesToNext is the whole minutes until the current or next meeting,
	// or nil when nothing remains today.
	MinutesToNext *int

	// Updated is when Light and MinutesToNext were received.
	Updated time.Time

	// Err is why the feed is not connected.
	Err error

	// Problem is a short, screen-sized form of Err.
	Problem string
}

// Source supplies the indicator's latest status.
type Source interface {
	Status() Status
}

// message is the feed's JSON.
type message struct {
	Type          string `json:"type"`
	Status        string `json:"status"`
	MinutesToNext *int   `json:"minutes-to-next"`
}

// Watcher keeps a connection to the feed open, redialing as needed. It is
// safe for concurrent use.
type Watcher struct {
	url string

	mu     sync.Mutex
	status Status
}

// NewWatcher returns a watcher for the feed at url, such as
// ws://localhost:3334/feed. Call Run to start it.
func NewWatcher(url string) *Watcher {
	return &Watcher{url: url, status: Status{Err: errors.New("not yet connected"), Problem: "connecting"}}
}

// Status returns the latest status.
func (w *Watcher) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Run follows the feed until ctx is canceled.
func (w *Watcher) Run(ctx context.Context) {
	for {
		connected, err := w.follow(ctx)
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		// Log changes only, so an indicator that stays down does not fill
		// the log every few seconds.
		if w.status.Connected || w.status.Err == nil || w.status.Err.Error() != err.Error() {
			log.Printf("busy indicator feed %s: %v", w.url, err)
		}
		w.status.Connected = false
		w.status.Err = err
		w.status.Problem = "not reachable at " + w.url
		if connected {
			w.status.Problem = "connection lost, retrying"
		}
		w.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// follow reads status messages from one connection until it fails,
// reporting whether it connected at all.
func (w *Watcher) follow(ctx context.Context) (bool, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, w.url, nil)
	cancel()
	if err != nil {
		return false, err
	}
	defer conn.CloseNow() //nolint:errcheck

	for {
		var msg message
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return true, err
		}
		// Anything but a status is an echo of something a client sent.
		if msg.Type != "status" {
			continue
		}

		w.mu.Lock()
		w.status = Status{
			Connected:     true,
			Light:         msg.Status,
			MinutesToNext: msg.MinutesToNext,
			Updated:       time.Now(),
		}
		w.mu.Unlock()
	}
}

// File reads the status from a JSON file shaped like the feed's messages,
// for trying the dashboard without an indicator:
//
//	{"status": "red", "minutes-to-next": 20}
//
// The file is re-read every time the status is asked for, so an edit shows
// at the next redraw. Its modification time stands in for when the status
// was received, so the minutes count down as they do from the feed.
type File struct {
	Path string
}

// Status reads the file, reporting a missing or malformed one as a feed that
// is not connected.
func (f File) Status() Status {
	info, err := os.Stat(f.Path)
	if err != nil {
		return Status{Err: err, Problem: "file " + f.Path + " unreadable"}
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return Status{Err: err, Problem: "file " + f.Path + " unreadable"}
	}
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Status{Err: fmt.Errorf("parsing %s: %w", f.Path, err), Problem: "file " + f.Path + " is not valid JSON"}
	}
	return Status{Connected: true, Light: msg.Status, MinutesToNext: msg.MinutesToNext, Updated: info.ModTime()}
}

// Control sends keystrokes to a busy indicator's UDP control port
// (busy-indicator --port), as its busy command does: "b" turns the light red,
// "o" turns it off until the next meeting.
type Control struct {
	// Addr is the control port's host:port, such as localhost:3333.
	Addr string
}

// Send sends key. UDP is not acknowledged, so a nil error means only that
// the datagram went out, not that an indicator received it.
func (c Control) Send(key rune) error {
	conn, err := net.Dial("udp", c.Addr)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck
	_, err = conn.Write([]byte("KEY " + string(key)))
	return err
}
