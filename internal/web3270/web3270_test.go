package web3270

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain runs the test binary as a fake web3270 when told to: it serves
// the request's Host and X-Forwarded-For back, answers an Upgrade by
// echoing, and says hello to the TN3270 port it is locked to.
func TestMain(m *testing.M) {
	if os.Getenv("WEB3270_FAKE") != "1" {
		os.Exit(m.Run())
	}
	fs := flag.NewFlagSet("web3270", flag.ExitOnError)
	listen := fs.String("listen", "", "")
	host := fs.String("host", "", "")
	port := fs.String("port", "", "")
	lock := fs.Bool("lock", false, "")
	trusted := fs.String("trusted-proxies", "", "")
	fs.Parse(os.Args[1:]) //nolint:errcheck
	if !*lock || *host != "127.0.0.1" || *trusted != "127.0.0.1/32" {
		fmt.Println("bad arguments:", os.Args[1:])
		os.Exit(2)
	}
	if c, err := net.Dial("tcp", net.JoinHostPort(*host, *port)); err == nil {
		fmt.Fprint(c, "hello from web3270\n")
		c.Close() //nolint:errcheck
	}
	fmt.Println("fake web3270 listening on", *listen)
	http.ListenAndServe(*listen, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:errcheck,gosec
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close() //nolint:errcheck
			fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			rw.Flush()        //nolint:errcheck
			io.Copy(conn, rw) //nolint:errcheck
			return
		}
		fmt.Fprintf(w, "path=%s host=%s xff=%s", r.URL.Path, r.Host, r.Header.Get("X-Forwarded-For"))
	}))
	os.Exit(1)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	return ln.Addr().(*net.TCPAddr).Port
}

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "adhd.example.com"},
		DNSNames: []string{"adhd.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: k}}}
}

// harness is a Supervisor running the fake web3270.
type harness struct {
	s      *Supervisor
	c      Config
	mu     sync.Mutex
	domain string
	hellos chan string
	logs   chan string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("WEB3270_FAKE", "1")
	h := &harness{domain: "adhd.example.com", hellos: make(chan string, 10), logs: make(chan string, 100)}
	h.c = Config{
		Bin:        os.Args[0],
		File:       filepath.Join(t.TempDir(), "web3270.txt"),
		Host:       "127.0.0.1",
		ProxyPort:  freePort(t),
		ListenPort: freePort(t),
		TN3270Port: freePort(t),
		Serve: func(c net.Conn) {
			defer c.Close() //nolint:errcheck
			line, _ := bufio.NewReader(c).ReadString('\n')
			h.hellos <- strings.TrimSpace(line)
		},
		Domain: func() string { h.mu.Lock(); defer h.mu.Unlock(); return h.domain },
		TLS:    selfSigned(t),
		Logf: func(f string, a ...any) {
			select {
			case h.logs <- fmt.Sprintf(f, a...):
			default:
			}
		},
	}
	h.s = New(h.c)
	t.Cleanup(h.s.Close)
	return h
}

func (h *harness) setDomain(d string) {
	h.mu.Lock()
	h.domain = d
	h.mu.Unlock()
	h.s.Reconcile()
}

