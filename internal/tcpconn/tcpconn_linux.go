// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package tcpconn

import (
	"time"

	"golang.org/x/sys/unix"
)

// setUnackedTimeout sets TCP_USER_TIMEOUT, in milliseconds: how long sent
// data may go unacknowledged, keepalive probes included, before the kernel
// drops the connection.
func setUnackedTimeout(fd uintptr, d time.Duration) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(d.Milliseconds()))
}
