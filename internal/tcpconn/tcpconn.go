// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package tcpconn makes accepted TCP connections notice a client that has
// gone away without closing them: one whose network now drops everything
// sent to it.
//
// Go's own keepalives do not do this for a session. A keepalive probe is
// sent only while the connection is idle, and a dashboard redrawn every
// second never is: each redraw sits unacknowledged and is retransmitted,
// for the kernel's default of 15 minutes or more on Linux, and the session
// stays meanwhile. So the time data may go unacknowledged is bounded too
// (TCP_USER_TIMEOUT on Linux, TCP_RXT_CONNDROPTIME on macOS), and the
// connection's next read or write fails once it passes.
package tcpconn

import (
	"errors"
	"net"
	"time"
)

// DeadAfter is about how long a client may go without acknowledging
// anything before its connection is dropped: long enough to ride out a
// brief network outage or a laptop's Wi-Fi changing, short enough that a
// session gone for good is not mistaken for a live one for long.
const DeadAfter = time.Minute

// keepAlive probes an idle connection, as on the login screen or a menu
// that is not redrawn, so that it is dropped in about DeadAfter too.
var keepAlive = net.KeepAliveConfig{
	Enable:   true,
	Idle:     20 * time.Second,
	Interval: 10 * time.Second,
	Count:    4,
}

// Tune sets c's keepalives and bounds how long what is sent to it may go
// unacknowledged. A connection that is not TCP is left alone.
func Tune(c net.Conn) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil
	}
	kaErr := tc.SetKeepAliveConfig(keepAlive)
	raw, err := tc.SyscallConn()
	if err != nil {
		return errors.Join(kaErr, err)
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) { setErr = setUnackedTimeout(fd, DeadAfter) }); err != nil {
		return errors.Join(kaErr, err)
	}
	return errors.Join(kaErr, setErr)
}

// listener tunes each connection it accepts.
type listener struct {
	net.Listener
	logf func(format string, args ...any)
}

// NewListener returns ln, with each connection it accepts tuned (see
// Tune). A connection that cannot be is still accepted, as it was before,
// and the failure logged with logf.
func NewListener(ln net.Listener, logf func(format string, args ...any)) net.Listener {
	return listener{Listener: ln, logf: logf}
}

// Accept accepts the next connection and tunes it.
func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err := Tune(c); err != nil {
		l.logf("tuning connection from %s: %v", c.RemoteAddr(), err)
	}
	return c, nil
}
