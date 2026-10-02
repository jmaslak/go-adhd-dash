// Package google connects a user's Google calendar: the OAuth flow that
// gets a refresh token for the server's shared client, and the Calendar API
// calls that list the calendars a user can choose from.
//
// The flow is for a terminal with no browser of its own. The server listens
// on a loopback port, both as the OAuth redirect and as a short link that
// sends a browser on the same machine on to Google, so that there the flow
// completes by itself. A browser elsewhere ends on a page that cannot load,
// whose address, holding the authorization code, is pasted back instead.
package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jmaslak/go-busy-indicator/gauth"
)

// Scope is the access asked for: reading calendars, and nothing more.
const Scope = "https://www.googleapis.com/auth/calendar.readonly"

// Google's endpoints; tests point them elsewhere.
var (
	AuthEndpoint   = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenEndpoint  = gauth.DefaultTokenURI
	RevokeEndpoint = "https://oauth2.googleapis.com/revoke"
	APIBase        = "https://www.googleapis.com/calendar/v3"
)

// FlowTimeout is how long a flow's loopback listener waits for the browser.
const FlowTimeout = 15 * time.Minute

// Client is the OAuth client every user's authorization is issued to.
type Client struct {
	ID, Secret string
}

// Flow is one authorization under way: a loopback listener for the
// redirect, and what it takes to turn the code it brings into a token.
type Flow struct {
	client      Client
	redirectURI string
	state       string
	verifier    string

	// AuthURL is Google's page for approving access; ShortURL is the
	// listener's own, which sends a browser on this machine there.
	AuthURL, ShortURL string

	server *http.Server
	timer  *time.Timer

	mu   sync.Mutex
	code string
}

// Start begins an authorization for client, listening on a loopback port
// until the flow is closed or FlowTimeout passes.
func Start(client Client) (*Flow, error) {
	if client.ID == "" || client.Secret == "" {
		return nil, errors.New("no Google client is set up")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for Google's redirect: %w", err)
	}
	f := &Flow{client: client, redirectURI: "http://" + ln.Addr().String() + "/"}
	f.ShortURL = f.redirectURI
	if f.state, err = randomString(12); err == nil {
		f.verifier, err = randomString(32)
	}
	if err != nil {
		ln.Close() //nolint:errcheck
		return nil, err
	}
	challenge := sha256.Sum256([]byte(f.verifier))
	f.AuthURL = AuthEndpoint + "?" + url.Values{
		"client_id":             {client.ID},
		"redirect_uri":          {f.redirectURI},
		"response_type":         {"code"},
		"scope":                 {Scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {f.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()

	f.server = &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 10 * time.Second}
	go f.server.Serve(ln) //nolint:errcheck // ends when closed
	f.timer = time.AfterFunc(FlowTimeout, f.Close)
	return f, nil
}

// serve sends a browser with no code on to Google, and takes the code from
// the redirect that comes back, if it carries this flow's state.
func (f *Flow) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Get("error") != "":
		http.Error(w, "Google did not grant access: "+q.Get("error")+". Return to the terminal.", http.StatusBadRequest)
	case q.Get("code") == "":
		http.Redirect(w, r, f.AuthURL, http.StatusFound)
	case q.Get("state") != f.state:
		http.Error(w, "This link is not from the authorization under way.", http.StatusBadRequest)
	default:
		f.mu.Lock()
		f.code = q.Get("code")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, donePage) //nolint:errcheck
	}
}

// donePage is shown in the browser once the code has been received.
const donePage = `<!doctype html><meta charset="utf-8"><title>Connected</title>` +
	`<p style="font:16px system-ui;margin:3em">Connected. Return to the terminal and press Enter.</p>`

// Code is the authorization code the listener received, if it has.
func (f *Flow) Code() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.code, f.code != ""
}

// Close stops the listener. It may be called more than once.
func (f *Flow) Close() {
	if f == nil {
		return
	}
	f.timer.Stop()
	f.server.Close() //nolint:errcheck
}

