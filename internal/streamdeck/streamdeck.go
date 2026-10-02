// Package streamdeck drives an Elgato Stream Deck Mini (six keys) as the
// busy light's buttons, as the sd program did, but setting the light
// directly rather than running a program: Busy, Free and Green set it red,
// off until the meetings under way end, and green; Remind only changes its
// picture, as a reminder to oneself.
//
// The protocol is the Mini's, as python-elgato-streamdeck speaks it: a
// feature report resets the deck and another sets its brightness; each
// key's image is an 80x80 BMP, sent in 1024-byte output reports; and an
// input report gives the six keys' states.
package streamdeck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/telesma-app/hid"
)

// The Mini's USB identifiers: the original Mini and the Mini MK.2.
const (
	VendorID      uint16 = 0x0fd9
	ProductMini   uint16 = 0x0063
	ProductMiniV2 uint16 = 0x0090
)

// The Mini's layout and image reports.
const (
	Keys            = 6
	KeySize         = 80 // pixels square
	imageReportLen  = 1024
	imageHeaderLen  = 16
	imagePayloadLen = imageReportLen - imageHeaderLen
	featureLen      = 17
)

// retryEvery is how long to wait for a deck to be plugged in, or after one
// has gone, before looking again.
const retryEvery = 5 * time.Second

// writeTimeout bounds one report's write.
const writeTimeout = 2 * time.Second

// Device is an open HID device; *hid.Device is one.
type Device interface {
	Read(ctx context.Context, b []byte) (int, error)
	Write(ctx context.Context, b []byte) (int, error)
	SendFeatureReport(report []byte) error
	Close() error
}

// ErrNoDeck reports that no Stream Deck Mini is attached.
var ErrNoDeck = errors.New("no Stream Deck Mini attached")

// openMini opens the first Stream Deck Mini attached.
func openMini() (Device, error) {
	var errs []error
	for _, product := range []uint16{ProductMini, ProductMiniV2} {
		for info, err := range hid.Enumerate(hid.WithVendorID(VendorID), hid.WithProductID(product)) {
			if err != nil {
				errs = append(errs, err)
				continue
			}
			dev, err := hid.OpenPath(info.Path)
			if err != nil {
				errs = append(errs, fmt.Errorf("opening %s: %w", info.Path, err))
				continue
			}
			return dev, nil
		}
	}
	if len(errs) > 0 {
		// Found but not opened: on Linux, almost always the permissions of
		// its /dev/hidraw node.
		return nil, fmt.Errorf("found a Stream Deck Mini but could not open it: %w", errors.Join(errs...))
	}
	return nil, ErrNoDeck
}

// Button is a key's look and what pressing it does. Images, when there is
// more than one, are stepped through at each press, as a toggle; Press,
// if set, runs at each press.
type Button struct {
	Label  string
	Images [][]byte // PNGs; none for a blank key
	Press  func()
}

// Deck drives a Stream Deck Mini with up to Keys buttons, in order from the
// top left; a zero Button is a blank key.
type Deck struct {
	Buttons []Button

	// open finds the deck, and retry is how long to wait before looking
	// again; tests replace them.
	open  func() (Device, error)
	retry time.Duration
	logf  func(format string, args ...any)

	shown []int // the image each key shows, by key
}

// New returns a deck with buttons.
func New(buttons []Button) *Deck {
	return &Deck{Buttons: buttons, open: openMini, retry: retryEvery, logf: log.Printf}
}

