package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/google"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// The sign-in and connect pages, under /google, for a Google calendar, and
// /trello, for a Trello account. Under each:
//
//   - GET "": the sign-in form, or once signed in, the page for connecting;
//   - POST /login: signs in, with the user name and password used on the
//     terminal;
//   - GET /start: sends the browser to Google or Trello to allow access;
//   - GET /callback: where it comes back. From Google, it brings a code,
//     traded for the user's refresh token. From Trello, it brings the
//     token in the address's fragment, which only the browser sees: the
//     page has a script post it to /trello/token;
//   - POST /trello/token: takes the Trello token;
//   - POST /logout: signs out.
//
// A sign-in lasts sessionLife, in memory only, kept by a cookie that only
// this site's /google and /trello pages get. Forms are posted only from the
// site itself (see sameOrigin), and those posted once signed in carry the
// session's own token too.
const (
	cookieName  = "exec3270_session"
	sessionLife = 30 * time.Minute
	authLife    = 10 * time.Minute // for coming back from Google or Trello

	// After maxFailures failed sign-ins as one name within failureWindow,
	// that name cannot sign in here until the oldest is that old.
	maxFailures   = 5
	failureWindow = 15 * time.Minute

	googleWait = 30 * time.Second
)

// session is one browser signed in.
type session struct {
	userID  int
	name    string
	csrf    string
	expires time.Time

	// auth is the authorization begun at Google, until it comes back.
	auth        *google.Authorization
	authClient  string // the client it was begun with
	authExpires time.Time

	// trelloKey is the API key a Trello authorization was begun with,
	// until trelloExpires, or until its token comes back.
	trelloKey     string
	trelloExpires time.Time
}

// service is Google or Trello, as the sign-in and connect pages name it.
type service struct {
	path         string // under the site's address: "google" or "trello"
	title        string // the pages' heading
	provider     string // whose password is not this site's
	providerHost string // where the provider asks for it
	why          string // what connecting does, after signing in
}

var (
	googleService = service{"google", "Connect your Google calendar", "Google", "accounts.google.com",
		"Then you can connect your Google calendar, so that your meetings show on your dashboard."}
	trelloService = service{"trello", "Link your Trello account", "Trello", "trello.com",
		"Then you can link your Trello account, so that the tasks on the Trello lists you choose show on your dashboard."}
)

// services are the paths under which there are sign-in pages, which the
// cookie is for.
var services = []service{googleService, trelloService}

