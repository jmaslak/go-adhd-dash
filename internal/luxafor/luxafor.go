// Package luxafor drives Luxafor flag USB indicators over HID. It is
// go-busy-indicator's driver, brought here with the rest of its logic.
//
// Copyright © 2026 Joelle Maslak
// All Rights Reserved - See License
package luxafor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/telesma-app/hid"
)

// Luxafor flag USB identifiers and protocol constants.
const (
	VendorID  uint16 = 0x04d8
	ProductID uint16 = 0xf372

	// cmdSetColor sets a static color immediately.
	cmdSetColor byte = 0x01

	// LEDAll addresses every LED in the flag.
	LEDAll byte = 0xff

	// DefaultTimeout bounds a single write to the device.
	DefaultTimeout = time.Second
)

// ErrNoDevice is returned when no Luxafor flag is attached.
var ErrNoDevice = errors.New("luxafor: no device found")

// Report builds the HID output report that sets a static color.
//
// The leading byte is the report ID, which the Luxafor leaves unnumbered, so
// it is always zero. The two trailing bytes are the fade and repeat fields,
// unused for a static color.
func Report(led, r, g, b byte) []byte {
	return []byte{0x00, cmdSetColor, led, r, g, b, 0x00, 0x00}
}

// Flag is the set of attached Luxafor flags. The zero value is not usable;
// call New. Devices are opened on first use and reopened after an error, so
// the program survives the flag being unplugged and plugged back in.
//
// Flag is safe for concurrent use.
type Flag struct {
	vendorID  uint16
	productID uint16
	timeout   time.Duration

	mu     sync.Mutex
	open   []*hid.Device
	opened bool
}

// New returns a Flag addressing every attached Luxafor indicator.
func New() *Flag {
	return &Flag{vendorID: VendorID, productID: ProductID, timeout: DefaultTimeout}
}

// Indicate sets every attached flag to the given color. It reports an error
// if no device is attached or if no device accepted the color.
func (f *Flag) Indicate(r, g, b byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	err := f.write(r, g, b)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNoDevice) {
		return err
	}

	// A flag that has been unplugged, or that missed a transfer, needs its
	// handles reopened before a retry has any chance of working.
	f.closeLocked() //nolint:errcheck
	time.Sleep(250 * time.Millisecond)
	return f.write(r, g, b)
}

// write sends the color to every open device, opening them if needed.
func (f *Flag) write(r, g, b byte) error {
	if err := f.openLocked(); err != nil {
		return err
	}

	report := Report(LEDAll, r, g, b)
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()

	var errs []error
	for _, dev := range f.open {
		if _, err := dev.Write(ctx, report); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == len(f.open) {
		return fmt.Errorf("luxafor: writing color: %w", errors.Join(errs...))
	}
	return nil
}

// openLocked opens every matching device if they are not open already.
func (f *Flag) openLocked() error {
	if f.opened && len(f.open) > 0 {
		return nil
	}
	f.closeLocked() //nolint:errcheck

	var errs []error
	for info, err := range hid.Enumerate(
		hid.WithVendorID(f.vendorID),
		hid.WithProductID(f.productID),
	) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		dev, err := hid.OpenPath(info.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("opening %s: %w", info.Path, err))
			continue
		}
		f.open = append(f.open, dev)
	}

	if len(f.open) == 0 {
		if len(errs) == 0 {
			// Nothing on the bus matched: either no flag is attached, or the
			// operating system is not presenting it as a HID device.
			return ErrNoDevice
		}
		// A flag is attached but could not be opened, which on Linux is
		// almost always the permissions on its /dev/hidraw node.
		return fmt.Errorf("luxafor: found a flag but could not open it: %w", errors.Join(errs...))
	}
	f.opened = true
	return nil
}

// Close releases every open device.
func (f *Flag) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeLocked()
}

func (f *Flag) closeLocked() error {
	var errs []error
	for _, dev := range f.open {
		if err := dev.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	f.open = nil
	f.opened = false
	return errors.Join(errs...)
}
