package streamdeck

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"
)

// fakeDeck records what is sent to it, and gives the key states it is fed.
type fakeDeck struct {
	mu       sync.Mutex
	features [][]byte
	writes   [][]byte
	closed   bool

	states chan []byte // each a report; closed, Read fails
}

func newFakeDeck() *fakeDeck { return &fakeDeck{states: make(chan []byte, 16)} }

func (f *fakeDeck) Read(ctx context.Context, b []byte) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case s, ok := <-f.states:
		if !ok {
			return 0, errors.New("unplugged")
		}
		return copy(b, s), nil
	}
}

func (f *fakeDeck) Write(_ context.Context, b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, bytes.Clone(b))
	return len(b), nil
}

func (f *fakeDeck) SendFeatureReport(r []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.features = append(f.features, bytes.Clone(r))
	return nil
}

func (f *fakeDeck) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeDeck) snapshot() (features, writes [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.features...), append([][]byte(nil), f.writes...)
}

// images reassembles the images sent, by key from 1, in the order sent,
// checking each page's header.
func images(t *testing.T, writes [][]byte) map[int][][]byte {
	t.Helper()
	out := map[int][][]byte{}
	var cur []byte
	page := 0
	for _, w := range writes {
		if len(w) != imageReportLen || w[0] != 0x02 || w[1] != 0x01 || w[3] != 0 {
			t.Fatalf("not an image report: % x", w[:8])
		}
		if int(w[2]) != page {
			t.Fatalf("page %d, want %d", w[2], page)
		}
		key, last := int(w[5]), w[4] == 1
		cur = append(cur, w[imageHeaderLen:]...)
		page++
		if last {
			out[key] = append(out[key], cur)
			cur, page = nil, 0
		}
	}
	if cur != nil {
		t.Fatalf("an image with no last page")
	}
	return out
}

// pressed is the input report for the keys down, with its report ID.
func pressed(keys ...int) []byte {
	r := make([]byte, 1+Keys)
	r[0] = 0x01
	for _, k := range keys {
		r[1+k] = 1
	}
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDeck(t *testing.T) {
	var mu sync.Mutex
	var keys []rune
	set := func(k rune) error {
		mu.Lock()
		defer mu.Unlock()
		keys = append(keys, k)
		return nil
	}
	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(keys)
	}
	fake := newFakeDeck()
	tries := 0
	d := New(BusyButtons(set, t.Logf))
	var logged []string
	d.logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	d.retry = 10 * time.Millisecond
	d.open = func() (Device, error) {
		tries++
		if tries == 1 {
			return nil, ErrNoDeck
		}
		return fake, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// Not there at first; then set up: reset, full brightness, every key
	// drawn.
	waitFor(t, "the keys drawn", func() bool { _, w := fake.snapshot(); return len(w) >= Keys*20 })
	features, writes := fake.snapshot()
	if len(features) != 2 || !bytes.Equal(features[0], feature(0x0b, 0x63)) || !bytes.Equal(features[1], feature(0x05, 0x55, 0xaa, 0xd1, 0x01, 100)) {
		t.Errorf("feature reports % x", features)
	}
	drawn := images(t, writes)
	for key := 1; key <= Keys; key++ {
		if len(drawn[key]) != 1 {
			t.Fatalf("key %d drawn %d times", key, len(drawn[key]))
		}
	}
	want, err := Render(busyPNG, "Busy")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(drawn[1][0], want) {
		t.Errorf("key 1 is not the Busy image")
	}
	blank, _ := Render(nil, "")
	if !bytes.HasPrefix(drawn[4][0], blank) || !bytes.HasPrefix(drawn[5][0], blank) {
		t.Errorf("keys 4 and 5 are not blank")
	}

	// Pressing Busy sets the light, once while held; then Free and Green.
	// A report without its ID byte is read the same.
	fake.states <- pressed(0)
	fake.states <- pressed(0)
	fake.states <- pressed()
	fake.states <- pressed(1)[1:]
	fake.states <- pressed()
	fake.states <- pressed(2)
	waitFor(t, "the keys acted on", func() bool { return got() == "bog" })

	// Remind changes picture, and sets nothing.
	_, before := fake.snapshot()
	fake.states <- pressed(5)
	fake.states <- pressed()
	waitFor(t, "Remind redrawn", func() bool { _, w := fake.snapshot(); return len(w) > len(before) })
	_, after := fake.snapshot()
	reminder, _ := Render(reminderPNG, "Remind")
	if redrawn := images(t, after[len(before):]); len(redrawn[6]) != 1 || !bytes.HasPrefix(redrawn[6][0], reminder) {
		t.Errorf("Remind not redrawn with the reminder")
	}
	if got() != "bog" {
		t.Errorf("Remind set the light: %q", got())
	}

	// Stopped, the deck is blanked and closed.
	cancel()
	<-done
	features, _ = fake.snapshot()
	if !bytes.Equal(features[len(features)-1], feature(0x0b, 0x63)) || !fake.closed {
		t.Errorf("not blanked and closed: % x", features[len(features)-1])
	}
	if len(logged) == 0 || logged[0] != "stream deck: none attached; looking again every 10ms" {
		t.Errorf("logged %q", logged)
	}
}

func TestRender(t *testing.T) {
	// A picture red on the left and blue on the right.
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 100 {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 50 {
				c = color.RGBA{0, 0, 255, 255}
			}
			src.Set(x, y, c)
		}
	}
	var pic bytes.Buffer
	if err := png.Encode(&pic, src); err != nil {
		t.Fatal(err)
	}
	bmp, err := Render(pic.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bmp) != 54+KeySize*KeySize*3 || string(bmp[:2]) != "BM" || binary.LittleEndian.Uint16(bmp[28:]) != 24 {
		t.Fatalf("BMP of %d bytes, header % x", len(bmp), bmp[:30])
	}
	// at is the BMP's pixel (x, y), counted from the top, as blue, green,
	// red.
	at := func(x, y int) [3]byte {
		i := 54 + (KeySize-1-y)*KeySize*3 + x*3
		return [3]byte{bmp[i], bmp[i+1], bmp[i+2]}
	}
	// Transposed: the picture's left (red) is the BMP's top, its right
	// (blue) further down; the label's space (at the picture's bottom) is
	// the BMP's right, and black.
	if c := at(30, 15); c != [3]byte{0, 0, 255} {
		t.Errorf("BMP (30,15) is %v, want red", c)
	}
	if c := at(30, 65); c != [3]byte{255, 0, 0} {
		t.Errorf("BMP (30,65) is %v, want blue", c)
	}
	if c := at(75, 40); c != [3]byte{0, 0, 0} {
		t.Errorf("BMP (75,40) is %v, want black", c)
	}

	// The label is white, in the bottom rows of the key.
	key, err := Draw(nil, "Busy")
	if err != nil {
		t.Fatal(err)
	}
	white := 0
	for y := range KeySize {
		for x := range KeySize {
			if key.RGBAAt(x, y).R > 200 {
				if y < KeySize-labelMargin {
					t.Fatalf("label above its space, at (%d,%d)", x, y)
				}
				white++
			}
		}
	}
	if white < 20 {
		t.Errorf("label hardly drawn: %d white pixels", white)
	}
	if _, err := Render([]byte("not a png"), "x"); err == nil {
		t.Errorf("a bad picture rendered")
	}
}
