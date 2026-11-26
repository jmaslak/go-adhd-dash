// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package tcpconn

import (
	"time"

	"golang.org/x/sys/unix"
)

// unackedTimeout reads back what setUnackedTimeout set, for the tests.
func unackedTimeout(fd uintptr) (time.Duration, error) {
	v, err := unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	return time.Duration(v) * time.Millisecond, err
}
