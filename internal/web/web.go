// Package web serves the service's web site: its public pages (what it is,
// its privacy policy and its terms of service, as Google requires of an app
// that reads its users' calendars), and the pages where a user signs in and
// connects their Google calendar. The site is served over plain HTTP, for a
// web server in front to give it its public HTTPS address, which an admin
// sets; every link on it is to that address.
package web

import (
	"bytes"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// AppName is the name the pages, and the Google consent screen, give the
// service.
const AppName = "exec-3270"

// TerminalPath is where, under the site's address, the web server in front
// serves a browser terminal for this server, linked to once a calendar is
// connected, for going back to choose its calendars.
const TerminalPath = "3270/"

// Updated is when the privacy policy and terms last changed.
const Updated = "October 2, 2026"

// Server serves the site. The settings, the Google client and the users are
// read from Users at each request, so that a change an admin makes shows at
// once.
type Server struct {
	Users *users.Store

	// Audit records sign-ins and sign-outs; nil for none.
	Audit *audit.Log

	// Now gives the time; nil for time.Now. Tests replace it.
	Now func() time.Time

	// TrelloBaseURL is the Trello API's address; "" for Trello's own.
	// Tests replace it.
	TrelloBaseURL string

	mu       sync.Mutex
	sessions map[string]*session    // by token
	failures map[string][]time.Time // failed sign-ins by user name, lower case
}

// page is what a page's template is given.
type page struct {
	Title        string
	AppName      string
	Organization string // who runs the service, or a description of them
	Contact      string // an email address, or "" for none set
	Home         string // the home page's address; the others follow it
	Privacy      string
	Terms        string
	Canonical    string // this page's address
	Updated      string

	// For the sign-in and connect pages.
	Google    string // the Google sign-in page's address
	Trello    string // the Trello one's
	Terminal  string // the browser terminal's address (see TerminalPath)
	Problem   string // why connecting cannot be done here yet
	Message   string // what the last step said
	IsError   bool
	Name      string // who is signed in, "" for no one
	Connected bool   // their Google calendar, or Trello account, is connected
	Account   string // the Trello account linked
	CSRF      string

	// The sign-in and connect pages' own: where they are, and whose
	// account they connect.
	Here, Provider, ProviderHost, Why string
}

// csp is the pages' Content Security Policy: nothing but their own inline
// style, and forms (on the sign-in pages) only to this site.
func csp(forms bool) string {
	action := "'none'"
	if forms {
		action = "'self'"
	}
	return "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action " + action + "; frame-ancestors 'none'"
}

// now is the time.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// site reads the site's settings, logging a failure, which leaves the pages
// with their defaults.
func (s *Server) site() *users.Site {
	site, err := s.Users.Site()
	if err != nil {
		log.Printf("web: reading the site's settings: %v", err)
	}
	return site
}

// render writes tmpl filled from p.
func render(w http.ResponseWriter, r *http.Request, tmpl *template.Template, p page, status int) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		log.Printf("web: %s: %v", r.URL.Path, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes()) //nolint:errcheck
}

// ServeHTTP serves the home page at /, the privacy policy at /privacy and
// the terms at /terms, to GET and HEAD only, and the sign-in and connect
// pages under /google (see serveGoogle).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", csp(false))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
	path := strings.TrimSuffix(r.URL.Path, "/")
	for _, svc := range services {
		if path == "/"+svc.path || strings.HasPrefix(path, "/"+svc.path+"/") {
			header.Set("Content-Security-Policy", csp(true))
			header.Set("Cache-Control", "no-store")
			// Not no-referrer: under it, browsers send a form posted here
			// with Origin: null and no Referer, and sameOrigin refuses
			// it. Under same-origin they send both to this site alone,
			// never to Google or Trello.
			header.Set("Referrer-Policy", "same-origin")
			s.serveConnect(w, r, path, svc)
			return
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var tmpl *template.Template
	var title, canonical string
	switch path {
	case "":
		tmpl, title, canonical = homePage, AppName, ""
	case "/privacy":
		tmpl, title, canonical = privacyPage, "Privacy policy", "privacy"
	case "/terms":
		tmpl, title, canonical = termsPage, "Terms of service", "terms"
	case "/robots.txt":
		header.Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("User-agent: *\nAllow: /\n")) //nolint:errcheck
		return
	default:
		http.NotFound(w, r)
		return
	}

	p := pageFor(s.site(), title)
	p.Canonical = p.Home + canonical
	header.Set("Cache-Control", "public, max-age=300")
	render(w, r, tmpl, p, http.StatusOK)
}

