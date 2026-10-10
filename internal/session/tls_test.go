package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/certs"
)

// fakeTLS is a TLSControl kept in memory, failing to set fail.
type fakeTLS struct {
	domain  string
	fail    string
	sets    int
	failed  bool
	retries int
}

func (f *fakeTLS) Retry() { f.retries++; f.failed = false }

func (f *fakeTLS) Domain() string { return f.domain }
func (f *fakeTLS) File() string   { return "/var/lib/adhd-dash/adhd-dash-tls-domain.txt" }
func (f *fakeTLS) HTTPSListen() string {
	if f.domain == "" {
		return ""
	}
	return "https://0.0.0.0:443"
}
func (f *fakeTLS) Certificates() []certs.Status {
	if f.domain == "" {
		return nil
	}
	if f.failed {
		return []certs.Status{{Domain: f.domain, Failed: true, Err: errors.New("failed at 09:00, tried again at 09:30: no viable challenge type found")}}
	}
	return []certs.Status{{Domain: f.domain, NotAfter: now.Add(60 * 24 * time.Hour)}}
}
func (f *fakeTLS) SetDomain(typed string) (string, error) {
	d, err := certs.ParseDomains(typed)
	if err != nil {
		return "", err
	}
	if typed == f.fail {
		return "", errors.New("listen on 0.0.0.0:443: permission denied")
	}
	f.sets++
	f.domain = strings.Join(d, ", ")
	return f.domain, nil
}

func TestTLSScreen(t *testing.T) {
	ctl := &fakeTLS{fail: "fail.example.com"}
	var s tlsState
	var screen go3270.Screen
	var text string
	draw := func() {
		screen, _, _ = buildTLS(24, 80, now, ctl, &s)
		text = strings.Join(screenText(t, screen, 24, 80), "\n")
	}
	key := func(aid go3270.AID, typed *string) bool {
		values := map[string]string{}
		for _, f := range screen {
			if f.Write {
				values[f.Name] = f.Content
			}
		}
		if typed != nil {
			values[tlsDomainField] = *typed
		}
		leave := s.handle(go3270.Response{AID: aid, Values: values}, ctl, func(string, ...any) {})
		draw()
		return leave
	}
	ptr := func(s string) *string { return &s }

	draw()
	if !strings.Contains(text, "off: terminals and web pages unencrypted") || !strings.Contains(text, ctl.File()) {
		t.Errorf("off:\n%s", text)
	}

	// Turning it on asks first; PF3 there goes back, setting nothing.
	key(go3270.AIDEnter, ptr(" ADHD.example.com "))
	if !strings.Contains(text, "Turn TLS on for adhd.example.com?") || ctl.sets != 0 {
		t.Fatalf("turning on did not ask:\n%s", text)
	}
	key(go3270.AIDPF3, nil)
	if ctl.sets != 0 || !strings.Contains(text, "ADHD.example.com") {
		t.Errorf("PF3 on the confirmation set it, or lost what was typed:\n%s", text)
	}
	key(go3270.AIDEnter, nil)
	key(go3270.AIDPF4, nil)
	if ctl.domain != "adhd.example.com" || !strings.Contains(text, "TLS is on for adhd.example.com.") {
		t.Fatalf("PF4 did not turn it on: %q\n%s", ctl.domain, text)
	}
	if !strings.Contains(text, "Certificate for adhd.example.com: expires 2026-11-26 10:00:00") || !strings.Contains(text, "https://0.0.0.0:443") {
		t.Errorf("on:\n%s", text)
	}

	// PF5 does nothing with no failure, and with one, asks again.
	key(go3270.AIDPF5, nil)
	if ctl.retries != 0 || !strings.Contains(text, "No certificate is waiting") {
		t.Errorf("PF5 with no failure:\n%s", text)
	}
	ctl.failed = true
	draw()
	if !strings.Contains(text, "none: failed at 09:00") || !strings.Contains(text, "PF5=Retry") {
		t.Errorf("failed:\n%s", text)
	}
	key(go3270.AIDPF5, nil)
	if ctl.retries != 1 || !strings.Contains(text, "Asking for the certificate again") || strings.Contains(text, "PF5=Retry") {
		t.Errorf("PF5 with a failure:\n%s", text)
	}

	// Changing the name while on asks nothing.
	key(go3270.AIDEnter, ptr("b.example.com"))
	if ctl.domain != "b.example.com" || ctl.sets != 2 {
		t.Errorf("changing the name: %q after %d sets", ctl.domain, ctl.sets)
	}
	key(go3270.AIDEnter, nil)
	if !strings.Contains(text, "Nothing changed.") || ctl.sets != 2 {
		t.Errorf("unchanged:\n%s", text)
	}

	// A bad name, and a failure to set one, leave it as it was, keeping
	// what was typed.
	key(go3270.AIDEnter, ptr("localhost"))
	if ctl.domain != "b.example.com" || !strings.Contains(text, "That will not do:") || !strings.Contains(text, "localhost") {
		t.Errorf("bad name:\n%s", text)
	}
	key(go3270.AIDEnter, ptr("fail.example.com"))
	if ctl.domain != "b.example.com" || !strings.Contains(text, "Could not set it: listen on 0.0.0.0:443") {
		t.Errorf("failure:\n%s", text)
	}

	// PF3 with something unsaved warns once.
	if key(go3270.AIDPF3, ptr("c.example.com")) {
		t.Error("PF3 with something typed left at once")
	}
	if !key(go3270.AIDPF3, nil) {
		t.Error("a second PF3 did not leave")
	}

	// Blanking it turns it off, asking first.
	s = tlsState{}
	draw()
	key(go3270.AIDEnter, ptr(""))
	if !strings.Contains(text, "Turn TLS off?") {
		t.Fatalf("turning off did not ask:\n%s", text)
	}
	key(go3270.AIDPF4, nil)
	if ctl.domain != "" || !strings.Contains(text, "TLS is off.") {
		t.Errorf("PF4 did not turn it off: %q\n%s", ctl.domain, text)
	}
}

func TestWebListen(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, ""},
		{Config{HTTPListen: "http://localhost:3280"}, "http://localhost:3280"},
		{Config{HTTPListen: "http://localhost:3280", TLS: &fakeTLS{}}, "http://localhost:3280"},
		{Config{TLS: &fakeTLS{domain: "adhd.example.com"}}, "https://0.0.0.0:443"},
		{Config{HTTPListen: "http://localhost:3280", TLS: &fakeTLS{domain: "adhd.example.com"}}, "http://localhost:3280, https://0.0.0.0:443"},
	} {
		if got := c.cfg.webListen(); got != c.want {
			t.Errorf("webListen = %q, want %q", got, c.want)
		}
	}
}
