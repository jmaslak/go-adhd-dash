package session

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLimitKey(t *testing.T) {
	for addr, want := range map[string]string{
		"192.0.2.7:5000":              "192.0.2.7",
		"[::ffff:192.0.2.7]:5000":     "192.0.2.7",
		"[2001:db8:1:2:aaaa::1]:5000": "2001:db8:1:2::/64",
		"[2001:db8:1:2:bbbb::9]:5000": "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:5000":      "2001:db8:1:3::/64",
		"127.0.0.1:5000":              "",
		"[::1]:5000":                  "",
	} {
		if got := limitKey(fakeAddr(addr)); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}

func TestConnLimiter(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	defer log.SetFlags(log.LstdFlags)
	log.SetFlags(0)

	l := NewConnLimiter(3, 2)
	clock := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }

	a1 := l.admit(fakeAddr("192.0.2.1:1"))
	a2 := l.admit(fakeAddr("192.0.2.1:2"))
	if a1 == nil || a2 == nil {
		t.Fatal("first two from one address refused")
	}
	if l.admit(fakeAddr("192.0.2.1:3")) != nil {
		t.Error("third from one address admitted, over the per-address cap")
	}
	// The same /64, by another address, is the same place.
	v1 := l.admit(fakeAddr("[2001:db8::1]:1"))
	if v1 == nil || l.admit(fakeAddr("[2001:db8::2]:1")) != nil {
		t.Error("IPv6 not counted by its /64")
	}
	if l.admit(fakeAddr("198.51.100.1:1")) != nil {
		t.Error("admitted over the total cap")
	}
	// This machine is not counted, even over the caps.
	for range 10 {
		if l.admit(fakeAddr("127.0.0.1:9")) == nil || l.admit(fakeAddr("[::1]:9")) == nil {
			t.Fatal("local connection refused")
		}
	}
	a1()
	a1() // twice is once
	if l.open != 2 || l.byAddr["192.0.2.1"] != 1 {
		t.Errorf("after release: open %d, by address %v", l.open, l.byAddr)
	}
	if l.admit(fakeAddr("198.51.100.1:1")) == nil {
		t.Error("refused with room")
	}

	// One line for the first refusal; the rest counted, and told with the
	// next line logged, after refusalLogEvery.
	if n := strings.Count(logged.String(), "refused connection from"); n != 1 {
		t.Errorf("%d refusals logged, want 1:\n%s", n, logged.String())
	}
	clock = clock.Add(refusalLogEvery)
	l.admit(fakeAddr("192.0.2.1:4"))
	l.admit(fakeAddr("192.0.2.1:5"))
	if !strings.Contains(logged.String(), "refused 2 more connections over the limits") || strings.Count(logged.String(), "refused connection from") != 2 {
		t.Errorf("log:\n%s", logged.String())
	}

	var nilLimiter *ConnLimiter
	if release := nilLimiter.admit(fakeAddr("192.0.2.1:1")); release == nil {
		t.Error("nil limiter refused")
	}
	unlimited := NewConnLimiter(0, 0)
	for range 100 {
		if unlimited.admit(fakeAddr("192.0.2.1:1")) == nil {
			t.Fatal("no limits, yet refused")
		}
	}
}
