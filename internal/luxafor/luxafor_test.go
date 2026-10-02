// Copyright © 2026 Joelle Maslak
// All Rights Reserved - See License

package luxafor_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jmaslak/go-adhd-dash/internal/luxafor"
)

func TestReport(t *testing.T) {
	tests := []struct {
		name         string
		led, r, g, b byte
		want         []byte
	}{
		{
			name: "dim red on every LED",
			led:  luxafor.LEDAll, r: 20, g: 0, b: 0,
			want: []byte{0x00, 0x01, 0xff, 20, 0, 0, 0x00, 0x00},
		},
		{
			name: "off",
			led:  luxafor.LEDAll,
			want: []byte{0x00, 0x01, 0xff, 0, 0, 0, 0x00, 0x00},
		},
		{
			name: "a single LED at full brightness",
			led:  0x02, r: 255, g: 255, b: 255,
			want: []byte{0x00, 0x01, 0x02, 255, 255, 255, 0x00, 0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, luxafor.Report(tt.led, tt.r, tt.g, tt.b)); diff != "" {
				t.Errorf("Report() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestIndicateWithoutHardware documents what happens with no flag attached,
// which is the state of any machine running the test suite.
func TestIndicateWithoutHardware(t *testing.T) {
	flag := luxafor.New()
	t.Cleanup(func() { flag.Close() }) //nolint:errcheck

	err := flag.Indicate(20, 0, 0)
	if err == nil {
		t.Skip("a Luxafor flag is attached to this machine")
	}
	if !errors.Is(err, luxafor.ErrNoDevice) {
		t.Errorf("Indicate() error = %v, want it to wrap ErrNoDevice", err)
	}
}

func TestCloseWithoutOpening(t *testing.T) {
	if err := luxafor.New().Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}
