package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// newStore is a users file holding site, if it is not nil.
func newStore(t *testing.T, site *users.Site) *users.Store {
	t.Helper()
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if site != nil {
		if err := store.SetSite(site); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// get fetches path from a Server over site, returning the reply and its
// body.
func get(t *testing.T, site *users.Site, method, path string) (*http.Response, string) {
	t.Helper()
	srv := httptest.NewServer(&Server{Users: newStore(t, site)})
	defer srv.Close()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPages(t *testing.T) {
	site := &users.Site{BaseURL: "https://adhd.example.com/", Organization: "Example Org", Contact: "it@example.com"}
	for path, wants := range map[string][]string{
		"/": {
			"<title>" + AppName + "</title>", `<link rel="canonical" href="https://adhd.example.com/">`,
			`href="https://adhd.example.com/privacy"`, `href="https://adhd.example.com/terms"`,
			"dashboard created by Example Org",
			"keep track of what they have to\ndo: their tasks, their appointments and their checklists",
			"help with\nexecutive function", "read-only access",
		},
		"/privacy": {
			"<title>Privacy policy – " + AppName + "</title>", `href="https://adhd.example.com/privacy">`,
			"Limited Use requirements", "calendar.readonly", "Last updated " + Updated, `mailto:it@example.com`,
			"api-services-user-data-policy",
		},
		"/terms/": {
			"<title>Terms of service – " + AppName + "</title>", `href="https://adhd.example.com/terms">`,
			"Acceptable use", "Example Org may", `mailto:it@example.com`,
		},
	} {
		resp, body := get(t, site, http.MethodGet, path)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: %s, %s", path, resp.Status, resp.Header.Get("Content-Type"))
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
			if resp.Header.Get(h) == "" {
				t.Errorf("%s: no %s header", path, h)
			}
		}
		if strings.Contains(body, "<script") {
			t.Errorf("%s has a script", path)
		}
	}
}

func TestPagesUnset(t *testing.T) {
	// With nothing set, links are by path, and the organization described.
	_, body := get(t, nil, http.MethodGet, "/privacy")
	for _, want := range []string{`href="/terms"`, "the organization that runs this service", "the administrators who gave you your account"} {
		if !strings.Contains(body, want) {
			t.Errorf("unset site's privacy page lacks %q", want)
		}
	}
	// With only an address, the organization is named by its host.
	_, body = get(t, &users.Site{BaseURL: "https://adhd.example.com/"}, http.MethodGet, "/")
	if !strings.Contains(body, "the operators of adhd.example.com") {
		t.Errorf("home page with only an address does not name the host")
	}
}

func TestPagesEscape(t *testing.T) {
	site := &users.Site{BaseURL: "https://x.example/", Organization: `<b>Evil</b> & "Co"`, Contact: `a@b.c"><script>`}
	_, body := get(t, site, http.MethodGet, "/terms")
	if strings.Contains(body, "<b>Evil</b>") || strings.Contains(body, "<script>") {
		t.Errorf("settings not escaped:\n%s", body)
	}
}

func TestPagesOther(t *testing.T) {
	if resp, _ := get(t, nil, http.MethodGet, "/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/nope: %s", resp.Status)
	}
	if resp, _ := get(t, nil, http.MethodPost, "/"); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %s, Allow %q", resp.Status, resp.Header.Get("Allow"))
	}
	if resp, body := get(t, nil, http.MethodHead, "/"); resp.StatusCode != http.StatusOK || body != "" {
		t.Errorf("HEAD: %s, body %q", resp.Status, body)
	}
	if resp, body := get(t, nil, http.MethodGet, "/robots.txt"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Allow: /") {
		t.Errorf("robots.txt: %s %q", resp.Status, body)
	}
	// A settings file that cannot be read still gives the pages.
	broken := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(broken, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Users: users.NewStore(broken)})
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with unreadable settings: %s", resp.Status)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://adhd.example.com":       "https://adhd.example.com/",
		" https://adhd.example.com/ ":    "https://adhd.example.com/",
		"https://example.com/adhd":       "https://example.com/adhd/",
		"http://localhost:3280/":         "http://localhost:3280/",
		"adhd.example.com":               "",
		"ftp://adhd.example.com/":        "",
		"https:///nohost":                "",
		"https://adhd.example.com/?q=1":  "",
		"https://adhd.example.com/#x":    "",
		"https://me@adhd.example.com/":   "",
		"https://adhd.example.com/%zz/":  "",
		"javascript:alert(1)":            "",
		"https://adhd.example.com/a b/":  "https://adhd.example.com/a%20b/",
		"HTTPS://Adhd.Example.com/path/": "https://Adhd.Example.com/path/",
	} {
		got, err := NormalizeBaseURL(in)
		if want == "" && err == nil {
			t.Errorf("%q: %q accepted", in, got)
		}
		if want != "" && (err != nil || got != want) {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
}