// pageFor fills in a page's details from the site's settings, which may be
// nil: links are then relative, and the organization is described.
func pageFor(site *users.Site, title string) page {
	p := page{Title: title, AppName: AppName, Updated: Updated, Home: "/", Organization: "the organization that runs this service"}
	if site != nil {
		if site.BaseURL != "" {
			p.Home = site.BaseURL
		}
		switch {
		case site.Organization != "":
			p.Organization = site.Organization
		case site.BaseURL != "":
			if u, err := url.Parse(site.BaseURL); err == nil && u.Hostname() != "" {
				p.Organization = "the operators of " + u.Hostname()
			}
		}
		p.Contact = site.Contact
	}
	p.Privacy, p.Terms, p.Google, p.Trello, p.Terminal = p.Home+"privacy", p.Home+"terms", p.Home+"google", p.Home+"trello", p.Home+TerminalPath
	return p
}

// NormalizeBaseURL checks an address typed for the site, returning it ending
// in a slash: an absolute http or https URL with a host, and no query or
// fragment.
func NormalizeBaseURL(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	switch {
	case err != nil:
		return "", err
	case u.Scheme != "https" && u.Scheme != "http":
		return "", errBaseURL("it must start with https://")
	case u.Host == "":
		return "", errBaseURL("it has no host name")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", errBaseURL("it can have no user, query or fragment")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

type errBaseURL string

func (e errBaseURL) Error() string { return "not a usable address: " + string(e) }

// layout is every page's frame.
const layout = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}{{if ne .Title .AppName}} – {{.AppName}}{{end}}</title>
<link rel="canonical" href="{{.Canonical}}">
<style>
:root { color-scheme: light dark; --fg: #1d1d1f; --bg: #fdfdfb; --muted: #5f6368; --rule: #d9d9d4; --link: #0b57d0; }
@media (prefers-color-scheme: dark) { :root { --fg: #e8e6e1; --bg: #17181a; --muted: #a0a3a8; --rule: #33353a; --link: #8ab4f8; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 17px/1.6 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 42rem; margin: 0 auto; padding: 2.5rem 1rem 3rem; }
header { border-bottom: 1px solid var(--rule); margin-bottom: 1.5rem; }
header a { font: 600 1rem ui-monospace, "SF Mono", Menlo, monospace; color: var(--fg); text-decoration: none; }
nav { display: inline; margin-left: 1rem; font-size: .95rem; }
nav a { margin-right: .9rem; }
a { color: var(--link); }
h1 { font-size: 1.8rem; line-height: 1.25; margin: 1rem 0 .3rem; }
h2 { font-size: 1.15rem; margin: 2rem 0 .4rem; }
.meta, footer { color: var(--muted); font-size: .9rem; }
footer { border-top: 1px solid var(--rule); margin-top: 2.5rem; padding-top: 1rem; }
code { font-family: ui-monospace, "SF Mono", Menlo, monospace; font-size: .92em; }
label { font-weight: 600; font-size: .95rem; }
input { font: inherit; font-weight: 400; width: 100%; max-width: 20rem; box-sizing: border-box; padding: .45rem .6rem; margin-top: .25rem; border: 1px solid var(--rule); border-radius: 6px; background: var(--bg); color: var(--fg); }
button, .button { display: inline-block; font: 600 1rem system-ui, -apple-system, "Segoe UI", sans-serif; padding: .55rem 1.1rem; border: 0; border-radius: 6px; background: var(--link); color: var(--bg); text-decoration: none; cursor: pointer; }
button.link { background: none; color: var(--link); padding: 0; font-weight: 400; text-decoration: underline; }
.notice { border: 2px solid #e37400; border-left-width: 8px; border-radius: 6px; background: #fef7e0; color: #3c2a00; padding: .2rem 1rem; margin: 1.2rem 0; }
.notice strong { font-size: 1.1rem; }
.notice code { color: inherit; }
@media (prefers-color-scheme: dark) { .notice { background: #3a2a00; color: #fde293; border-color: #fbbc04; } }
.error { color: #c5221f; font-weight: 600; }
.ok { color: #137333; font-weight: 600; }
@media (prefers-color-scheme: dark) { .error { color: #f28b82; } .ok { color: #81c995; } }
</style>
</head>
<body>
<main>
<header><p><a href="{{.Home}}">{{.AppName}}</a><nav><a href="{{.Privacy}}">Privacy</a><a href="{{.Terms}}">Terms</a></nav></p></header>
{{template "body" .}}
<footer>{{.AppName}} is run by {{.Organization}}.{{if .Contact}} Questions: <a href="mailto:{{.Contact}}">{{.Contact}}</a>.{{end}}</footer>
</main>
</body>
</html>
`

// contact is how to reach the operators, for the pages' text.
const contact = `{{define "contact"}}{{if .Contact}}<a href="mailto:{{.Contact}}">{{.Contact}}</a>{{else}}the administrators who gave you your account{{end}}{{end}}`

func mustPage(body string) *template.Template {
	funcs := template.FuncMap{
		"sessionMinutes": func() int { return int(sessionLife / time.Minute) },
		"maxName":        func() int { return users.MaxNameLength },
	}
	return template.Must(template.Must(template.New("layout").Funcs(funcs).Parse(layout + contact)).Parse(`{{define "body"}}` + body + `{{end}}`))
}

var homePage = mustPage(`
<h1>{{.AppName}}</h1>
<p>{{.AppName}} helps the people who use it keep track of what they have to
do: their tasks, their appointments and their checklists, together on one
screen that can stay up all day. It is meant for anyone who wants help with
executive function: remembering what comes next, seeing how long until it
starts, getting started on it, and finishing what was begun.</p>
<p>{{.AppName}} is a dashboard created by {{.Organization}}, used from a
TN3270 (mainframe-style) terminal. Each user signs in with an account their
administrators give them, and sees on one screen:</p>
<ul>
<li>their upcoming meetings, from the Google calendars they choose to connect, read only;</li>
<li>their open tasks, from the Trello lists they choose to link, and their own checklists;</li>
<li>whether the organization's busy indicator shows that someone is in a meeting.</li>
</ul>
<p>It also has a calendar of the month, a calculator, and a chat shared by
everyone signed in.</p>
<h2>Google Calendar</h2>
<p>Connecting a Google calendar is optional. A user who connects one gives
{{.AppName}} read-only access to their calendars, so that it can list them for
the user to choose from, and show the events of those chosen. {{.AppName}}
never changes a calendar, and keeps no copy of any event. The user can
disconnect at any time, which deletes the authorization and asks Google to
withdraw it.</p>
<p>Users connect their calendar on <a href="{{.Google}}">this site's sign-in
page</a>, with the user name and password they use on the terminal.</p>
<h2>Trello</h2>
<p>Linking a Trello account is optional. A user who links one gives
{{.AppName}} access to their Trello boards, so that it can list their boards'
lists for the user to choose from, show the cards on the lists chosen as
their tasks, and add a card, move one to another of their lists, or archive
one, when the user adds, moves or finishes a task on the terminal. It changes nothing else, and keeps no copy of any card.
The user can unlink at any time, which deletes the token and asks Trello to
withdraw it. Users link their account on <a href="{{.Trello}}">this site's
Trello page</a>.</p>
<p>How {{.AppName}} handles personal information is set out in its
<a href="{{.Privacy}}">privacy policy</a>; using it is subject to its
<a href="{{.Terms}}">terms of service</a>.</p>
<p>The service is managed by {{.Organization}}; there is no public sign-up.
For an account, ask {{template "contact" .}}.</p>
`)

var privacyPage = mustPage(`
<h1>Privacy policy</h1>
<p class="meta">Last updated {{.Updated}}</p>
<p>This policy says what information {{.AppName}} keeps about the people who
use it, why, who can see it, and how long it is kept. {{.AppName}} is run by
{{.Organization}}, on servers {{.Organization}} operates.</p>

<h2>What is kept</h2>
<ul>
<li><strong>Your account.</strong> Your user name, a one-way hash of your
password (made with Argon2id; the password itself is never stored), and
whether you are an administrator.</li>
<li><strong>Your checklists.</strong> Their names, items and which items are
checked off.</li>
<li><strong>Your Google Calendar connection, if you make one.</strong> The
authorization Google issues (a refresh token), the calendars you chose, their
names, and the short names you gave them. See below.</li>
<li><strong>Your Trello link, if you make one.</strong> The token Trello
issues, your Trello user name, the lists you chose, their boards' and their
names, and the tags you gave them. See below.</li>
<li><strong>Chat messages.</strong> Each message, with your user name and the
time. Messages are kept in memory only: the newest thousand, until an
administrator clears them or the server restarts.</li>
<li><strong>Records of use.</strong> When you sign in and out, failed sign-in
attempts (with the name tried), and when a session ends and why, each with the
terminal's name (or, on this web site, that it was here), the IP address it
came from and the time. While you are connected,
administrators can also see which screen you are on and how long since you
last pressed a key.</li>
</ul>
<p>The busy indicator shown comes from {{.Organization}}'s own systems, not
from you.</p>

<h2>Google user data</h2>
<p>If you connect a Google calendar, {{.AppName}} asks Google only for
read-only access to your calendars (the <code>calendar.readonly</code> scope).
It uses that access only to list your calendars, so that you can choose which
to show, and to read the events of those you chose, to show them to you on
your dashboard and calendar screen. Events are held in memory while you are
using the service, and refreshed every few minutes; they are never written to
disk, never shown to other users, and never used for anything else.</p>
<p>{{.AppName}}'s use and transfer to any other app of information received
from Google APIs will adhere to the
<a href="https://developers.google.com/terms/api-services-user-data-policy">Google API Services User Data Policy</a>,
including the Limited Use requirements. Google user data is not sold, not used
for advertising, not used to determine credit-worthiness, not used to train
artificial intelligence models, and not transferred to anyone else, except as
needed to provide the features described here, to comply with the law, or as
part of a merger or acquisition with your consent.</p>
<p>You can disconnect your calendar at any time on the <code>settings</code>
screen on the dashboard: your authorization and choices are deleted, and
Google is asked to withdraw the authorization. You can also withdraw it
yourself on your Google Account's
<a href="https://myaccount.google.com/permissions">third-party access page</a>.</p>

<h2>Trello data</h2>
<p>If you link a Trello account, {{.AppName}} asks Trello for a token to read
and write your boards, which does not expire until you unlink or withdraw it.
It uses the token only to list your boards and their lists, so that you can
choose which to show; to read the cards on the lists you chose, to show them
to you as your tasks; and, when you add, move or finish a task on the
terminal, to add that card, to move it to the list you pick (on any of your
boards), or to mark it done and archive it. Cards are held in memory
while you are using the service, refreshed every quarter of an hour, never
written to disk, never shown to other users, and never used for anything
else.</p>
<p>You can unlink your Trello account at any time on the
<code>settings</code> screen on the dashboard: your token and choices are
deleted, and Trello is asked to withdraw the token. You can also withdraw it
yourself in your Trello account's settings, under applications.</p>

<h2>Who can see it</h2>
<p>Your checklists, calendar and tasks are shown only to you. Chat messages are seen
by everyone signed in. {{.Organization}}'s administrators of this service can
see the accounts, the records of use and the sessions connected, and those who
run the server can read the files it keeps. Nothing is shared with anyone else,
except Google and Trello, to read the calendars and lists you connect (and,
on Trello, to add, move and archive the cards you ask to), and where the law
requires it.</p>

<h2>How it is kept</h2>
<p>Accounts, Google authorizations, Trello tokens and checklists are kept in
files on the server; the file with accounts, authorizations and tokens is
readable only by the account the service runs as. The terminal connection itself is not encrypted
by {{.AppName}}: it should be reached only via encrypted connections.
These web pages run no scripts, but for one line on the page Trello returns
you to, which passes Trello's token on to the server. Signing in on them, to
connect your calendar or link Trello, sets one cookie, which keeps you signed in on the sign-in pages for
{{sessionMinutes}} minutes, or until you sign out; no other page sets one, and
it is not used for anything else. The web server in front of them may keep
ordinary access logs.</p>

<h2>How long it is kept</h2>
<ul>
<li>Your account, checklists, Google connection and Trello link: until your
account is deleted. Deleting it deletes your checklists, your Google
connection and your Trello link, and asks Google and Trello to withdraw the
authorization and token.</li>
<li>Chat messages: until cleared, or the server restarts.</li>
<li>Records of use: as long as {{.Organization}} keeps the server's logs.</li>
</ul>

<h2>Your choices</h2>
<p>You can change or delete your checklists, connect or disconnect your
Google calendar, and link or unlink your Trello account, at any time. To see what is kept about you, correct it, or
have your account deleted, ask {{template "contact" .}}.</p>

<h2>Children</h2>
<p>{{.AppName}} is for {{.Organization}} and others it gives accounts
to; it is not meant for children.</p>

<h2>Changes</h2>
<p>If this policy changes, the new one will be posted here with a new date.
A change in how Google user data is used will be made only with your consent.</p>

<h2>Contact</h2>
<p>Questions about this policy go to {{template "contact" .}}.</p>
`)

var termsPage = mustPage(`
<h1>Terms of service</h1>
<p class="meta">Last updated {{.Updated}}</p>
<p>{{.AppName}} is a service {{.Organization}} provides to the people it gives
accounts to. By using it you agree to these terms, and to {{.Organization}}'s
own policies, which come first where they differ.</p>

<h2>Accounts</h2>
<p>Accounts are made by {{.Organization}}'s administrators; there is no public
sign-up. Keep your password to yourself, and tell an administrator if you
think someone else has used it. You are responsible for what is done with your
account. Administrators can change, restrict or delete accounts.</p>

<h2>Acceptable use</h2>
<p>Use {{.AppName}} only for the purposes {{.Organization}} allows. Do not:</p>
<ul>
<li>try to reach accounts, data or parts of the service that are not yours;</li>
<li>interfere with the service, or overload it;</li>
<li>post in the chat anything unlawful, harassing, or that
{{.Organization}}'s policies forbid;</li>
<li>use it in breach of the law.</li>
</ul>

<h2>Monitoring</h2>
<p>Administrators can see who is connected, from where, and on which screen,
can read the records of sign-ins and sessions, and can end any session. The
chat is seen by everyone signed in. The <a href="{{.Privacy}}">privacy policy</a>
says what is kept and for how long.</p>

<h2>Google Calendar</h2>
<p>Connecting a Google calendar is optional. If you do, {{.AppName}} reads your
calendars, read only, as the privacy policy describes, and your use of Google's
services remains subject to Google's own terms. You can disconnect at any
time.</p>

<h2>Trello</h2>
<p>Linking a Trello account is optional. If you do, {{.AppName}} reads the
Trello lists you choose, and adds, moves and archives cards when you ask it
to, as the privacy policy describes; your use of Trello remains subject to
Trello's own terms. You can unlink at any time.</p>

<h2>Your content</h2>
<p>Your checklists and chat messages are yours to keep up to date; you are
responsible for them. Administrators may remove content that breaks these
terms or {{.Organization}}'s policies.</p>

<h2>The service</h2>
<p>{{.AppName}} is provided as it is, as a convenience. {{.Organization}} may
change it, suspend it or end it at any time, and does not promise that it
will be available, uninterrupted or free of errors, or that what it shows
(meetings, tasks, the busy indicator) is complete or up to date. To the extent
the law allows, {{.Organization}} is not liable for any loss arising from
using it, or from being unable to.</p>

<h2>Ending</h2>
<p>Your access ends when your account is deleted, or when you no longer have
{{.Organization}}'s permission to use the service. You may stop using it at any
time.</p>

<h2>Changes</h2>
<p>These terms may change; the new terms will be posted here with a new date,
and using the service afterwards means accepting them.</p>

<h2>Contact</h2>
<p>Questions about these terms go to {{template "contact" .}}.</p>
`)