// ParsePasted takes the authorization code from what was pasted: the address
// the browser was redirected to, which must carry this flow's state, or the
// code alone.
func (f *Flow) ParsePasted(text string) (string, error) {
	text = strings.Join(strings.Fields(text), "") // a copy off the screen may break it into lines
	if text == "" {
		return "", errors.New("nothing was pasted")
	}
	if !strings.Contains(text, "?") && !strings.Contains(text, "=") {
		return text, nil
	}
	_, query, _ := strings.Cut(text, "?")
	q, err := url.ParseQuery(query)
	switch {
	case err != nil:
		return "", fmt.Errorf("that address cannot be read: %w", err)
	case q.Get("error") != "":
		return "", fmt.Errorf("access was not granted: %s", q.Get("error"))
	case q.Get("code") == "":
		return "", errors.New("that address has no code in it; paste the address the browser ended on")
	case q.Get("state") != f.state:
		return "", errors.New("that address is not from this authorization; open the link shown again")
	}
	return q.Get("code"), nil
}

// Exchange trades code for a refresh token.
func (f *Flow) Exchange(ctx context.Context, code string) (string, error) {
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {f.verifier},
		"redirect_uri":  {f.redirectURI},
		"client_id":     {f.client.ID},
		"client_secret": {f.client.Secret},
	}
	if err := postForm(ctx, TokenEndpoint, form, &resp); err != nil {
		return "", err
	}
	if resp.RefreshToken == "" {
		return "", errors.New("no refresh token came back from Google; remove this app's access at myaccount.google.com/permissions and try again")
	}
	return resp.RefreshToken, nil
}

// Tokens is a source of access tokens for a user's refresh token.
func Tokens(client Client, refreshToken string) *gauth.TokenSource {
	return gauth.FromCredentials(gauth.Credentials{
		ClientID: client.ID, ClientSecret: client.Secret, RefreshToken: refreshToken,
		TokenURI: TokenEndpoint, Source: "your Google authorization",
	})
}

// TokenSource gives access tokens.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Calendar is one of the calendars a user can see.
type Calendar struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`

	// SummaryOverride is the name the user gave it, if they did.
	SummaryOverride string `json:"summaryOverride"`

	Primary bool `json:"primary"`
}

// Name is what the user calls the calendar.
func (c Calendar) Name() string {
	if c.SummaryOverride != "" {
		return c.SummaryOverride
	}
	return c.Summary
}

// ListCalendars returns every calendar on the user's calendar list.
func ListCalendars(ctx context.Context, tokens TokenSource) ([]Calendar, error) {
	var all []Calendar
	page := ""
	for {
		q := url.Values{"maxResults": {"250"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		var resp struct {
			Items         []Calendar `json:"items"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := get(ctx, tokens, APIBase+"/users/me/calendarList?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if page = resp.NextPageToken; page == "" {
			return all, nil
		}
	}
}

// LookupCalendar reads the calendar with id, which need not be on the user's
// calendar list, to check that it can be read and to learn its name.
func LookupCalendar(ctx context.Context, tokens TokenSource, id string) (Calendar, error) {
	var c Calendar
	err := get(ctx, tokens, APIBase+"/calendars/"+url.PathEscape(id), &c)
	return c, err
}

// Revoke withdraws a refresh token at Google.
func Revoke(ctx context.Context, refreshToken string) error {
	return postForm(ctx, RevokeEndpoint, url.Values{"token": {refreshToken}}, nil)
}

// get reads JSON from the Calendar API into out.
func get(ctx context.Context, tokens TokenSource, target string, out any) error {
	token, err := tokens.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return do(req, out)
}

// postForm posts form and reads the JSON reply into out, if out is not nil.
func postForm(ctx context.Context, target string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(req, out)
}

// do sends req, reporting a reply other than 200 with Google's own message
// for it when it gives one.
func do(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("from Google: %s%s", resp.Status, errorDetail(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// errorDetail is Google's explanation in an error reply, if it has one, as
// ": explanation". The token endpoint gives it as error_description, or a
// bare error code; the Calendar API as the error's message.
func errorDetail(data []byte) string {
	var e struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	if e.Description != "" {
		return ": " + e.Description
	}
	var code string
	if json.Unmarshal(e.Error, &code) == nil && code != "" {
		return ": " + code
	}
	var api struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Error, &api) == nil && api.Message != "" {
		return ": " + api.Message
	}
	return ""
}

// randomString is n random bytes, base64url encoded.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("making a random string: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
