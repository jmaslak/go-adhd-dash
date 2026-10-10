// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

//go:build !unix

package backup

// Lock does nothing where there is no flock: a restore is not stopped by
// a server running.
func Lock(string) (unlock func(), err error) {
	return func() {}, nil
}
