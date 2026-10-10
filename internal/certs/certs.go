// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package certs gets, keeps and renews the server's TLS certificates from
// an ACME certificate authority (Let's Encrypt), for its HTTPS web pages
// and its TN3270 port alike, and imports a certificate obtained elsewhere,
// such as by a web server that was in front, into its store.
package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/idna"
)

// LetsEncryptURL is Let's Encrypt's production ACME directory.
const LetsEncryptURL = acme.LetsEncryptURL

// DefaultDir is where the certificates, their keys and the ACME account's
// key are kept unless told otherwise: adhd-dash-certs in the home
// directory.
func DefaultDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-certs")
	}
	return "adhd-dash-certs"
}

// Config says where certificates are kept, and from where they are got.
type Config struct {
	// Dir is the directory they are kept in, made if missing.
	Dir string
	// Email is the contact the certificate authority is given; "" for
	// none.
	Email string
	// DirectoryURL is the ACME directory; "" for Let's Encrypt's.
	DirectoryURL string
	// Logf logs a failure to get a certificate; log.Printf if nil.
	Logf func(string, ...any)
}

// RetryAfter is how long, after failing to get a domain's certificate, the
// certificate authority is left alone before it is asked again, however
// many clients connect meanwhile. Each attempt can fail validation twice
// (tls-alpn-01, then http-01), and Let's Encrypt allows five failures for
// a name in an hour; at two attempts an hour, that is not reached.
const RetryAfter = 30 * time.Minute

// failure is a failed attempt to get a domain's certificate.
type failure struct {
	at  time.Time
	err error
}

// Manager gets certificates for its domains as they are first wanted,
// keeps them in its directory, and renews each a third of its lifetime
// before it expires. Its domains can be changed while it runs; a
// certificate of one removed is still renewed until the program ends,
// since autocert gives no way to stop that.
type Manager struct {
	m    *autocert.Manager
	kick chan struct{} // tells Watch the domains changed
	logf func(string, ...any)
	now  func() time.Time

	mu      sync.RWMutex
	domains []string
	failed  map[string]failure // by domain, the last failure to get one
}

// New makes a Manager for c, with no domains yet.
func New(c Config) (*Manager, error) {
	if c.Dir == "" {
		return nil, errors.New("certs: no directory")
	}
	mgr := &Manager{kick: make(chan struct{}, 1), logf: c.Logf, now: time.Now}
	if mgr.logf == nil {
		mgr.logf = log.Printf
	}
	mgr.m = &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(c.Dir),
		HostPolicy: func(_ context.Context, host string) error {
			if !slices.Contains(mgr.Domains(), host) {
				return fmt.Errorf("certs: %q is not this server's TLS domain", host)
			}
			return nil
		},
		Email: c.Email,
	}
	if c.DirectoryURL != "" {
		mgr.m.Client = &acme.Client{DirectoryURL: c.DirectoryURL}
	}
	return mgr, nil
}

// Domains are the names m keeps certificates for, none when TLS is off.
// The first is the one given to a client that names none (sends no SNI),
// as many TN3270 emulators do not.
func (m *Manager) Domains() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.domains)
}

