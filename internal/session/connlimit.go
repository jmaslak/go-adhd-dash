package session

import (
	"log"
	"net"
	"sync"
	"time"
)

// ConnLimiter caps how many connections are open at once, in all and from
// any one address, so that one client, or a few, cannot tie up the server
// by opening connections and leaving them at the login screen. It is
// checked before a connection is negotiated, and is safe for concurrent
// use. A nil ConnLimiter admits everything.
//
// Connections from this machine are not counted: were they, a flood from
// elsewhere could fill every place and lock out an admin connecting from
// here.
type ConnLimiter struct {
	total, perAddr int // zero for no limit

	mu     sync.Mutex
	open   int
	byAddr map[string]int

	// Refusals are logged at most once per refusalLogEvery, the rest
	// counted, so that a flood cannot flood the log too.
	lastLog time.Time
	unseen  int
	now     func() time.Time
}

// refusalLogEvery is how often refused connections may be logged.
const refusalLogEvery = 10 * time.Second

// NewConnLimiter returns a limiter allowing total connections at once, and
// perAddr from any one address (an IPv6 address by its /64, which one host
// usually has the whole of); zero for no limit.
func NewConnLimiter(total, perAddr int) *ConnLimiter {
	return &ConnLimiter{total: total, perAddr: perAddr, byAddr: map[string]int{}, now: time.Now}
}

// limitKey is the address addr is counted under: an IPv4 address as it is,
// an IPv6 one by its /64, and "" for this machine, which is not counted.
func limitKey(addr net.Addr) string {
	if isLocal(addr) {
		return ""
	}
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return host
	case ip.To4() != nil:
		return ip.To4().String()
	default:
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
}

// admit counts a connection from addr, returning the function that stops
// counting it, or nil if it would go over a limit, which is logged.
func (l *ConnLimiter) admit(addr net.Addr) (release func()) {
	if l == nil {
		return func() {}
	}
	key := limitKey(addr)
	if key == "" {
		return func() {}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var why string
	switch {
	case l.total > 0 && l.open >= l.total:
		why = "too many connections"
	case l.perAddr > 0 && l.byAddr[key] >= l.perAddr:
		why = "too many connections from " + key
	}
	if why != "" {
		l.refused(addr, why)
		return nil
	}
	l.open++
	l.byAddr[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.open--
			if l.byAddr[key]--; l.byAddr[key] <= 0 {
				delete(l.byAddr, key)
			}
		})
	}
}

// refused logs a connection refused, or counts it to log later. l.mu must
// be held.
func (l *ConnLimiter) refused(addr net.Addr, why string) {
	now := l.now()
	if now.Sub(l.lastLog) < refusalLogEvery {
		l.unseen++
		return
	}
	if l.unseen > 0 {
		log.Printf("refused %d more connections over the limits", l.unseen)
	}
	log.Printf("refused connection from %s: %s", addr, why)
	l.lastLog, l.unseen = now, 0
}
