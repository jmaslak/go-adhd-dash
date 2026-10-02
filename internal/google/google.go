// Package google connects a user's Google calendar: the OAuth authorization
// that gets a refresh token for the server's shared client, and the Calendar
// API calls that list the calendars a user can choose from.
//
// The authorization is a web one: the user's browser is sent to Google from
// the server's web site, and comes back to it, at a redirect address
// registered with a Web application client.
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
	"net/http"
	"net/url"
	"strings"

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

// Client is the OAuth client every user's authorization is issued to.
type Client struct {
	ID, Secret string
}

// Authorization is one authorization begun: where to send the browser, and
// what checks the code it comes back with and trades it for a token.
type Authorization struct {
	URL         string // Google's page for approving access
	State       string // comes back with the code; it must match
	Verifier    string // proves the trade is by whoever began it (PKCE)
	RedirectURI string
}

// Begin starts an authorization for client, Google to send the browser back
// to redirectURI, registered with the client.
func Begin(client Client, redirectURI string) (Authorization, error) {
	if client.ID == "" || client.Secret == "" {
		return Authorization{}, errors.New("no Google client is set up")
	}
	a := Authorization{RedirectURI: redirectURI}
	var err error
	if a.State, err = randomString(24); err == nil {
		a.Verifier, err = randomString(32)
	}
	if err != nil {
		return Authorization{}, err
	}
	challenge := sha256.Sum256([]byte(a.Verifier))
	a.URL = AuthEndpoint + "?" + url.Values{
		"client_id":             {client.ID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {Scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {a.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}.Encode()
	return a, nil
}

// Exchange trades the code the browser came back with for a refresh token.
func (a Authorization) Exchange(ctx context.Context, client Client, code string) (string, error) {
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {a.Verifier},
		"redirect_uri":  {a.RedirectURI},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
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
