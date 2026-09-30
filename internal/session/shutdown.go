package session

import (
	"net"
	"sync"
	"time"
)

// Shutdown stops the server on request: every session is told to say
// goodbye and disconnect, and Wait returns once they all have.
//
// The only file the server writes, the checklists, is written by a session
// between one key and the next screen, as are the changes it sends Trello,
// so a session that has returned has finished writing; once Wait returns
// nothing is left half done.
type Shutdown struct {
	mu        sync.Mutex
	requested bool
	conns     map[net.Conn]struct{} // the sessions', to wake or close
	sessions  sync.WaitGroup
	done      chan struct{} // closed when shutdown is requested
}

// NewShutdown returns a Shutdown not yet requested.
func NewShutdown() *Shutdown {
	return &Shutdown{conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
}

// add counts a session on conn, reporting false, and counting nothing, once
// shutdown has been requested. A nil Shutdown counts nothing and never
// refuses.
func (s *Shutdown) add(conn net.Conn) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return false
	}
	s.conns[conn] = struct{}{}
	s.sessions.Add(1)
	return true
}

// remove stops counting the session on conn, which add counted.
func (s *Shutdown) remove(conn net.Conn) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.sessions.Done()
}

// Sessions is how many sessions are connected.
func (s *Shutdown) Sessions() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Request asks for the server to shut down. Each session waiting for a key
// is woken by its read deadline passing, sees the request, and disconnects;
// one busy with a key does so when it is done with it. Asking again does
// nothing more.
func (s *Shutdown) Request() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return
	}
	s.requested = true
	close(s.done)
	for c := range s.conns {
		_ = c.SetReadDeadline(time.Now())
	}
}

// Requested reports whether shutdown has been requested. A nil Shutdown
// never is.
func (s *Shutdown) Requested() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requested
}

// Done is closed when shutdown is requested.
func (s *Shutdown) Done() <-chan struct{} { return s.done }

// Wait waits for every session to disconnect after shutdown is requested.
// Those still connected after grace, their terminals slow to take the
// goodbye screen or their deadline missed, have their connections closed,
// which ends a session at its next read or write but never in the middle of
// writing a file; Wait still waits for them to return.
func (s *Shutdown) Wait(grace time.Duration) {
	finished := make(chan struct{})
	go func() {
		s.sessions.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return
	case <-time.After(grace):
	}
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	<-finished
}
