// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned makes a certificate for names, good from notBefore to
// notAfter, with key, returning it and the key as PEM; the key in PKCS #8
// form.
func selfSigned(t *testing.T, key crypto.Signer, names []string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})
}

func ecKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ecdsaHello is a client able to use an ECDSA certificate.
func ecdsaHello(name string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName:       name,
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
}

// newManager makes a Manager on dir pointed at no certificate authority,
// so that it can only serve what is in dir.
func newManager(t *testing.T, dir string, domains ...string) *Manager {
	t.Helper()
	m, err := New(Config{Dir: dir, DirectoryURL: "http://127.0.0.1:1/directory"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetDomains(domains)
	return m
}

func TestImportServed(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certPEM, keyPEM := selfSigned(t, ecKey(t), []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	got, err := Import(dir, nil, certPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Domain != "adhd.example.com" || got[0].Kept {
		t.Fatalf("Import = %+v, want adhd.example.com imported", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "adhd.example.com")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("imported file mode %v, want none for group or others", fi.Mode().Perm())
	}

	m := newManager(t, dir, "adhd.example.com")
	block, _ := pem.Decode(certPEM)
	for _, name := range []string{"adhd.example.com", ""} {
		c, err := m.TN3270Config().GetCertificate(ecdsaHello(name))
		if err != nil {
			t.Fatalf("GetCertificate(%q): %v", name, err)
		}
		if string(c.Certificate[0]) != string(block.Bytes) {
			t.Errorf("GetCertificate(%q) is not the imported certificate", name)
		}
	}
	st := m.Check()
	if len(st) != 1 || st[0].Err != nil || !st[0].NotAfter.Equal(got[0].NotAfter) {
		t.Errorf("Check = %+v, want the imported certificate's expiry", st)
	}
	if st := m.Stored(); len(st) != 1 || st[0].Err != nil || !st[0].NotAfter.Equal(got[0].NotAfter) {
		t.Errorf("Stored = %+v, want the imported certificate's expiry", st)
	}

	// Another domain is refused, and has none stored.
	if _, err := m.TN3270Config().GetCertificate(ecdsaHello("other.example.com")); err == nil {
		t.Error("GetCertificate for a domain not set succeeded")
	}
	m.SetDomains([]string{"other.example.com"})
	if st := m.Stored(); len(st) != 1 || st[0].Err == nil {
		t.Errorf("Stored for a domain with none = %+v, want an error", st)
	}
	// With none, a client naming no server gets nothing.
	m.SetDomains(nil)
	if _, err := m.TN3270Config().GetCertificate(ecdsaHello("")); err == nil {
		t.Error("GetCertificate with no domains succeeded")
	}
}

func TestImportWildcard(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certPEM, keyPEM := selfSigned(t, ecKey(t), []string{"*.example.com"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	if _, err := Import(dir, nil, certPEM, keyPEM, now); err == nil {
		t.Error("Import of a wildcard certificate for no domain succeeded")
	}
	got, err := Import(dir, []string{"adhd.example.com"}, certPEM, keyPEM, now)
	if err != nil || len(got) != 1 || got[0].Domain != "adhd.example.com" {
		t.Errorf("Import of a wildcard certificate for a domain = %+v, %v", got, err)
	}
}

func TestImportRSA(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM := selfSigned(t, k, []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	if _, err := Import(dir, []string{"adhd.example.com"}, certPEM, keyPEM, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "adhd.example.com+rsa")); err != nil {
		t.Errorf("RSA certificate not kept under +rsa: %v", err)
	}
	// A client that cannot use ECDSA gets it.
	m := newManager(t, dir, "adhd.example.com")
	if _, err := m.TN3270Config().GetCertificate(&tls.ClientHelloInfo{ServerName: "adhd.example.com", CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}); err != nil {
		t.Errorf("GetCertificate: %v", err)
	}
}

func TestImportKeepsNewer(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	k := ecKey(t)
	newer, keyPEM := selfSigned(t, k, []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(80*24*time.Hour))
	older, _ := selfSigned(t, k, []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(20*24*time.Hour))
	if _, err := Import(dir, []string{"adhd.example.com"}, newer, keyPEM, now); err != nil {
		t.Fatal(err)
	}
	got, err := Import(dir, []string{"adhd.example.com"}, older, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Kept {
		t.Errorf("Import of an older certificate = %+v, want the newer kept", got)
	}
}

func TestImportRefuses(t *testing.T) {
	now := time.Now()
	k := ecKey(t)
	good, keyPEM := selfSigned(t, k, []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	expired, _ := selfSigned(t, k, []string{"adhd.example.com"}, now.Add(-48*time.Hour), now.Add(-time.Hour))
	_, otherKey := selfSigned(t, ecKey(t), []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(24*time.Hour))
	for _, tc := range []struct {
		name          string
		domains       []string
		certPEM, kPEM []byte
		want          string
	}{
		{"expired", []string{"adhd.example.com"}, expired, keyPEM, "expired"},
		{"wrong key", []string{"adhd.example.com"}, good, otherKey, "not the certificate's"},
		{"no certificate", []string{"adhd.example.com"}, keyPEM, keyPEM, "no certificate"},
		{"no key", []string{"adhd.example.com"}, good, good, "no private key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := Import(dir, tc.domains, tc.certPEM, tc.kPEM, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Import error %v, want one saying %q", err, tc.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("Import wrote %d files after refusing", len(entries))
			}
		})
	}
}

func TestParseDomains(t *testing.T) {
	got, err := ParseDomains(" ADHD.example.com., b.example.com ,\nc.example.com adhd.example.com\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "adhd.example.com,b.example.com,c.example.com" {
		t.Errorf("ParseDomains = %q", got)
	}
	for _, none := range []string{"", " , \n"} {
		if got, err := ParseDomains(none); err != nil || len(got) != 0 {
			t.Errorf("ParseDomains(%q) = %q, %v; want none", none, got, err)
		}
	}
	for _, bad := range []string{"localhost", "192.0.2.1", "*.example.com"} {
		if _, err := ParseDomains(bad); err == nil {
			t.Errorf("ParseDomains(%q) succeeded", bad)
		}
	}
}

func TestRedirect(t *testing.T) {
	for _, tc := range []struct {
		port         int
		host, method string
		code         int
		location     string
	}{
		{443, "adhd.example.com", http.MethodGet, http.StatusMovedPermanently, "https://adhd.example.com/privacy?x=1"},
		{443, "adhd.example.com:80", http.MethodHead, http.StatusMovedPermanently, "https://adhd.example.com/privacy?x=1"},
		{8443, "adhd.example.com", http.MethodGet, http.StatusMovedPermanently, "https://adhd.example.com:8443/privacy?x=1"},
		{443, "[2001:db8::1]:80", http.MethodGet, http.StatusMovedPermanently, "https://[2001:db8::1]/privacy?x=1"},
		{443, "adhd.example.com", http.MethodPost, http.StatusBadRequest, ""},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+"/privacy?x=1", nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		redirect(tc.port).ServeHTTP(w, r)
		if w.Code != tc.code || w.Header().Get("Location") != tc.location {
			t.Errorf("%s %s to port %d: %d %q, want %d %q", tc.method, tc.host, tc.port, w.Code, w.Header().Get("Location"), tc.code, tc.location)
		}
	}
}

func TestFailureBackoff(t *testing.T) {
	dir := t.TempDir()
	var logs []string
	m, err := New(Config{Dir: dir, DirectoryURL: "http://127.0.0.1:1/directory", Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.SetDomains([]string{"adhd.example.com"})
	cfg := m.HTTPSConfig()

	// The first connection asks the certificate authority, and fails.
	if _, err := cfg.GetCertificate(ecdsaHello("adhd.example.com")); err == nil || strings.Contains(err.Error(), "tried again at") {
		t.Fatalf("first: %v; want autocert's failure", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "getting a certificate for adhd.example.com failed") {
		t.Fatalf("logged %q", logs)
	}
	// Those after, without a certificate, do not, and are not logged.
	for range 3 {
		if _, err := cfg.GetCertificate(ecdsaHello("")); err == nil || !strings.Contains(err.Error(), "tried again at") {
			t.Fatalf("within RetryAfter: %v; want the failure remembered", err)
		}
	}
	if len(logs) != 1 {
		t.Errorf("logged %d times, want once", len(logs))
	}
	if st := m.Stored(); len(st) != 1 || !st[0].Failed {
		t.Errorf("Stored = %+v, want Failed", st)
	}
	// A challenge's handshake is still autocert's.
	hello := ecdsaHello("adhd.example.com")
	hello.SupportedProtos = []string{"acme-tls/1"}
	if _, err := cfg.GetCertificate(hello); err == nil || strings.Contains(err.Error(), "tried again at") {
		t.Errorf("token handshake: %v; want autocert's", err)
	}

	// After RetryAfter it is asked again, and a new failure logged.
	clock = clock.Add(RetryAfter + time.Second)
	if _, err := cfg.GetCertificate(ecdsaHello("adhd.example.com")); err == nil || strings.Contains(err.Error(), "tried again at") {
		t.Errorf("after RetryAfter: %v; want autocert asked", err)
	}
	if len(logs) != 2 {
		t.Errorf("logged %d times, want twice", len(logs))
	}

	// Retry forgets it.
	m.Retry()
	if st := m.Stored(); st[0].Failed {
		t.Errorf("after Retry: %+v", st)
	}

	// A certificate put in the directory while waiting is used, rather
	// than the failure. (autocert itself remembers its own failure for a
	// minute, so this is a Manager that has not asked.)
	m2 := newManager(t, dir, "adhd.example.com")
	m2.fail("adhd.example.com", errors.New("down"))
	now := time.Now()
	certPEM, keyPEM := selfSigned(t, ecKey(t), []string{"adhd.example.com"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	if _, err := Import(dir, nil, certPEM, keyPEM, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.HTTPSConfig().GetCertificate(ecdsaHello("adhd.example.com")); err != nil {
		t.Errorf("with one imported during the wait: %v", err)
	}
}
