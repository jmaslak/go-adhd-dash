// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/web3270"
)

// fakeWeb3270 is a Web3270Control kept in memory: running while enabled
// with a domain.
type fakeWeb3270 struct {
	enabled bool
	domain  string
	bin     string
}

func (f *fakeWeb3270) Status() web3270.Status {
	st := web3270.Status{Enabled: f.enabled, Bin: f.bin}
	switch {
	case f.enabled && f.domain != "" && f.bin != "":
		st.Running, st.Domain = true, f.domain
	case f.enabled && f.domain == "":
		st.Problem = "no TLS domain is set (admin menu option 9)"
	}
	return st
}
func (f *fakeWeb3270) SetEnabled(on bool) error { f.enabled = on; return nil }
func (f *fakeWeb3270) URL(host string) string {
	if !f.Status().Running {
		return ""
	}
	return "https://" + host + ":32700/web3270"
}

func TestWebTermScreen(t *testing.T) {
	ctl := &fakeWeb3270{bin: "/usr/local/bin/web3270"}
	var s webTermState
	var screen go3270.Screen
	var text string
	draw := func() {
		screen, _, _ = buildWebTerm(24, 80, now, ctl, &s)
		text = strings.Join(screenText(t, screen, 24, 80), "\n")
	}
	key := func(aid go3270.AID, typed string) bool {
		leave := s.handle(go3270.Response{AID: aid, Values: map[string]string{webTermField: typed}}, ctl, func(string, ...any) {})
		draw()
		return leave
	}

	draw()
	if !strings.Contains(text, "disabled") || !strings.Contains(text, "/usr/local/bin/web3270") {
		t.Errorf("disabled:\n%s", text)
	}
	key(go3270.AIDEnter, "y")
	if !ctl.enabled || !strings.Contains(text, "Enabled, but not running: no TLS domain") {
		t.Errorf("enabled with no domain:\n%s", text)
	}
	ctl.domain = "adhd.example.com, b.example.com"
	draw()
	if !strings.Contains(text, "running for adhd.example.com") || !strings.Contains(text, "https://adhd.example.com:32700/web3270") {
		t.Errorf("running:\n%s", text)
	}
	key(go3270.AIDEnter, "maybe")
	if !strings.Contains(text, "Type Y or N.") || !ctl.enabled {
		t.Errorf("bad answer:\n%s", text)
	}
	if key(go3270.AIDPF3, "N") {
		t.Error("PF3 with a change left at once")
	}
	if !key(go3270.AIDPF3, "N") || !ctl.enabled {
		t.Error("a second PF3 did not leave, or saved")
	}
	s = webTermState{}
	key(go3270.AIDEnter, " n ")
	if ctl.enabled || !strings.Contains(text, "Disabled the web terminal.") {
		t.Errorf("disabling:\n%s", text)
	}

	ctl.bin = ""
	draw()
	if !strings.Contains(text, "not installed") {
		t.Errorf("not installed:\n%s", text)
	}
}
