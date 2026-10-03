//go:build unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Lock takes the lock at path, made if need be, for as long as this
// process holds it, or until the function returned is called. It fails
// with ErrInUse if another process holds it.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s is locked)", ErrInUse, path)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() { f.Close() }, nil //nolint:errcheck // closing releases it
}