// serveConnect serves the pages under /google or /trello, as svc says;
// path is the request's, with no trailing slash.
func (s *Server) serveConnect(w http.ResponseWriter, r *http.Request, path string, svc service) {
	site := s.site()
	p := pageFor(site, svc.title)
	p.Here = p.Home + svc.path
	p.Canonical = p.Here
	p.Title, p.Provider, p.ProviderHost, p.Why = svc.title, svc.provider, svc.providerHost, svc.why
	_, gClient, err := s.Users.GoogleClient()
	if err != nil {
		log.Printf("web: reading the users: %v", err)
	}
	_, tClient, err := s.Users.TrelloClient()
	if err != nil {
		log.Printf("web: reading the users: %v", err)
	}
	switch {
	case site == nil || site.BaseURL == "":
		p.Problem = "This site's public address has not been set yet. An administrator sets it on the terminal's admin menu."
	case svc == googleService && gClient == nil:
		p.Problem = "No Google client has been set up yet. An administrator sets one on the terminal's admin menu."
	case svc == trelloService && tClient == nil:
		p.Problem = "No Trello API key has been set up yet. An administrator sets one on the terminal's admin menu."
	}

	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	route := strings.TrimPrefix(path, "/"+svc.path)
	allowed := map[string]string{"": http.MethodGet, "/login": http.MethodPost, "/start": http.MethodGet, "/callback": http.MethodGet, "/logout": http.MethodPost}
	if svc == trelloService {
		allowed["/token"] = http.MethodPost
	}
	want, ok := allowed[route]
	switch {
	case !ok:
		http.NotFound(w, r)
		return
	case method != want:
		w.Header().Set("Allow", want)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	case want == http.MethodPost && !sameOrigin(r, p.Home):
		http.Error(w, "forbidden: this form must be sent from "+p.Home, http.StatusForbidden)
		return
	}

	sess, token := s.lookup(r)
	if p.Problem != "" {
		render(w, r, signInPage, p, http.StatusServiceUnavailable)
		return
	}
	if sess == nil && route != "/login" && route != "/logout" {
		if route != "" {
			p.Message, p.IsError = "Your sign-in here has expired, or you have not signed in. Sign in, then try again.", true
		}
		render(w, r, signInPage, p, http.StatusOK)
		return
	}
	switch route {
	case "":
		s.renderConnect(w, r, p, svc, sess, "", false)
	case "/login":
		s.login(w, r, p, site)
	case "/logout":
		if sess != nil && s.csrfOK(r, sess) {
			s.mu.Lock()
			delete(s.sessions, token)
			s.mu.Unlock()
			s.record(audit.Logout, r, sess.name)
			log.Printf("web: %s signed out", sess.name)
		}
		for _, path := range cookiePaths(p.Home) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: secure(p.Home), SameSite: http.SameSiteLaxMode})
		}
		http.Redirect(w, r, p.Here, http.StatusSeeOther)
	case "/start":
		if svc == trelloService {
			s.mu.Lock()
			sess.trelloKey, sess.trelloExpires = tClient.APIKey, s.now().Add(authLife)
			s.mu.Unlock()
			http.Redirect(w, r, tasks.AuthorizeURL(tClient.APIKey, AppName, p.Here+"/callback"), http.StatusFound)
			return
		}
		auth, err := google.Begin(google.Client{ID: gClient.ClientID, Secret: gClient.ClientSecret}, p.Home+"google/callback")
		if err != nil {
			s.renderConnect(w, r, p, svc, sess, "Could not begin: "+err.Error(), true)
			return
		}
		s.mu.Lock()
		sess.auth, sess.authClient, sess.authExpires = &auth, gClient.ClientID, s.now().Add(authLife)
		s.mu.Unlock()
		http.Redirect(w, r, auth.URL, http.StatusFound)
	case "/callback":
		if svc == trelloService {
			w.Header().Set("Content-Security-Policy", csp(true)+"; script-src '"+trelloScriptHash+"'")
			p.Name, p.CSRF = sess.name, sess.csrf
			render(w, r, trelloCallbackPage, p, http.StatusOK)
			return
		}
		s.callback(w, r, p, sess, gClient)
	case "/token":
		s.trelloToken(w, r, p, sess, tClient)
	}
}

// csrfOK reports whether the form posted in r carries sess's own token.
func (s *Server) csrfOK(r *http.Request, sess *session) bool {
	return subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.csrf)) == 1
}

