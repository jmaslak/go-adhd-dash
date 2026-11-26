// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package tcpconn

import (
	"errors"
	"net"
	"testing"
	"time"
)

// TestListenerTunes checks that a connection accepted through NewListener
// has the unacknowledged-data bound set, where the system has one.
func TestListenerTunes(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	ln := NewListener(inner, func(f string, args ...any) { logged = append(logged, f) })
	defer ln.Close() //nolint:errcheck

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close() //nolint:errcheck
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck
	if len(logged) != 0 {
		t.Errorf("logged %v, want nothing", logged)
	}

	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got time.Duration
	var getErr error
	if err := raw.Control(func(fd uintptr) { got, getErr = unackedTimeout(fd) }); err != nil {
		t.Fatal(err)
	}
	if errors.Is(getErr, errors.ErrUnsupported) {
		t.Skip("no unacknowledged-data bound on this system")
	}
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got != DeadAfter {
		t.Errorf("unacknowledged-data bound %v, want %v", got, DeadAfter)
	}
}

// TestTuneNotTCP checks that a connection that is not TCP is left alone.
func TestTuneNotTCP(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck
	defer b.Close() //nolint:errcheck
	if err := Tune(a); err != nil {
		t.Errorf("Tune(pipe) = %v, want nil", err)
	}
}
