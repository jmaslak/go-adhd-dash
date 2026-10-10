//go:build !linux

package web3270

import "syscall"

// sysProcAttr is nothing special: only Linux can end a child with its
// parent.
func sysProcAttr() *syscall.SysProcAttr { return nil }