// login checks the user name and password posted, by the terminal's rules:
// the default admin password is refused, and so is a restricted user, who
// has only the calculator. A name with too many failed sign-ins is refused
// without checking.
func (s *Server) login(w http.ResponseWriter, r *http.Request, p page, site *users.Site) {
	name, password := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("password")
	fail := func(reason, message string, count bool) {
		if count {
			s.mu.Lock()
			key := strings.ToLower(name)
			s.failures[key] = append(s.recentFailures(key), s.now())
			n := len(s.failures[key])
			s.mu.Unlock()
			s.record(audit.LoginFailed, r, name, audit.F("try", strconv.Itoa(n)), audit.F("reason", reason))
			log.Printf("web: sign-in as %q failed: %s", name, reason)
		}
		p.Message, p.IsError = message, true
		render(w, r, signInPage, p, http.StatusUnauthorized)
	}
	if name == "" || password == "" {
		fail("", "Type both your user name and your password.", false)
		return
	}
	s.mu.Lock()
	if s.failures == nil {
		s.failures = map[string][]time.Time{}
	}
	recent := s.recentFailures(strings.ToLower(name))
	s.mu.Unlock()
	if len(recent) >= maxFailures {
		wait := recent[0].Add(failureWindow).Sub(s.now()).Round(time.Minute)
		fail("too many failures", fmt.Sprintf("Too many failed sign-ins as %s; try again in %v.", name, max(wait, time.Minute)), false)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), googleWait)
	defer cancel()
	u, ok, err := s.Users.Authenticate(ctx, name, password)
	switch {
	case errors.Is(err, users.ErrBusy):
		fail("", "The server is busy; try again in a moment.", false)
		return
	case err != nil:
		log.Printf("web: sign-in as %q: %v", name, err)
		fail("", "Could not check the password.", false)
		return
	case ok && users.IsDefaultLogin(u.Name, password):
		fail("default password", "The default admin password only works on the console; change it there.", true)
		return
	case !ok:
		fail("wrong password", "Wrong user name or password.", true)
		return
	case u.Restricted:
		fail("restricted", "Your account cannot use these pages.", true)
		return
	}

	token, err := randomToken()
	if err == nil {
		var csrf string
		if csrf, err = randomToken(); err == nil {
			s.mu.Lock()
			if s.sessions == nil {
				s.sessions = map[string]*session{}
			}
			for t, x := range s.sessions {
				if !s.now().Before(x.expires) {
					delete(s.sessions, t)
				}
			}
			s.sessions[token] = &session{userID: u.ID, name: u.Name, csrf: csrf, expires: s.now().Add(sessionLife)}
			delete(s.failures, strings.ToLower(name))
			s.mu.Unlock()
		}
	}
	if err != nil {
		fail("", "Could not sign you in: "+err.Error(), false)
		return
	}
	s.record(audit.Login, r, u.Name)
	log.Printf("web: %s signed in", u.Name)
	for _, path := range cookiePaths(site.BaseURL) {
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: token, Path: path, MaxAge: int(sessionLife / time.Second),
			HttpOnly: true, Secure: secure(site.BaseURL), SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, p.Here, http.StatusSeeOther)
}

// callback takes the browser back from Google: the code it brings, if it
// carries the state of the authorization this browser's session began,
// is traded for a refresh token, which becomes the user's.
func (s *Server) callback(w http.ResponseWriter, r *http.Request, p page, sess *session, client *users.GoogleClient) {
	q := r.URL.Query()
	s.mu.Lock()
	auth, authClient, expires := sess.auth, sess.authClient, sess.authExpires
	sess.auth = nil // good for one try
	s.mu.Unlock()
	switch {
	case auth == nil || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(auth.State)) != 1 || !s.now().Before(expires):
		s.renderConnect(w, r, p, googleService, sess, "That was not from a connection begun here, or it took too long. Connect again.", true)
		return
	case q.Get("error") != "":
		s.renderConnect(w, r, p, googleService, sess, "Google did not grant access ("+q.Get("error")+").", true)
		return
	case q.Get("code") == "":
		s.renderConnect(w, r, p, googleService, sess, "Google sent no authorization code. Connect again.", true)
		return
	case authClient != client.ClientID:
		s.renderConnect(w, r, p, googleService, sess, "The Google client changed meanwhile. Connect again.", true)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), googleWait)
	token, err := auth.Exchange(ctx, google.Client{ID: client.ClientID, Secret: client.ClientSecret}, q.Get("code"))
	cancel()
	if err != nil {
		log.Printf("web: %s: trading Google's code: %v", sess.name, err)
		s.renderConnect(w, r, p, googleService, sess, "Google refused the authorization ("+err.Error()+"). Connect again.", true)
		return
	}
	if err := s.Users.ConnectGoogle(sess.userID, client.ClientID, token); err != nil {
		log.Printf("web: %s: saving the Google authorization: %v", sess.name, err)
		s.renderConnect(w, r, p, googleService, sess, "Could not save the authorization: "+err.Error(), true)
		return
	}
	log.Printf("web: %s connected a Google calendar", sess.name)
	s.renderConnect(w, r, p, googleService, sess, "Connected. Now go back to your terminal to choose which calendars to show.", false)
}

