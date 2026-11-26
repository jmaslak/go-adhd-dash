// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

// Package web3270 runs moshix's web3270, a TN3270 emulator in the browser,
// beside the server, in kiosk mode, connecting only to the server itself,
// and serves it to browsers over HTTPS through a reverse proxy, with the
// server's own certificate.
//
// web3270 speaks TN3270 only unencrypted, so it is given a TN3270 port of
// its own on the loopback address, which nothing else on the network can
// reach. It listens on the loopback address too, over HTTP; browsers reach
// it only through the proxy.
package web3270

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/tcpconn"
)

// DefaultFile is where whether web3270 is enabled is kept unless told
// otherwise: adhd-dash-web3270.txt in the home directory.
func DefaultFile() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-web3270.txt")
	}
	return "adhd-dash-web3270.txt"
}

// enabledText is what the file holds when web3270 is enabled.
const enabledText = "enabled\n"

// Config is how web3270 is run, and served.
type Config struct {
	// Bin is web3270's program: a path, or a name looked up in $PATH.
	// It is looked for each time it is to be started, so that one
	// installed later is used.
	Bin string
	// File is the plain text file that says web3270 is enabled; with
	// none, it is not.
	File string
	// Host is the address ProxyPort is opened on.
	Host string
	// ProxyPort is where browsers reach web3270, over HTTPS.
	ProxyPort int
	// ListenPort is where web3270 itself listens, over HTTP, on the
	// loopback address.
	ListenPort int
	// TN3270Port is the unencrypted TN3270 port, on the loopback address,
	// that web3270 is locked to; each connection to it is given to Serve.
	TN3270Port int
	// Serve runs a TN3270 session on a connection.
	Serve func(net.Conn)
	// Domain is the TLS domain: web3270 runs only while there is one, and
	// is restarted when it changes.
	Domain func() string
	// TLS is the TLS configuration for ProxyPort.
	TLS *tls.Config
	// Logf logs what is done; log.Printf if nil.
	Logf func(string, ...any)
}

// Supervisor runs web3270 while it is enabled, installed, and there is a
// TLS domain, restarting it when it exits, and when the domain changes.
type Supervisor struct {
	c Config

	mu      sync.Mutex
	enabled bool
	cur     *instance
	problem string        // why it is not running, when it should be
	backoff time.Duration // before starting again after it exited
	retry   *time.Timer
	closed  bool
}

// instance is web3270 running, with the ports serving it.
type instance struct {
	domain  string
	bin     string
	started time.Time
	cmd     *exec.Cmd
	exited  chan struct{}
	proxy   *http.Server
	tn3270  net.Listener
}

// Status is what an admin is shown.
type Status struct {
	Enabled bool
	// Bin is the program found, "" when it is not installed.
	Bin string
	// Running is set while web3270 runs, Domain being what it was
	// started for.
	Running bool
	Domain  string
	// Problem is why it is not running, if it should be.
	Problem string
}

// New makes a Supervisor for c, stopped until Start.
func New(c Config) *Supervisor {
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return &Supervisor{c: c}
}

// Start reads whether web3270 is enabled, and starts it if it should run.
func (s *Supervisor) Start() error {
	data, err := os.ReadFile(s.c.File)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		switch t := strings.TrimSpace(string(data)); strings.ToLower(t) {
		case "enabled", "on", "yes", "true", "1":
			s.enabled = true
		case "", "disabled", "off", "no", "false", "0":
		default:
			return fmt.Errorf("%s: %q is neither enabled nor disabled", s.c.File, t)
		}
	}
	s.Reconcile()
	return nil
}

// Enabled reports whether web3270 is enabled.
func (s *Supervisor) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled enables or disables web3270, keeping that in the file, and
// starts or stops it to match.
func (s *Supervisor) SetEnabled(on bool) error {
	s.mu.Lock()
	if on {
		if err := writeFile(s.c.File, enabledText); err != nil {
			s.mu.Unlock()
			return err
		}
	} else if err := os.Remove(s.c.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.mu.Unlock()
		return err
	}
	s.enabled = on
	s.backoff, s.problem = 0, ""
	s.mu.Unlock()
	if on {
		s.c.Logf("web3270: enabled")
	} else {
		s.c.Logf("web3270: disabled")
	}
	s.Reconcile()
	return nil
}

