// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package certs

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// freePort is a TCP port on 127.0.0.1 free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	return ln.Addr().(*net.TCPAddr).Port
}

// newService makes a Service on a temporary directory, with an imported
// certificate for adhd.example.com, and its HTTPS and ACME ports free.
func newService(t *testing.T) (*Service, ServiceConfig) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	certPEM, keyPEM := selfSigned(t, ecKey(t), []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	if _, err := Import(filepath.Join(dir, "certs"), nil, certPEM, keyPEM, now); err != nil {
		t.Fatal(err)
	}
	c := ServiceConfig{
		Manager:   newManager(t, filepath.Join(dir, "certs")),
		File:      filepath.Join(dir, "domain.txt"),
		Host:      "127.0.0.1",
		HTTPSPort: freePort(t),
		ACMEPort:  freePort(t),
		Web:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("hello")) }), //nolint:errcheck
		Logf:      t.Logf,
	}
	s := NewService(c)
	t.Cleanup(s.Close)
	return s, c
}

// listening reports whether port on 127.0.0.1 takes connections.
func listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	c.Close() //nolint:errcheck
	return true
}

func TestServiceOnOff(t *testing.T) {
	s, c := newService(t)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if s.Domain() != "" || s.HTTPSListen() != "" || listening(c.HTTPSPort) || listening(c.ACMEPort) {
		t.Fatalf("with no file: domain %q, HTTPS %q; want TLS off", s.Domain(), s.HTTPSListen())
	}
	if _, ok := s.Wrap(&net.TCPConn{}).(*tls.Conn); ok {
		t.Error("Wrap with TLS off made a TLS connection")
	}

	got, err := s.SetDomain(" ADHD.example.com ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "adhd.example.com" || s.Domain() != got {
		t.Errorf("SetDomain = %q, Domain %q; want adhd.example.com", got, s.Domain())
	}
	if data, err := os.ReadFile(c.File); err != nil || string(data) != "adhd.example.com\n" {
		t.Errorf("file holds %q, %v", data, err)
	}
	if !listening(c.HTTPSPort) || !listening(c.ACMEPort) || !strings.HasPrefix(s.HTTPSListen(), "https://127.0.0.1:") {
		t.Errorf("with TLS on: HTTPS %q; want both ports open", s.HTTPSListen())
	}
	if _, ok := s.Wrap(&net.TCPConn{}).(*tls.Conn); !ok {
		t.Error("Wrap with TLS on made no TLS connection")
	}
	// The imported certificate is served over HTTPS.
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.HTTPSPort)), &tls.Config{ServerName: "adhd.example.com", InsecureSkipVerify: true}) //nolint:gosec // a self-signed certificate
	if err != nil {
		t.Fatalf("HTTPS handshake: %v", err)
	}
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "adhd.example.com" {
		t.Errorf("HTTPS certificate for %q", cn)
	}
	conn.Close() //nolint:errcheck
	if st := s.Certificates(); len(st) != 1 || st[0].Err != nil {
		t.Errorf("Certificates = %+v", st)
	}

	// A second Service on the same file starts with TLS on.
	c2 := c
	c2.Manager = newManager(t, t.TempDir())
	c2.HTTPSPort, c2.ACMEPort = freePort(t), freePort(t)
	s2 := NewService(c2)
	defer s2.Close()
	if err := s2.Start(); err != nil || s2.Domain() != "adhd.example.com" {
		t.Errorf("started from the file: %q, %v", s2.Domain(), err)
	}

	if got, err := s.SetDomain(""); err != nil || got != "" {
		t.Fatalf("SetDomain(\"\") = %q, %v", got, err)
	}
	if _, err := os.Stat(c.File); !os.IsNotExist(err) {
		t.Errorf("file left with TLS off: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if s.Domain() != "" || s.HTTPSListen() != "" || listening(c.HTTPSPort) || listening(c.ACMEPort) {
		t.Errorf("after turning off: domain %q, HTTPS %q; want TLS off", s.Domain(), s.HTTPSListen())
	}
}

func TestServiceBusyPort(t *testing.T) {
	s, c := newService(t)
	busy, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.ACMEPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close() //nolint:errcheck
	if _, err := s.SetDomain("adhd.example.com"); err == nil {
		t.Fatal("SetDomain with the ACME port taken succeeded")
	}
	if s.Domain() != "" || listening(c.HTTPSPort) {
		t.Errorf("after failing: domain %q; want TLS still off, HTTPS closed", s.Domain())
	}
	if _, err := os.Stat(c.File); !os.IsNotExist(err) {
		t.Errorf("file written after failing: %v", err)
	}
	if _, err := s.SetDomain("localhost"); err == nil {
		t.Error("SetDomain(localhost) succeeded")
	}
}