// trelloToken takes the Trello token the callback page posted, if this
// browser's session began a Trello authorization, with the key still set,
// not long ago; it is checked with Trello, and becomes the user's.
func (s *Server) trelloToken(w http.ResponseWriter, r *http.Request, p page, sess *session, client *users.TrelloClient) {
	token := strings.TrimSpace(r.PostFormValue("token"))
	s.mu.Lock()
	key, expires := sess.trelloKey, sess.trelloExpires
	sess.trelloKey = "" // good for one try
	s.mu.Unlock()
	switch {
	case !s.csrfOK(r, sess):
		s.renderConnect(w, r, p, trelloService, sess, "That did not come from this page. Link again.", true)
		return
	case key == "" || !s.now().Before(expires):
		s.renderConnect(w, r, p, trelloService, sess, "That was not from a link begun here, or it took too long. Link again.", true)
		return
	case key != client.APIKey:
		s.renderConnect(w, r, p, trelloService, sess, "The Trello API key changed meanwhile. Link again.", true)
		return
	case token == "" || len(token) > 200 || strings.ContainsFunc(token, func(c rune) bool {
		return (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z')
	}):
		s.renderConnect(w, r, p, trelloService, sess, "Trello sent no usable token. Link again.", true)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), googleWait)
	username, err := tasks.Member(ctx, tasks.Config{APIKey: key, Token: token, BaseURL: s.TrelloBaseURL})
	cancel()
	if err != nil {
		log.Printf("web: %s: checking the Trello token: %v", sess.name, err)
		s.renderConnect(w, r, p, trelloService, sess, "Trello did not accept the token ("+err.Error()+"). Link again.", true)
		return
	}
	if err := s.Users.ConnectTrello(sess.userID, key, token, username); err != nil {
		log.Printf("web: %s: saving the Trello token: %v", sess.name, err)
		s.renderConnect(w, r, p, trelloService, sess, "Could not save the token: "+err.Error(), true)
		return
	}
	log.Printf("web: %s linked the Trello account %s", sess.name, username)
	s.renderConnect(w, r, p, trelloService, sess, "Linked. Now go back to your terminal to choose which lists to show.", false)
}

// renderConnect shows the page for connecting sess's user's Google calendar
// or Trello account, as svc says.
func (s *Server) renderConnect(w http.ResponseWriter, r *http.Request, p page, svc service, sess *session, message string, isError bool) {
	list, gClient, err := s.Users.GoogleClient()
	if err != nil {
		log.Printf("web: reading the users: %v", err)
	}
	_, tClient, err := s.Users.TrelloClient()
	if err != nil {
		log.Printf("web: reading the users: %v", err)
	}
	if i := slices.IndexFunc(list, func(u users.User) bool { return u.ID == sess.userID }); i >= 0 {
		u := list[i]
		p.Connected = u.Connected(gClient)
		if svc == trelloService {
			p.Connected = u.TrelloLinked(tClient)
			if p.Connected {
				p.Account = u.Trello.Username
			}
		}
	}
	p.Name, p.CSRF, p.Message, p.IsError = sess.name, sess.csrf, message, isError
	status := http.StatusOK
	if isError {
		status = http.StatusBadRequest
	}
	tmpl := connectPage
	if svc == trelloService {
		tmpl = trelloConnectPage
	}
	render(w, r, tmpl, p, status)
}

// lookup returns the session the request's cookie names, and its token, or
// nil for none, or one expired or of a user since removed or restricted.
func (s *Server) lookup(r *http.Request) (*session, string) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, ""
	}
	s.mu.Lock()
	sess := s.sessions[c.Value]
	if sess != nil && !s.now().Before(sess.expires) {
		delete(s.sessions, c.Value)
		sess = nil
	}
	s.mu.Unlock()
	if sess == nil {
		return nil, ""
	}
	list, _, err := s.Users.Load()
	if err != nil {
		return nil, ""
	}
	if i := slices.IndexFunc(list, func(u users.User) bool { return u.ID == sess.userID }); i < 0 || list[i].Restricted {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
		return nil, ""
	}
	return sess, c.Value
}