// Status is what web3270 is doing.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Enabled: s.enabled, Bin: s.bin(), Problem: s.problem}
	if s.cur != nil {
		st.Running, st.Domain, st.Problem = true, s.cur.domain, ""
	}
	return st
}

// URL is where browsers reach web3270 at host (a request's Host, its port
// dropped), "" while it is not running.
func (s *Supervisor) URL(host string) string {
	s.mu.Lock()
	running := s.cur != nil
	s.mu.Unlock()
	if !running {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "https://" + net.JoinHostPort(host, fmt.Sprint(s.c.ProxyPort)) + "/web3270"
}

// Close stops web3270 for good.
func (s *Supervisor) Close() {
	s.mu.Lock()
	s.closed = true
	if s.retry != nil {
		s.retry.Stop()
	}
	cur := s.cur
	s.cur = nil
	s.mu.Unlock()
	s.stop(cur)
}

// bin is the program found, "" for none.
func (s *Supervisor) bin() string {
	if s.c.Bin == "" {
		return ""
	}
	p, err := exec.LookPath(s.c.Bin)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// Reconcile starts, stops or restarts web3270 to match whether it should
// run, and for what domain, at once, rather than at any retry pending.
func (s *Supervisor) Reconcile() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.retry != nil {
		s.retry.Stop()
		s.retry = nil
	}
	domain, bin := s.c.Domain(), s.bin()
	want := s.enabled && domain != "" && bin != ""
	var old *instance
	if s.cur != nil && (!want || s.cur.domain != domain || s.cur.bin != bin) {
		old, s.cur = s.cur, nil
	}
	if !want {
		s.problem = ""
		if s.enabled && domain == "" {
			s.problem = "no TLS domain is set (admin menu option 9)"
		} else if s.enabled && bin == "" {
			s.problem = s.c.Bin + " is not installed"
		}
	}
	s.mu.Unlock()
	if old != nil {
		s.c.Logf("web3270: stopping (was for %s)", old.domain)
		s.stop(old)
	}
	if !want {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil || s.closed {
		return
	}
	inst, err := s.start(domain, bin)
	if err != nil {
		s.problem = err.Error()
		s.c.Logf("web3270: %v", err)
		s.retryLater()
		return
	}
	s.cur, s.problem = inst, ""
}

// retryLater calls Reconcile after the backoff, doubling it, up to five
// minutes. Called with s.mu held.
func (s *Supervisor) retryLater() {
	s.backoff = min(max(2*s.backoff, 5*time.Second), 5*time.Minute)
	var t *time.Timer
	t = time.AfterFunc(s.backoff, func() {
		s.mu.Lock()
		current := s.retry == t
		s.mu.Unlock()
		if current {
			s.Reconcile()
		}
	})
	s.retry = t
}

// start opens the loopback TN3270 port and the proxy's port, and starts
// web3270 for domain, all or none of them. Called with s.mu held.
func (s *Supervisor) start(domain, bin string) (*instance, error) {
	tnAddr := net.JoinHostPort("127.0.0.1", fmt.Sprint(s.c.TN3270Port))
	tn, err := net.Listen("tcp", tnAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %v", tnAddr, err)
	}
	proxyAddr := net.JoinHostPort(s.c.Host, fmt.Sprint(s.c.ProxyPort))
	pl, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		tn.Close() //nolint:errcheck
		return nil, fmt.Errorf("listen on %s: %v", proxyAddr, err)
	}
	webAddr := net.JoinHostPort("127.0.0.1", fmt.Sprint(s.c.ListenPort))
	cmd := exec.Command(bin, Args(webAddr, s.c.TN3270Port)...)
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.StdoutPipe()
	if err == nil {
		cmd.Stderr = cmd.Stdout
		err = cmd.Start()
	}
	if err != nil {
		tn.Close() //nolint:errcheck
		pl.Close() //nolint:errcheck
		return nil, fmt.Errorf("starting %s: %v", bin, err)
	}
	inst := &instance{
		domain: domain, bin: bin, started: time.Now(),
		cmd: cmd, exited: make(chan struct{}),
		proxy:  &http.Server{Handler: Proxy(webAddr), TLSConfig: s.c.TLS, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10},
		tn3270: tn,
	}
	s.c.Logf("web3270: started %s for %s (pid %d), served on https://%s/web3270", bin, domain, cmd.Process.Pid, proxyAddr)

	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			s.c.Logf("web3270: %s", sc.Text())
		}
		io.Copy(io.Discard, out) //nolint:errcheck // a line too long ends the scan
	}()
	go s.wait(inst)
	go func() {
		// The proxy has no read or write timeouts, which would cut off
		// the long-lived WebSockets the terminals run over; a browser that
		// vanishes without closing its connection is dropped by TCP
		// instead (see tcpconn).
		if err := inst.proxy.Serve(tls.NewListener(tcpconn.NewListener(pl, s.c.Logf), s.c.TLS)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.c.Logf("web3270 proxy: %v", err)
		}
	}()
	go func() {
		for {
			conn, err := tn.Accept()
			if err != nil {
				return // closed by stop
			}
			go s.c.Serve(conn)
		}
	}()
	return inst, nil
}