// Run drives the deck until ctx is canceled: it waits for one to be
// attached, draws the keys, and acts on each key pressed, starting over if
// it is unplugged. The deck is blanked when ctx is canceled.
func (d *Deck) Run(ctx context.Context) {
	missing := false
	for {
		dev, err := d.open()
		switch {
		case err == nil:
			missing = false
			d.logf("stream deck: attached")
			if err := d.serve(ctx, dev); ctx.Err() == nil {
				d.logf("stream deck: %v", err)
			}
			if ctx.Err() != nil {
				d.blank(dev)
			}
			dev.Close() //nolint:errcheck
		case errors.Is(err, ErrNoDeck):
			if !missing {
				missing = true
				d.logf("stream deck: none attached; looking again every %v", d.retry)
			}
		default:
			d.logf("stream deck: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.retry):
		}
	}
}

// serve sets the deck up and acts on its keys until ctx is canceled or the
// deck fails, returning why it stopped.
func (d *Deck) serve(ctx context.Context, dev Device) error {
	if err := dev.SendFeatureReport(feature(0x0b, 0x63)); err != nil {
		return fmt.Errorf("resetting: %w", err)
	}
	if err := dev.SendFeatureReport(feature(0x05, 0x55, 0xaa, 0xd1, 0x01, 100)); err != nil {
		return fmt.Errorf("setting the brightness: %w", err)
	}
	d.shown = make([]int, Keys)
	for key := range Keys {
		if err := d.draw(ctx, dev, key); err != nil {
			return err
		}
	}

	down := make([]bool, Keys)
	buf := make([]byte, 64)
	for {
		n, err := dev.Read(ctx, buf)
		if err != nil {
			return fmt.Errorf("reading the keys: %w", err)
		}
		states := buf[:n]
		// The input report's ID, 1, comes first, if the system keeps it.
		if len(states) > Keys && states[0] == 0x01 {
			states = states[1:]
		}
		for key := range min(len(states), Keys) {
			pressed := states[key] != 0
			if pressed && !down[key] {
				if err := d.press(ctx, dev, key); err != nil {
					return err
				}
			}
			down[key] = pressed
		}
	}
}

// press acts on key being pressed: its next image, if it has several, then
// what it does.
func (d *Deck) press(ctx context.Context, dev Device, key int) error {
	if key >= len(d.Buttons) {
		return nil
	}
	b := d.Buttons[key]
	if len(b.Images) > 1 {
		d.shown[key] = (d.shown[key] + 1) % len(b.Images)
		if err := d.draw(ctx, dev, key); err != nil {
			return err
		}
	}
	if b.Press != nil {
		b.Press()
	}
	return nil
}

// draw sends key its image as it should show now.
func (d *Deck) draw(ctx context.Context, dev Device, key int) error {
	var b Button
	if key < len(d.Buttons) {
		b = d.Buttons[key]
	}
	var png []byte
	if len(b.Images) > 0 {
		png = b.Images[d.shown[key]%len(b.Images)]
	}
	img, err := Render(png, b.Label)
	if err != nil {
		return fmt.Errorf("drawing key %d: %w", key, err)
	}
	return sendImage(ctx, dev, key, img)
}

// blank clears every key, as a deck left unused should be.
func (d *Deck) blank(dev Device) {
	dev.SendFeatureReport(feature(0x0b, 0x63)) //nolint:errcheck
}

// sendImage sends key the image bmp, in pages: each an output report with
// report ID 2, the page number, whether it is the last, and the key, from
// 1, then up to imagePayloadLen bytes of the image, padded.
func sendImage(ctx context.Context, dev Device, key int, bmp []byte) error {
	for page, sent := 0, 0; sent < len(bmp) || page == 0; page++ {
		n := min(len(bmp)-sent, imagePayloadLen)
		last := byte(0)
		if sent+n == len(bmp) {
			last = 1
		}
		report := make([]byte, imageReportLen)
		copy(report, []byte{0x02, 0x01, byte(page), 0x00, last, byte(key + 1)})
		copy(report[imageHeaderLen:], bmp[sent:sent+n])
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		_, err := dev.Write(wctx, report)
		cancel()
		if err != nil {
			return fmt.Errorf("sending key %d's image: %w", key, err)
		}
		sent += n
	}
	return nil
}

// feature is a feature report: data, padded to featureLen bytes.
func feature(data ...byte) []byte {
	report := make([]byte, featureLen)
	copy(report, data)
	return report
}
