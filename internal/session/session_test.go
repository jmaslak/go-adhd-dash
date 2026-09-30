package session

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/busy"
)

// fakeIndicator is a busy indicator: a UDP control port that records the
// keys it is sent and, when publish is set, reports each as a new status.
type fakeIndicator struct {
	conn    net.PacketConn
	publish bool

	mu     sync.Mutex
	keys   []string
	status busy.Status
}

func newFakeIndicator(t *testing.T, publish bool) *fakeIndicator {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck
	f := &fakeIndicator{conn: conn, publish: publish, status: busy.Status{Connected: true, Light: "off", Updated: time.Now().Add(-time.Minute)}}
	go func() {
		buf := make([]byte, 64)
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.keys = append(f.keys, string(buf[:n]))
			if f.publish {
				f.status = busy.Status{Connected: true, Light: "red", Updated: time.Now()}
			}
			f.mu.Unlock()
		}
	}()
	return f
}

func (f *fakeIndicator) Status() busy.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeIndicator) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func TestSendBusyKey(t *testing.T) {
	ind := newFakeIndicator(t, true)
	cfg := Config{Busy: ind, BusyControl: &busy.Control{Addr: ind.conn.LocalAddr().String()}}
	if msg := sendBusyKey(cfg, 'b'); msg != "" {
		t.Errorf("message %q, want none", msg)
	}
	if got := ind.Status().Light; got != "red" {
		t.Errorf("returned before the feed reported the change: light %q", got)
	}
	if got := ind.sent(); len(got) != 1 || got[0] != "KEY b" {
		t.Errorf("sent %q", got)
	}

	silent := newFakeIndicator(t, false)
	cfg = Config{Busy: silent, BusyControl: &busy.Control{Addr: silent.conn.LocalAddr().String()}}
	if msg := sendBusyKey(cfg, 'o'); !strings.Contains(msg, "No change seen") {
		t.Errorf("no feed update: message %q", msg)
	}

	cfg = Config{BusyControl: &busy.Control{Addr: "no-such-host.invalid:1"}}
	if msg := sendBusyKey(cfg, 'o'); !strings.Contains(msg, "Could not send") {
		t.Errorf("unresolvable host: message %q", msg)
	}

	if msg := sendBusyKey(Config{}, 'b'); msg != "" {
		t.Errorf("without a control port: message %q", msg)
	}
}
