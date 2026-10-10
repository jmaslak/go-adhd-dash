package certs

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultDomainFile is where the TLS domain is kept unless told otherwise:
// adhd-dash-tls-domain.txt in the home directory.
func DefaultDomainFile() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-tls-domain.txt")
	}
	return "adhd-dash-tls-domain.txt"
}

// ServiceConfig is what a Service serves, and where.
type ServiceConfig struct {
	Manager *Manager
	// File is the plain text file the domain names are kept in, separated
	// by commas, spaces or lines; with none, there is no TLS.
	File string
	// Host is the address the HTTPS and ACME ports are opened on.
	Host string
	// HTTPSPort is where Web is served over HTTPS (0: nowhere), and
	// ACMEPort where the certificate authority's http-01 challenges are
	// answered, and browsers sent to HTTPS (0: nowhere).
	HTTPSPort, ACMEPort int
	Web                 http.Handler
	// Logf logs what the service does; log.Printf if nil.
	Logf func(string, ...any)
	// OnChange, if not nil, is called after SetDomain changes the domain.
	OnChange func()
}

// Service turns TLS on and off as its domain is set: with one, TN3270
// connections are TLS (see Wrap), the web pages are served over HTTPS,
// and the certificate authority is answered; with none, none of that.
type Service struct {
	c ServiceConfig

	mu      sync.Mutex
	servers []*http.Server // the HTTPS and ACME ports' servers while on
	https   string         // where HTTPS is served while on, "" when not
	closed  bool
}

// NewService makes a Service for c, off until Start.
func NewService(c ServiceConfig) *Service {
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return &Service{c: c}
}

// Start turns TLS on for the domain in the file, if it names one.
func (s *Service) Start() error {
	data, err := os.ReadFile(s.c.File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	domains, err := ParseDomains(string(data))
	if err != nil {
		return fmt.Errorf("%s: %v", s.c.File, err)
	}
	return s.apply(domains)
}

// Domain is the domain names TLS is on for, comma-separated; "" when off.
func (s *Service) Domain() string {
	return strings.Join(s.c.Manager.Domains(), ", ")
}

// File is where the domain is kept.
func (s *Service) File() string { return s.c.File }

// HTTPSListen is where the web pages are served over HTTPS, as
// "https://host:port"; "" when they are not.
func (s *Service) HTTPSListen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.https
}

// Certificates are what is kept for each domain, without asking the
// certificate authority for anything.
func (s *Service) Certificates() []Status { return s.c.Manager.Stored() }

// Retry has the certificate authority asked at once for any certificate
// that could not be got.
func (s *Service) Retry() {
	s.c.Logf("tls: asking for certificates again")
	s.c.Manager.Retry()
}

// SetDomain turns TLS on for the domain names in typed, or with none, off,
// and keeps them in the file (removing it for none), returning them as
// kept. Nothing is changed when the ports cannot be opened, or the file
// written.
func (s *Service) SetDomain(typed string) (string, error) {
	domains, err := ParseDomains(typed)
	if err != nil {
		return "", err
	}
	old := s.c.Manager.Domains()
	if err := s.apply(domains); err != nil {
		return "", err
	}
	if err := writeDomainFile(s.c.File, domains); err != nil {
		if rerr := s.apply(old); rerr != nil {
			s.c.Logf("tls: putting back %v: %v", old, rerr)
		}
		return "", err
	}
	if len(domains) == 0 {
		s.c.Logf("tls: off")
	} else {
		s.c.Logf("tls: on for %s", strings.Join(domains, ", "))
	}
	if s.c.OnChange != nil {
		s.c.OnChange()
	}
	return strings.Join(domains, ", "), nil
}

// Wrap is conn as a TN3270 connection is to be spoken on: in TLS while TLS
// is on, its handshake done at the first read or write.
func (s *Service) Wrap(conn net.Conn) net.Conn {
	if len(s.c.Manager.Domains()) == 0 {
		return conn
	}
	return tls.Server(conn, s.c.Manager.TN3270Config())
}

// Close stops the HTTPS and ACME ports' servers, for good.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.stop()
}

// apply opens or closes the HTTPS and ACME ports as domains turns TLS on or
// off, and gives the Manager domains.
func (s *Service) apply(domains []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("the server is shutting down")
	}
	switch {
	case len(domains) == 0:
		s.stop()
	case s.servers == nil:
		if err := s.start(); err != nil {
			return err
		}
	}
	s.c.Manager.SetDomains(domains)
	return nil
}

// start opens the HTTPS and ACME ports, both or neither, and serves them.
func (s *Service) start() error {
	type port struct {
		port int
		h    http.Handler
		tls  *tls.Config
	}
	redirectPort := s.c.HTTPSPort
	if redirectPort == 0 {
		redirectPort = 443
	}
	var ports []port
	if s.c.HTTPSPort != 0 {
		ports = append(ports, port{s.c.HTTPSPort, s.c.Web, s.c.Manager.HTTPSConfig()})
	}
	if s.c.ACMEPort != 0 {
		ports = append(ports, port{s.c.ACMEPort, s.c.Manager.HTTPHandler(redirectPort), nil})
	}
	var lns []net.Listener
	for _, p := range ports {
		addr := net.JoinHostPort(s.c.Host, fmt.Sprint(p.port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range lns {
				l.Close() //nolint:errcheck
			}
			return fmt.Errorf("listen on %s: %v", addr, err)
		}
		lns = append(lns, ln)
	}
	s.servers = []*http.Server{}
	for i, p := range ports {
		ln, scheme := lns[i], "http"
		if p.tls != nil {
			ln, scheme = tls.NewListener(ln, p.tls), "https"
			s.https = "https://" + lns[i].Addr().String()
		}
		srv := &http.Server{
			Handler:           p.h,
			TLSConfig:         p.tls,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    16 << 10,
		}
		s.servers = append(s.servers, srv)
		where := scheme + "://" + lns[i].Addr().String()
		s.c.Logf("serving %s", where)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.c.Logf("%s: %v", where, err)
			}
		}()
	}
	return nil
}

// stop closes the HTTPS and ACME ports, if open.
func (s *Service) stop() {
	for _, srv := range s.servers {
		srv.Close() //nolint:errcheck
	}
	s.servers, s.https = nil, ""
}

// writeDomainFile keeps domains in path, one per line, or with none,
// removes it.
func writeDomainFile(path string, domains []string) error {
	if len(domains) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone once renamed
	if _, err := tmp.WriteString(strings.Join(domains, "\n") + "\n"); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
