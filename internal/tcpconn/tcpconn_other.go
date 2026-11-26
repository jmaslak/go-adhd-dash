// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

//go:build !linux && !darwin

package tcpconn

import "time"

// setUnackedTimeout does nothing: this system has no option for it, and
// only the keepalives, and the kernel's own retransmission limit, apply.
func setUnackedTimeout(uintptr, time.Duration) error { return nil }
