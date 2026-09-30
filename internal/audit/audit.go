// Package audit keeps the audit log: a file of one line per event (a user
// logging in or out, or being disconnected), appended to and flushed to
// disk as each is written.
//
// A line is the time, the event, then key=value fields, a value quoted (as
// Go quotes strings) when it is empty or holds a space, quote or equals
// sign:
//
//	2026-09-29T20:50:01-06:00 LOGIN user=bob lu=AD000002 ip=192.168.1.5 session=2
package audit

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The events.
const (
	Login       = "LOGIN"
	LoginFailed = "LOGIN-FAILED"
	Logout      = "LOGOUT"
	Disconnect  = "DISCONNECT"
)

// Field is one key=value of an event.
type Field struct {
	Key, Value string
}

// F makes a Field.
func F(key, value string) Field { return Field{key, value} }

// Log is an open audit log, safe for concurrent use. A nil Log records
// nothing.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
	now  func() time.Time
}

// Open opens the audit log at path for appending, making it, readable only
// by its owner, if need be.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the audit log: %w", err)
	}
	return &Log{f: f, path: path, now: time.Now}, nil
}

// Record writes event, with fields, and flushes it to disk. A failure is
// reported on the server's log, as there is no one else to tell.
func (l *Log) Record(event string, fields ...Field) {
	if l == nil {
		return
	}
	var b strings.Builder
	b.WriteString(l.now().Format(time.RFC3339))
	b.WriteString(" ")
	b.WriteString(event)
	for _, f := range fields {
		b.WriteString(" ")
		b.WriteString(f.Key)
		b.WriteString("=")
		b.WriteString(quote(f.Value))
	}
	b.WriteString("\n")

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		log.Printf("audit log %s closed; lost: %s", l.path, strings.TrimSpace(b.String()))
		return
	}
	if _, err := l.f.WriteString(b.String()); err != nil {
		log.Printf("writing the audit log %s: %v; lost: %s", l.path, err, strings.TrimSpace(b.String()))
		return
	}
	if err := l.f.Sync(); err != nil {
		log.Printf("flushing the audit log %s: %v", l.path, err)
	}
}

// Close closes the log; later events are reported on the server's log.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// quote is v as a field's value: as it is, or quoted if it is empty or
// holds anything that would make the line ambiguous.
func quote(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"=\\\n\r") || !strconv.CanBackquote(v) {
		return strconv.Quote(v)
	}
	return v
}
