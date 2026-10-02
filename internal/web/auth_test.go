package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/google"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// authRig is a site with users, a Google client, a fake Google token
// endpoint, and a browser with cookies that does not follow redirects.
type authRig struct {
	t       *testing.T
	store   *users.Store
	server  *Server
	srv     *httptest.Server
	browser *http.Client
	auditTo string
	now     time.Time
}

func newAuthRig(t *testing.T) *authRig {
	t.Helper()
	r := &authRig{t: t, now: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	r.store = users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := r.store.Load(); err != nil {
		t.Fatal(err)
	}
	hash, err := users.HashPassword(context.Background(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.Update(func(list *[]users.User, nextID func() int) error {
		*list = append(*list,
			users.User{ID: nextID(), Name: "joelle", Password: hash},
			users.User{ID: nextID(), Name: "calc", Password: hash, Restricted: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.auditTo = filepath.Join(t.TempDir(), "audit.log")
	log, err := audit.Open(r.auditTo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() }) //nolint:errcheck
	r.server = &Server{Users: r.store, Audit: log, Now: func() time.Time { return r.now }}
	r.srv = httptest.NewServer(r.server)
	t.Cleanup(r.srv.Close)
	jar, _ := cookiejar.New(nil)
	r.browser = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// The site at the test server's own address (plain HTTP, so that the
	// cookie jar sends the cookie), and the client.
	if err := r.store.SetSite(&users.Site{BaseURL: r.srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetGoogleClient(&users.GoogleClient{ClientID: "cid", ClientSecret: "cs"}); err != nil {
		t.Fatal(err)
	}

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.ParseForm() //nolint:errcheck
		if req.PostForm.Get("code") != "good-code" || req.PostForm.Get("redirect_uri") != r.srv.URL+"/google/callback" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": "invalid_grant", "error_description": "Bad code"}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"access_token": "at", "refresh_token": "rt", "expires_in": 3600}`)) //nolint:errcheck
	}))
	t.Cleanup(fake.Close)
	old := google.TokenEndpoint
	google.TokenEndpoint = fake.URL
	t.Cleanup(func() { google.TokenEndpoint = old })
	return r
}

// do sends a request, a form posted from the site itself when form is not
// nil, returning the reply and its body.
func (r *authRig) do(method, path string, form url.Values) (*http.Response, string) {
	r.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, r.srv.URL+path, body)
	if err != nil {
		r.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", r.srv.URL)
	}
	resp, err := r.browser.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func (r *authRig) login(name, password string) (*http.Response, string) {
	r.t.Helper()
	return r.do(http.MethodPost, "/google/login", url.Values{"name": {name}, "password": {password}})
}

// link is joelle's Google link as saved.
func (r *authRig) link() *users.GoogleLink {
	r.t.Helper()
	list, _, err := r.store.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return list[1].Google
}

func TestSignInAndConnect(t *testing.T) {
	r := newAuthRig(t)

	resp, body := r.do(http.MethodGet, "/google", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="`+r.srv.URL+`/google/login"`) || !strings.Contains(body, "user name and password you use on the "+AppName) {
		t.Fatalf("sign-in page: %s\n%s", resp.Status, body)
	}
	if !strings.Contains(body, `class="notice"`) || !strings.Contains(body, "<strong>This is not your Google password.</strong>") ||
		!strings.Contains(body, "password (not your Google password)") {
		t.Errorf("sign-in page does not say it is not the Google password:\n%s", body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("sign-in page headers: CSP %q, cache %q", csp, resp.Header.Get("Cache-Control"))
	}
	// A browser posts a form from a page with Referrer-Policy: no-referrer
	// with Origin: null, which the check on where forms come from refuses:
	// the sign-in pages must send their origin to themselves.
	if rp := resp.Header.Get("Referrer-Policy"); rp != "same-origin" {
		t.Errorf("sign-in page Referrer-Policy %q; want same-origin, so forms posted from it carry its origin", rp)
	}

	// Refused: a wrong password, the default admin password, a restricted
	// user, and a form from elsewhere.
	if resp, body := r.login("joelle", "wrong"); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Wrong user name or password.") {
		t.Errorf("wrong password: %s", resp.Status)
	}
	if _, body := r.login("admin", "admin"); !strings.Contains(body, "only works on the console") {
		t.Errorf("default password not refused")
	}
	if _, body := r.login("calc", "secret"); !strings.Contains(body, "cannot use these pages") {
		t.Errorf("restricted user not refused")
	}
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/google/login", strings.NewReader("name=joelle&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	if resp, err := r.browser.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("form from elsewhere: %v %v", resp.Status, err)
	}
	req, _ = http.NewRequest(http.MethodPost, r.srv.URL+"/google/login", strings.NewReader("name=joelle&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	if resp, err := r.browser.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("form with Origin null: %v %v", resp.Status, err)
	}
	if resp, _ := r.do(http.MethodGet, "/google/login", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET of the login: %s", resp.Status)
	}

	// Signing in sets the cookie, for the sign-in pages only, and shows
	// the page for connecting.
	resp, _ = r.login("Joelle", "secret")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != r.srv.URL+"/google" {
		t.Fatalf("sign in: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	cookie := resp.Cookies()[0]
	if cookie.Name != cookieName || cookie.Path != "/google" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie %+v", cookie)
	}
	_, body = r.do(http.MethodGet, "/google", nil)
	if !strings.Contains(body, "Signed in as <strong>joelle</strong>") || !strings.Contains(body, `href="`+r.srv.URL+`/google/start"`) {
		t.Fatalf("connect page:\n%s", body)
	}

	// Connect: the browser is sent to Google with a state; it comes back
	// with it and a code.
	resp, _ = r.do(http.MethodGet, "/google/start", nil)
	to, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(to.String(), google.AuthEndpoint) {
		t.Fatalf("start: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	state := to.Query().Get("state")
	if to.Query().Get("redirect_uri") != r.srv.URL+"/google/callback" {
		t.Errorf("redirect_uri %q", to.Query().Get("redirect_uri"))
	}
	if _, body := r.do(http.MethodGet, "/google/callback?state=other&code=good-code", nil); !strings.Contains(body, "not from a connection begun here") || r.link() != nil {
		t.Errorf("another state was taken")
	}
	// That spent the authorization: even the right state is refused now.
	if _, body := r.do(http.MethodGet, "/google/callback?state="+state+"&code=good-code", nil); !strings.Contains(body, "not from a connection begun here") {
		t.Errorf("an authorization was used twice")
	}

	resp, _ = r.do(http.MethodGet, "/google/start", nil)
	to, _ = url.Parse(resp.Header.Get("Location"))
	resp, body = r.do(http.MethodGet, "/google/callback?"+url.Values{"state": {to.Query().Get("state")}, "code": {"good-code"}}.Encode(), nil)
	if l := r.link(); resp.StatusCode != http.StatusOK || l == nil || l.ClientID != "cid" || l.RefreshToken != "rt" || !strings.Contains(body, "Connected.") {
		t.Fatalf("callback: %s, link %+v\n%s", resp.Status, l, body)
	}
	if !strings.Contains(body, "Your Google calendar is connected") || !strings.Contains(body, "Connect again") ||
		!strings.Contains(body, `href="`+r.srv.URL+`/3270/"`) {
		t.Errorf("connect page once connected:\n%s", body)
	}

	// Google refusing, or the code being bad, says so.
	resp, _ = r.do(http.MethodGet, "/google/start", nil)
	to, _ = url.Parse(resp.Header.Get("Location"))
	if _, body := r.do(http.MethodGet, "/google/callback?state="+to.Query().Get("state")+"&error=access_denied", nil); !strings.Contains(body, "access_denied") {
		t.Errorf("access denied not reported")
	}
	resp, _ = r.do(http.MethodGet, "/google/start", nil)
	to, _ = url.Parse(resp.Header.Get("Location"))
	if _, body := r.do(http.MethodGet, "/google/callback?state="+to.Query().Get("state")+"&code=bad", nil); !strings.Contains(body, "Bad code") {
		t.Errorf("bad code not reported")
	}

	// Signing out needs the session's own token. With it, the session ends
	// on the server, so even the old cookie no longer works. (Each sign-out
	// clears the cookie in the browser; it is put back to check.)
	restore := func() { r.browser.Jar.SetCookies(mustURL(r.srv.URL+"/google"), []*http.Cookie{cookie}) }
	r.do(http.MethodPost, "/google/logout", url.Values{"csrf": {"wrong"}})
	restore()
	_, page := r.do(http.MethodGet, "/google", nil)
	if !strings.Contains(page, "Signed in as") {
		t.Fatalf("signed out without the session's token")
	}
	r.do(http.MethodPost, "/google/logout", url.Values{"csrf": {between(page, `name="csrf" value="`, `"`)}})
	restore()
	if _, page := r.do(http.MethodGet, "/google", nil); strings.Contains(page, "Signed in as") {
		t.Errorf("still signed in after signing out")
	}

	data, _ := os.ReadFile(r.auditTo)
	for _, want := range []string{"LOGIN user=joelle via=web", "LOGIN-FAILED user=joelle via=web", "reason=\"wrong password\"", "LOGOUT user=joelle via=web"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("audit log lacks %q:\n%s", want, data)
		}
	}
}

func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	v, _, _ := strings.Cut(rest, to)
	return v
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func TestSignInThrottled(t *testing.T) {
	r := newAuthRig(t)
	for range maxFailures {
		r.login("joelle", "wrong")
	}
	if _, body := r.login("joelle", "secret"); !strings.Contains(body, "Too many failed sign-ins") {
		t.Fatalf("not throttled after %d failures", maxFailures)
	}
	// Other names are not.
	if _, body := r.login("calc", "secret"); strings.Contains(body, "Too many") {
		t.Errorf("another name throttled")
	}
	// After the window, the name can sign in again.
	r.now = r.now.Add(failureWindow + time.Second)
	if resp, _ := r.login("joelle", "secret"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("still throttled after the window: %s", resp.Status)
	}
}

func TestSessionEnds(t *testing.T) {
	r := newAuthRig(t)
	r.login("joelle", "secret")
	r.now = r.now.Add(sessionLife + time.Second)
	if _, body := r.do(http.MethodGet, "/google", nil); strings.Contains(body, "Signed in as") {
		t.Errorf("signed in past the session's life")
	}

	// A user removed is signed out.
	r.now = r.now.Add(time.Minute)
	r.login("joelle", "secret")
	if err := r.store.Update(func(list *[]users.User, _ func() int) error {
		*list = (*list)[:1]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, body := r.do(http.MethodGet, "/google", nil); strings.Contains(body, "Signed in as") {
		t.Errorf("a removed user is still signed in")
	}
}

func TestSignInNotSetUp(t *testing.T) {
	r := newAuthRig(t)
	if err := r.store.SetGoogleClient(nil); err != nil {
		t.Fatal(err)
	}
	if resp, body := r.do(http.MethodGet, "/google", nil); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "No Google client") {
		t.Errorf("no client: %s", resp.Status)
	}
	if err := r.store.SetSite(nil); err != nil {
		t.Fatal(err)
	}
	if _, body := r.do(http.MethodGet, "/google", nil); !strings.Contains(body, "public address has not been set") {
		t.Errorf("no site address")
	}
}

func TestCookieSecure(t *testing.T) {
	if !secure("https://adhd.example.com/") || secure("http://localhost:3280/") {
		t.Errorf("secure wrong")
	}
	if got := fmt.Sprint(cookiePaths("https://example.com/adhd/")); got != "[/adhd/google /adhd/trello]" {
		t.Errorf("cookie paths %s", got)
	}
	if got := fmt.Sprint(cookiePaths("https://example.com/")); got != "[/google /trello]" {
		t.Errorf("cookie paths %s", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/google/login", nil)
	req.Header.Set("Referer", "https://adhd.example.com/google")
	if !sameOrigin(req, "https://adhd.example.com/") {
		t.Errorf("Referer from the site refused")
	}
	req.Header.Set("Origin", "https://adhd.example.com.evil.example")
	if sameOrigin(req, "https://adhd.example.com/") {
		t.Errorf("another origin accepted")
	}
}

func TestTrelloLink(t *testing.T) {
	r := newAuthRig(t)
	trello := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if req.URL.Path != "/1/members/me" || q.Get("key") != "tkey" || q.Get("token") != "GoodToken123" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"id": "m1", "username": "joelle_t"}`)) //nolint:errcheck
	}))
	defer trello.Close()
	r.server.TrelloBaseURL = trello.URL
	if resp, body := r.do(http.MethodGet, "/trello", nil); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "No Trello API key") {
		t.Errorf("no key: %s", resp.Status)
	}
	if err := r.store.SetTrelloClient(&users.TrelloClient{APIKey: "tkey"}); err != nil {
		t.Fatal(err)
	}

	_, body := r.do(http.MethodGet, "/trello", nil)
	if !strings.Contains(body, "<h1>Link your Trello account</h1>") || !strings.Contains(body, "This is not your Trello password.") ||
		!strings.Contains(body, `action="`+r.srv.URL+`/trello/login"`) {
		t.Fatalf("Trello sign-in page:\n%s", body)
	}
	// Signing in here signs in for both, by a cookie for each.
	resp, _ := r.login2("/trello/login")
	if resp.Header.Get("Location") != r.srv.URL+"/trello" || len(resp.Cookies()) != 2 {
		t.Fatalf("sign in: to %q, cookies %v", resp.Header.Get("Location"), resp.Cookies())
	}
	if _, body := r.do(http.MethodGet, "/google", nil); !strings.Contains(body, "Signed in as") {
		t.Errorf("not signed in on /google after signing in on /trello")
	}
	_, body = r.do(http.MethodGet, "/trello", nil)
	csrf := between(body, `name="csrf" value="`, `"`)
	if !strings.Contains(body, "Link your Trello account</a>") {
		t.Fatalf("link page:\n%s", body)
	}

	// A token posted with no link begun is refused.
	if _, body := r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {csrf}, "token": {"GoodToken123"}}); !strings.Contains(body, "not from a link begun here") || r.trello() != nil {
		t.Errorf("token with no link begun taken")
	}

	// Begun, the browser goes to Trello, which sends it back to the
	// callback, whose script posts the token.
	resp, _ = r.do(http.MethodGet, "/trello/start", nil)
	to, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || to.Query().Get("key") != "tkey" || to.Query().Get("return_url") != r.srv.URL+"/trello/callback" {
		t.Fatalf("start: %s to %q", resp.Status, resp.Header.Get("Location"))
	}
	resp, body = r.do(http.MethodGet, "/trello/callback", nil)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src '"+trelloScriptHash+"'") || !strings.Contains(body, "<script>"+trelloScript+"</script>") {
		t.Errorf("callback: CSP %q\n%s", csp, body)
	}
	// Without the session's token, refused; that spends the link.
	r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {"wrong"}, "token": {"GoodToken123"}})
	if r.trello() != nil {
		t.Fatalf("token taken without the session's token")
	}
	r.do(http.MethodGet, "/trello/start", nil)
	if _, body := r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {csrf}, "token": {"BadToken"}}); !strings.Contains(body, "Trello did not accept the token") {
		t.Errorf("bad token: %s", body)
	}
	r.do(http.MethodGet, "/trello/start", nil)
	if _, body := r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {csrf}, "token": {"not a token!"}}); !strings.Contains(body, "no usable token") {
		t.Errorf("malformed token taken")
	}
	r.do(http.MethodGet, "/trello/start", nil)
	_, body = r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {csrf}, "token": {"GoodToken123"}})
	if l := r.trello(); l == nil || l.APIKey != "tkey" || l.Token != "GoodToken123" || l.Username != "joelle_t" {
		t.Fatalf("linked %+v:\n%s", l, body)
	}
	if !strings.Contains(body, "Linked.") || !strings.Contains(body, "<strong>joelle_t</strong>") || !strings.Contains(body, `href="`+r.srv.URL+`/3270/"`) {
		t.Errorf("linked page:\n%s", body)
	}
	// Used once.
	if _, body := r.do(http.MethodPost, "/trello/token", url.Values{"csrf": {csrf}, "token": {"GoodToken123"}}); !strings.Contains(body, "not from a link begun here") {
		t.Errorf("a link used twice")
	}
}

// login2 signs joelle in at path.
func (r *authRig) login2(path string) (*http.Response, string) {
	r.t.Helper()
	return r.do(http.MethodPost, path, url.Values{"name": {"joelle"}, "password": {"secret"}})
}

// trello is joelle's Trello link as saved.
func (r *authRig) trello() *users.TrelloLink {
	r.t.Helper()
	list, _, err := r.store.Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return list[1].Trello
}