// get fetches path through the proxy, as a browser at the domain would.
func (h *harness) get(t *testing.T, path string) (int, string) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // self-signed
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", fmt.Sprint(h.c.ProxyPort)))
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var resp *http.Response
	var err error
	for range 50 { // web3270 takes a moment to listen
		req, _ := http.NewRequest(http.MethodGet, "https://adhd.example.com:32700"+path, nil)
		req.Header.Set("X-Forwarded-For", "6.6.6.6")
		if resp, err = client.Do(req); err == nil && resp.StatusCode != http.StatusBadGateway {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 == 3 {
		return resp.StatusCode, resp.Header.Get("Location")
	}
	return resp.StatusCode, string(b)
}

func (h *harness) waitHello(t *testing.T) {
	t.Helper()
	select {
	case got := <-h.hellos:
		if got != "hello from web3270" {
			t.Errorf("TN3270 port got %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("web3270 never connected to the TN3270 port")
	}
}

func TestSupervisor(t *testing.T) {
	h := newHarness(t)
	if err := h.s.Start(); err != nil {
		t.Fatal(err)
	}
	if st := h.s.Status(); st.Enabled || st.Running || st.Bin == "" {
		t.Fatalf("not enabled: %+v", st)
	}

	if err := h.s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(h.c.File); err != nil || string(data) != "enabled\n" {
		t.Errorf("file %q, %v", data, err)
	}
	st := h.s.Status()
	if !st.Running || st.Domain != "adhd.example.com" {
		t.Fatalf("enabled: %+v", st)
	}
	h.waitHello(t)
	if got := h.s.URL("adhd.example.com:443"); got != fmt.Sprintf("https://adhd.example.com:%d/web3270", h.c.ProxyPort) {
		t.Errorf("URL = %q", got)
	}

	// Through the proxy: the Host kept, X-Forwarded-For the proxy's own.
	if code, body := h.get(t, "/web3270"); code != 200 || body != "path=/web3270 host=adhd.example.com:32700 xff=127.0.0.1" {
		t.Errorf("GET /web3270: %d %q", code, body)
	}
	if code, loc := h.get(t, "/"); code != http.StatusFound || loc != "/web3270" {
		t.Errorf("GET /: %d %q", code, loc)
	}

	// A WebSocket's upgrade passes through, both ways.
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(h.c.ProxyPort)), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(conn, "GET /web3270/ws HTTP/1.1\r\nHost: adhd.example.com:32700\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	r := bufio.NewReader(conn)
	if status, _ := r.ReadString('\n'); !strings.Contains(status, "101") {
		t.Fatalf("upgrade: %q", status)
	}
	for {
		l, err := r.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	fmt.Fprint(conn, "ping\n")
	if echo, _ := r.ReadString('\n'); echo != "ping\n" {
		t.Errorf("WebSocket echo %q", echo)
	}
	conn.Close() //nolint:errcheck

	// A new domain restarts it.
	pid := h.s.cur.cmd.Process.Pid
	h.setDomain("b.example.com")
	if st := h.s.Status(); !st.Running || st.Domain != "b.example.com" || h.s.cur.cmd.Process.Pid == pid {
		t.Errorf("after the domain changed: %+v", st)
	}
	h.waitHello(t)

	// No domain stops it, and says why.
	h.setDomain("")
	if st := h.s.Status(); st.Running || !strings.Contains(st.Problem, "no TLS domain") {
		t.Errorf("with no domain: %+v", st)
	}
	if h.s.URL("adhd.example.com") != "" {
		t.Error("URL while stopped")
	}
	h.setDomain("adhd.example.com")
	h.waitHello(t)

	// Killed, it is started again.
	h.s.mu.Lock()
	inst := h.s.cur
	h.s.mu.Unlock()
	inst.cmd.Process.Kill() //nolint:errcheck
	<-inst.exited
	deadline := time.Now().Add(15 * time.Second)
	for !h.s.Status().Running && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !h.s.Status().Running {
		t.Fatalf("not restarted: %+v", h.s.Status())
	}
	h.waitHello(t)

	// Disabled, it stops, and the file goes.
	if err := h.s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if st := h.s.Status(); st.Running || st.Problem != "" {
		t.Errorf("disabled: %+v", st)
	}
	if _, err := os.Stat(h.c.File); !os.IsNotExist(err) {
		t.Errorf("file left: %v", err)
	}
}

func TestNotInstalled(t *testing.T) {
	h := newHarness(t)
	h.c.Bin = "no-such-web3270-anywhere"
	h.s = New(h.c)
	defer h.s.Close()
	if err := h.s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if st := h.s.Status(); st.Running || st.Bin != "" || !strings.Contains(st.Problem, "not installed") {
		t.Errorf("not installed: %+v", st)
	}
}

func TestStartFile(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.c.File, []byte("maybe\n"), 0o644) //nolint:errcheck
	if err := h.s.Start(); err == nil {
		t.Error("Start with a file saying neither succeeded")
	}
}
