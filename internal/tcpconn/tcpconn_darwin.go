// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package tcpconn

import (
	"time"

	"golang.org/x/sys/unix"
)

// setUnackedTimeout sets TCP_RXT_CONNDROPTIME, in seconds: how long the
// kernel retransmits unacknowledged data before dropping the connection.
func setUnackedTimeout(fd uintptr, d time.Duration) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_RXT_CONNDROPTIME, int(d.Seconds()))
}
