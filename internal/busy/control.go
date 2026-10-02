package busy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

// The UDP control port speaks go-busy-indicator's protocol, so that its
// busy command can drive this light instead. Each datagram is
//
//	KEY <char>    a key, as Key takes
//
// and anything else is ignored. There is no authentication: anything that
// can reach the port can set the light.
const maxDatagram = 1024

// parseControl decodes a datagram's key.
func parseControl(payload string) (key rune, ok bool) {
	if k, found := strings.CutPrefix(payload, "KEY "); found {
		if r, size := utf8.DecodeRuneInString(k); size == len(k) && r != utf8.RuneError {
			return r, true
		}
	}
	return 0, false
}

// ListenControl serves the control port at addr until ctx is canceled.
func (i *Indicator) ListenControl(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("busy light: control port %s: %w", addr, err)
	}
	i.ServeControl(ctx, conn)
	return nil
}

// ServeControl reads control datagrams from conn until ctx is canceled,
// closing it then.
func (i *Indicator) ServeControl(ctx context.Context, conn net.PacketConn) {
	go func() {
		<-ctx.Done()
		conn.Close() //nolint:errcheck
	}()
	buf := make([]byte, maxDatagram)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil {
				i.logf("busy light: control port: %v", err)
			}
			return
		}
		if key, ok := parseControl(string(buf[:n])); ok {
			if err := i.Key(key); err != nil {
				i.logf("busy light: control port: %v", err)
			}
		}
	}
}