// recentFailures are the failed sign-ins as key within failureWindow,
// oldest first. The caller holds s.mu.
func (s *Server) recentFailures(key string) []time.Time {
	cutoff := s.now().Add(-failureWindow)
	recent := slices.DeleteFunc(slices.Clone(s.failures[key]), func(t time.Time) bool { return t.Before(cutoff) })
	if len(recent) == 0 {
		delete(s.failures, key)
	} else {
		s.failures[key] = recent
	}
	return recent
}

// record adds a sign-in event to the audit log, marked as from the web,
// with the address the request came from and any it was forwarded for.
func (s *Server) record(event string, r *http.Request, name string, extra ...audit.Field) {
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	fields := []audit.Field{audit.F("user", name), audit.F("via", "web"), audit.F("ip", ip)}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		fields = append(fields, audit.F("forwarded_for", fwd))
	}
	s.Audit.Record(event, append(fields, extra...)...)
}

// sameOrigin reports whether a form posted in r came from the site at home:
// by its Origin header, or failing that its Referer.
func sameOrigin(r *http.Request, home string) bool {
	u, err := url.Parse(home)
	if err != nil || u.Host == "" {
		return false
	}
	want := u.Scheme + "://" + u.Host
	if origin := r.Header.Get("Origin"); origin != "" {
		return strings.EqualFold(origin, want)
	}
	ref, err := url.Parse(r.Header.Get("Referer"))
	return err == nil && strings.EqualFold(ref.Scheme+"://"+ref.Host, want)
}

// cookiePaths are the paths of the sign-in pages under home, which only
// they get the cookie for.
func cookiePaths(home string) []string {
	base := ""
	if u, err := url.Parse(home); err == nil {
		base = strings.TrimSuffix(u.Path, "/")
	}
	var out []string
	for _, svc := range services {
		out = append(out, base+"/"+svc.path)
	}
	return out
}

// secure reports whether home is reached over HTTPS, so that the cookie is
// to be sent only over it.
func secure(home string) bool {
	return strings.HasPrefix(strings.ToLower(home), "https://")
}

// randomToken is 32 random bytes, base64url encoded.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

var signInPage = mustPage(`
<h1>{{.Title}}</h1>
{{if .Problem}}
<p>{{.Problem}}</p>
{{else}}
<p>Sign in with the user name and password you use on the {{.AppName}}
terminal. {{.Why}}</p>
<div class="notice" role="note"><p><strong>This is not your {{.Provider}} password.</strong>
Use your {{.AppName}} user name and password here, the ones you use on the
terminal. Never type your {{.Provider}} password on this page: {{.Provider}} asks for it
only on its own sign-in page, at <code>{{.ProviderHost}}</code>, after you
choose to connect.</p></div>
{{if .Message}}<p class="{{if .IsError}}error{{else}}ok{{end}}">{{.Message}}</p>{{end}}
<form method="post" action="{{.Here}}/login">
<p><label>{{.AppName}} user name<br><input name="name" maxlength="{{maxName}}" autocomplete="username" autocapitalize="none" spellcheck="false" required autofocus></label></p>
<p><label>{{.AppName}} password (not your {{.Provider}} password)<br><input name="password" type="password" autocomplete="current-password" required></label></p>
<p><button type="submit">Sign in</button></p>
</form>
<p class="meta">Signing in sets one cookie, which keeps you signed in on these
pages for {{sessionMinutes}} minutes. See the <a href="{{.Privacy}}">privacy policy</a>.</p>
{{end}}
`)