// wait waits for inst's web3270 to exit; if it was not stopped, its ports
// are closed, and it is started again after the backoff.
func (s *Supervisor) wait(inst *instance) {
	err := inst.cmd.Wait()
	close(inst.exited)
	s.mu.Lock()
	if s.cur != inst {
		s.mu.Unlock()
		return // stopped
	}
	s.cur = nil
	s.problem = fmt.Sprintf("web3270 exited: %v", err)
	if time.Since(inst.started) > time.Minute {
		s.backoff = 0
	}
	if !s.closed {
		s.retryLater()
	}
	backoff := s.backoff
	s.mu.Unlock()
	s.c.Logf("web3270: exited (%v); starting it again in %s", err, backoff)
	s.stop(inst)
}

// stop ends inst's web3270, asking first, then closes its ports. Sessions
// already connected through it end when web3270 does.
func (s *Supervisor) stop(inst *instance) {
	if inst == nil {
		return
	}
	select {
	case <-inst.exited:
	default:
		terminate(inst.cmd.Process)
		select {
		case <-inst.exited:
		case <-time.After(5 * time.Second):
			inst.cmd.Process.Kill() //nolint:errcheck
			<-inst.exited
		}
	}
	inst.proxy.Close()  //nolint:errcheck
	inst.tn3270.Close() //nolint:errcheck
}

// Args are web3270's arguments: listening on webAddr, in kiosk mode,
// locked to the server's loopback TN3270 port, and trusting the proxy,
// on the loopback address, to say who each browser is.
func Args(webAddr string, tn3270Port int) []string {
	return []string{
		"-listen", webAddr,
		"-host", "127.0.0.1",
		"-port", fmt.Sprint(tn3270Port),
		"-lock",
		"-trusted-proxies", "127.0.0.1/32",
	}
}

// Proxy passes requests to web3270 at webAddr, WebSockets included,
// keeping the Host the browser asked for (web3270 checks a WebSocket's
// Origin against it), and saying, in X-Forwarded-For, who it is; any such
// header the browser sent is dropped. / is sent to /web3270, its page.
func Proxy(webAddr string) http.Handler {
	target := &url.URL{Scheme: "http", Host: webAddr}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "The web terminal is not answering; try again shortly.", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/web3270", http.StatusFound)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// writeFile writes text to path, replacing it whole.
func writeFile(path, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone once renamed
	if _, err := tmp.WriteString(text); err != nil {
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