// SetDomains makes domains the names m keeps certificates for, and has
// Watch look at them at once. Any failure to get one is forgotten, so
// that the certificate authority is asked again at once.
func (m *Manager) SetDomains(domains []string) {
	m.mu.Lock()
	m.domains = slices.Clone(domains)
	m.failed = nil
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// getCertificate is autocert's, but giving a client that names no server
// the first domain's certificate rather than failing.
func (m *Manager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName == "" {
		domains := m.Domains()
		if len(domains) == 0 {
			return nil, errors.New("certs: no TLS domain")
		}
		h := *hello
		h.ServerName = domains[0]
		hello = &h
	}
	return m.get(hello)
}

// Retry forgets any failure to get a certificate, so that the certificate
// authority is asked again at the next connection, and has Watch ask now.
func (m *Manager) Retry() {
	m.mu.Lock()
	m.failed = nil
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// get is autocert's GetCertificate, but not asking the certificate
// authority for a domain's certificate within RetryAfter of failing to get
// one, unless one has been put in the directory since. Without that, every
// connection would ask it again (autocert waits only a minute), and Let's
// Encrypt would soon refuse to try for a while.
func (m *Manager) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
	// A challenge's own handshake, and a name not kept, are autocert's
	// alone: neither asks the certificate authority for anything.
	if wantsTokenCert(hello) || !slices.Contains(m.Domains(), name) {
		return m.m.GetCertificate(hello)
	}
	if f, ok := m.failure(name); ok && !m.stored(name) {
		return nil, fmt.Errorf("certs: no certificate for %s: getting one failed at %s, and is tried again at %s: %v",
			name, f.at.Local().Format(time.TimeOnly), f.at.Add(RetryAfter).Local().Format(time.TimeOnly), f.err)
	}
	c, err := m.m.GetCertificate(hello)
	if err != nil && !m.stored(name) {
		m.fail(name, err)
	}
	return c, err
}

// wantsTokenCert reports whether hello is the certificate authority's, for
// a tls-alpn-01 challenge, as autocert decides.
func wantsTokenCert(hello *tls.ClientHelloInfo) bool {
	return len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto
}

// failure is the last failure to get domain's certificate, if within
// RetryAfter.
func (m *Manager) failure(domain string) (failure, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.failed[domain]
	return f, ok && m.now().Sub(f.at) < RetryAfter
}

// fail records a failure to get domain's certificate, logging it, unless
// one within RetryAfter is recorded already (autocert gives the same error
// to every connection for a minute after).
func (m *Manager) fail(domain string, err error) {
	if _, ok := m.failure(domain); ok {
		return
	}
	m.mu.Lock()
	if m.failed == nil {
		m.failed = map[string]failure{}
	}
	m.failed[domain] = failure{at: m.now(), err: err}
	m.mu.Unlock()
	hint := ""
	if strings.Contains(err.Error(), "no viable challenge type") {
		hint = fmt.Sprintf(" (the certificate authority could not reach this server as %s on port 443 or 80: check that its DNS points here, that nothing else, such as a web server that was in front, answers there, and that 443 or 80 reach this server)", domain)
	}
	m.logf("tls: getting a certificate for %s failed: %v%s; trying again in %s, or at once on the TLS screen (PF5)", domain, err, hint, RetryAfter)
}

// stored reports whether the directory holds a certificate for domain.
func (m *Manager) stored(domain string) bool {
	for _, name := range []string{domain, domain + "+rsa"} {
		if _, err := m.m.Cache.Get(context.Background(), name); err == nil {
			return true
		}
	}
	return false
}

// HTTPSConfig is the TLS configuration for the web pages' HTTPS server. It
// answers the certificate authority's tls-alpn-01 challenges, so the
// server must be reached at each domain on port 443 for them.
func (m *Manager) HTTPSConfig() *tls.Config {
	c := m.m.TLSConfig()
	c.GetCertificate = m.getCertificate
	c.MinVersion = tls.VersionTLS12
	return c
}

// TN3270Config is the TLS configuration for the TN3270 port: TLS from the
// first byte (as x3270's L: prefix makes), not STARTTLS.
func (m *Manager) TN3270Config() *tls.Config {
	return &tls.Config{
		GetCertificate: m.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// HTTPHandler answers the certificate authority's http-01 challenges, and
// sends every other request to the same path over HTTPS, at httpsPort
// (443 is left out of the address).
func (m *Manager) HTTPHandler(httpsPort int) http.Handler {
	return m.m.HTTPHandler(redirect(httpsPort))
}

// redirect sends GET and HEAD requests to the same path over HTTPS at
// port, and refuses the rest, whose bodies it would lose.
func redirect(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "use HTTPS", http.StatusBadRequest)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if port != 443 {
			host = net.JoinHostPort(host, fmt.Sprint(port))
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// Status is what is known of one domain's certificate.
type Status struct {
	Domain   string
	NotAfter time.Time // zero when there is none
	Err      error     // why there is none
	Failed   bool      // getting one failed, and is not tried again yet
}

// Check gets each domain's certificate, from memory, the directory, or the
// certificate authority, in that order of preference, which can take
// minutes. Once a certificate is in memory it is renewed in the
// background; one never looked at is not, until it is.
func (m *Manager) Check() []Status {
	var out []Status
	for _, d := range m.Domains() {
		s := Status{Domain: d}
		// Described as a client able to use ECDSA, so that the ECDSA
		// certificate that clients get is the one renewed; one that
		// says nothing is given an RSA one.
		c, err := m.get(&tls.ClientHelloInfo{
			ServerName:       d,
			SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves:  []tls.CurveID{tls.CurveP256},
			CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		})
		switch {
		case err != nil:
			s.Err = err
		case c.Leaf != nil:
			s.NotAfter = c.Leaf.NotAfter
		default:
			if leaf, err := x509.ParseCertificate(c.Certificate[0]); err != nil {
				s.Err = err
			} else {
				s.NotAfter = leaf.NotAfter
			}
		}
		out = append(out, s)
	}
	return out
}

// Stored is what the directory holds for each domain, without asking the
// certificate authority for anything: quick enough for a screen to show.
func (m *Manager) Stored() []Status {
	var out []Status
	for _, d := range m.Domains() {
		s := Status{Domain: d, Err: errors.New("none yet")}
		for _, name := range []string{d, d + "+rsa"} {
			data, err := m.m.Cache.Get(context.Background(), name)
			if err != nil {
				if !errors.Is(err, autocert.ErrCacheMiss) {
					s.Err = err
				}
				continue
			}
			if n, ok := notAfter(data); ok && n.After(s.NotAfter) {
				s.NotAfter, s.Err = n, nil
			}
		}
		if f, ok := m.failure(d); ok && s.NotAfter.IsZero() {
			s.Err = fmt.Errorf("failed at %s, tried again at %s: %v", f.at.Local().Format(time.TimeOnly), f.at.Add(RetryAfter).Local().Format(time.TimeOnly), f.err)
			s.Failed = true
		}
		out = append(out, s)
	}
	return out
}

// Watch checks the certificates now, then every interval, and whenever
// the domains change, until ctx is done, logging each one's expiry with
// logf. Renewal itself is autocert's, which says nothing when it fails; a
// certificate with under warnLeft left is logged as a warning, since it
// should have been renewed by then.
func (m *Manager) Watch(ctx context.Context, interval, warnLeft time.Duration, logf func(string, ...any)) {
	// Changes made before it started are seen by the first check.
	select {
	case <-m.kick:
	default:
	}
	for {
		for _, s := range m.Check() {
			switch left := time.Until(s.NotAfter); {
			case s.Err != nil:
				logf("tls: no certificate for %s: %v", s.Domain, s.Err)
			case left < warnLeft:
				logf("tls: WARNING: the certificate for %s expires %s, in %s, and has not been renewed", s.Domain, s.NotAfter.Local().Format(time.DateTime), left.Round(time.Hour))
			default:
				logf("tls: the certificate for %s expires %s", s.Domain, s.NotAfter.Local().Format(time.DateTime))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		case <-time.After(interval):
		}
	}
}

// ParseDomains splits a list of domain names, separated by commas or
// spaces, returning them lowercased and in ASCII (Punycode), as autocert
// looks them up, without repeats; "" is none.
func ParseDomains(s string) ([]string, error) {
	var out []string
	for _, d := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		d = strings.TrimSuffix(d, ".")
		a, err := idna.Lookup.ToASCII(d)
		if err != nil {
			return nil, fmt.Errorf("domain %q: %v", d, err)
		}
		if net.ParseIP(a) != nil || !strings.Contains(a, ".") {
			return nil, fmt.Errorf("domain %q: not a domain name a certificate can be got for", d)
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// Imported is what Import did for one domain.
type Imported struct {
	Domain   string
	NotAfter time.Time
	// Kept is set when the directory already held a certificate expiring
	// no sooner, which was kept.
	Kept bool
}

// Import puts the certificate chain in certPEM, with its key in keyPEM,
// into dir, for each name it is for and each of domains it is good for
// (by a wildcard), where a Manager on dir uses it until it is due for
// renewal. The chain is the certificate
// first, then any intermediates, as Let's Encrypt and Caddy write it; the
// key is in PKCS #1, PKCS #8 or SEC 1 (EC) form.
func Import(dir string, domains []string, certPEM, keyPEM []byte, now time.Time) ([]Imported, error) {
	var chain [][]byte
	for rest := certPEM; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			chain = append(chain, b.Bytes)
		}
	}
	if len(chain) == 0 {
		return nil, errors.New("no certificate in the certificate file")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, fmt.Errorf("the certificate: %v", err)
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("the certificate expired %s", leaf.NotAfter.Local().Format(time.DateTime))
	}
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("the certificate is not good until %s", leaf.NotBefore.Local().Format(time.DateTime))
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(leaf.PublicKey) {
		return nil, errors.New("the key is not the certificate's")
	}
	// Kept as autocert keeps them: the key, then the chain, under the
	// domain's name, with "+rsa" added for an RSA one.
	var suffix string
	var keyBlock *pem.Block
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		keyBlock = &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	case *rsa.PrivateKey:
		suffix = "+rsa"
		keyBlock = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	default:
		return nil, fmt.Errorf("the key is a %T; only ECDSA and RSA keys can be used", key)
	}
	data := pem.EncodeToMemory(keyBlock)
	for _, c := range chain {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c})...)
	}

	var names []string
	for _, n := range leaf.DNSNames {
		if a, err := ParseDomains(n); err == nil && len(a) == 1 && !slices.Contains(names, a[0]) {
			names = append(names, a[0])
		}
	}
	for _, d := range domains {
		if leaf.VerifyHostname(d) == nil && !slices.Contains(names, d) {
			names = append(names, d)
		}
	}

	cache := autocert.DirCache(dir)
	ctx := context.Background()
	var out []Imported
	for _, d := range names {
		im := Imported{Domain: d, NotAfter: leaf.NotAfter}
		if old, err := cache.Get(ctx, d+suffix); err == nil {
			if n, ok := notAfter(old); ok && !n.Before(leaf.NotAfter) {
				im.NotAfter, im.Kept = n, true
				out = append(out, im)
				continue
			}
		} else if !errors.Is(err, autocert.ErrCacheMiss) {
			return out, err
		}
		if err := cache.Put(ctx, d+suffix, data); err != nil {
			return out, err
		}
		out = append(out, im)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the certificate is for no name a certificate can be kept for (%s)", strings.Join(leaf.DNSNames, ", "))
	}
	return out, nil
}

// parseKey reads the private key in a PEM file.
func parseKey(keyPEM []byte) (crypto.Signer, error) {
	for rest := keyPEM; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return nil, errors.New("no private key in the key file")
		}
		if !strings.Contains(b.Type, "PRIVATE KEY") {
			continue
		}
		if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
			return k, nil
		}
		if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
			return k, nil
		}
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the key: %v", err)
		}
		switch k := k.(type) {
		case *ecdsa.PrivateKey:
			return k, nil
		case *rsa.PrivateKey:
			return k, nil
		case ed25519.PrivateKey:
			return k, nil
		}
		return nil, fmt.Errorf("the key is a %T", k)
	}
}

// notAfter is when the certificate in an entry of autocert's cache
// expires.
func notAfter(data []byte) (time.Time, bool) {
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return time.Time{}, false
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return time.Time{}, false
		}
		return c.NotAfter, true
	}
}
