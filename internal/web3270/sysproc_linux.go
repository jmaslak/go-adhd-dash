// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package web3270

import "syscall"

// sysProcAttr has web3270 ended should the server die without stopping it,
// so that it does not keep its port from the next one.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
