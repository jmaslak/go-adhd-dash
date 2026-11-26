// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

//go:build !linux && !darwin

package tcpconn

import (
	"errors"
	"time"
)

// unackedTimeout is never set here.
func unackedTimeout(uintptr) (time.Duration, error) { return 0, errors.ErrUnsupported }
