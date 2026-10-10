// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

//go:build !unix

package web3270

import "os"

// terminate ends p; there is no asking it here.
func terminate(p *os.Process) {
	p.Kill() //nolint:errcheck
}
