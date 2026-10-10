//go:build unix

package web3270

import (
	"os"
	"syscall"
)

// terminate asks p to end.
func terminate(p *os.Process) {
	p.Signal(syscall.SIGTERM) //nolint:errcheck
}
