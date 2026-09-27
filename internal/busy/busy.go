// Package busy follows the status feed of a busy indicator
// (github.com/jmaslak/go-busy-indicator, run with --ws-port), keeping the
// latest status for the dashboard to show.
package busy

import (
	"context"
	"errors"
	"log"
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