var connectPage = mustPage(`
<h1>Connect your Google calendar</h1>
<p>Signed in as <strong>{{.Name}}</strong>.</p>
{{if .Message}}<p class="{{if .IsError}}error{{else}}ok{{end}}">{{.Message}}</p>{{end}}
{{if .Connected}}
<p>Your Google calendar is connected. To choose which of its calendars show on
your dashboard, type <code>GOOGLE</code> on the terminal's command line (or, if
the terminal is showing the steps for connecting, press Enter there).</p>
<p><a class="button" href="{{.Terminal}}">Go to the terminal</a></p>
<p><a href="{{.Here}}/start">Connect again</a></p>
{{else}}
<p>{{.AppName}} asks Google for read-only access to your calendars, to list
them for you to choose from and to show the events of those you choose. It
never changes a calendar. See the <a href="{{.Privacy}}">privacy policy</a>.</p>
<p><a class="button" href="{{.Here}}/start">Connect your Google calendar</a></p>
<p>Then, on the terminal, type <code>GOOGLE</code> (or press Enter on the screen
it shows) to choose which calendars to show.</p>
{{end}}
<form method="post" action="{{.Here}}/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><button type="submit" class="link">Sign out</button></p></form>
`)

var trelloConnectPage = mustPage(`
<h1>Link your Trello account</h1>
<p>Signed in as <strong>{{.Name}}</strong>.</p>
{{if .Message}}<p class="{{if .IsError}}error{{else}}ok{{end}}">{{.Message}}</p>{{end}}
{{if .Connected}}
<p>Your Trello account{{if .Account}}, <strong>{{.Account}}</strong>,{{end}} is linked.
To choose which of its lists show on your dashboard, type <code>TRELLO</code> on
the terminal's command line (or, if the terminal is showing the steps for
linking, press Enter there).</p>
<p><a class="button" href="{{.Terminal}}">Go to the terminal</a></p>
<p><a href="{{.Here}}/start">Link again</a></p>
{{else}}
<p>{{.AppName}} asks Trello for access to your boards: to list them and their
lists for you to choose from, to show the cards on the lists you choose as your
tasks, and to add cards and archive them when you add or finish a task on the
terminal. See the <a href="{{.Privacy}}">privacy policy</a>.</p>
<p><a class="button" href="{{.Here}}/start">Link your Trello account</a></p>
<p>Then, on the terminal, type <code>TRELLO</code> (or press Enter on the screen
it shows) to choose which lists to show.</p>
{{end}}
<form method="post" action="{{.Here}}/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><p><button type="submit" class="link">Sign out</button></p></form>
`)

// trelloScript, on the page Trello sends the browser back to, posts the
// token in the address's fragment, which the server never sees, and takes
// it out of the address. It is the only script on the site; the page's
// Content Security Policy allows it alone, by its hash.
const trelloScript = `var m=/[#&]token=([^&]+)/.exec(location.hash);` +
	`if(m){var f=document.getElementById("got");f.elements.token.value=decodeURIComponent(m[1]);` +
	`history.replaceState(null,"",location.pathname);f.submit();}` +
	`else{document.getElementById("none").hidden=false;}`

// trelloScriptHash is trelloScript's hash, as a CSP source.
var trelloScriptHash = func() string {
	sum := sha256.Sum256([]byte(trelloScript))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}()

var trelloCallbackPage = mustPage(`
<h1>Link your Trello account</h1>
<p>Signed in as <strong>{{.Name}}</strong>. Finishing linking your Trello account…</p>
<form id="got" method="post" action="{{.Here}}/token"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="token"></form>
<p id="none" class="error" hidden>Trello did not send a token back here.
<a href="{{.Here}}/start">Link again</a>.</p>
<noscript><p>This page needs its one short script to pass Trello's token on. Without it,
copy the token from the address bar (the part after <code>#token=</code>), and paste it here:</p></noscript>
<form method="post" action="{{.Here}}/token"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p><label>Trello token<br><input name="token" autocomplete="off" spellcheck="false"></label></p>
<p><button type="submit">Link</button></p></form>
<script>` + trelloScript + `</script>
`)
