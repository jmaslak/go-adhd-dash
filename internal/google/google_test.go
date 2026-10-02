package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// noRedirect is an HTTP client that reports redirects rather than following
// them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestFlowListener(t *testing.T) {
	f, err := Start(Client{ID: "id.apps.googleusercontent.com", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	auth, err := url.Parse(f.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	q := auth.Query()
	for k, want := range map[string]string{
		"client_id": "id.apps.googleusercontent.com", "redirect_uri": f.ShortURL, "scope": Scope,
		"access_type": "offline", "code_challenge_method": "S256", "response_type": "code",
	} {
		if q.Get(k) != want {
			t.Errorf("auth URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if !strings.HasPrefix(f.ShortURL, "http://127.0.0.1:") {
		t.Errorf("short URL %q is not on the loopback interface", f.ShortURL)
	}

	// The short link sends the browser on to Google.
	resp, err := noRedirect.Get(f.ShortURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != f.AuthURL {
		t.Errorf("short link: %s to %q", resp.Status, resp.Header.Get("Location"))
	}

	// A redirect with another state is refused.
	resp, err = http.Get(f.ShortURL + "?state=wrong&code=nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if _, ok := f.Code(); ok || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong state: %s, code taken %v", resp.Status, ok)
	}

	// Google's redirect brings the code.
	resp, err = http.Get(f.ShortURL + "?" + url.Values{"state": {q.Get("state")}, "code": {"4/abc"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if code, ok := f.Code(); !ok || code != "4/abc" || resp.StatusCode != http.StatusOK {
		t.Errorf("redirect: %s, code %q %v", resp.Status, code, ok)
	}

	// Closed, it stops listening.
	f.Close()
	if _, err := http.Get(f.ShortURL); err == nil {
		t.Errorf("still listening after Close")
	}
	f.Close() // again, harmlessly
}

func TestParsePasted(t *testing.T) {
	f := &Flow{state: "st8"}
	for _, c := range []struct{ pasted, code, err string }{
		{"4/0Abc-def", "4/0Abc-def", ""},
		{"  4/0Abc-\n def ", "4/0Abc-def", ""},
		{"http://127.0.0.1:5555/?state=st8&code=4%2F0Abc&scope=x", "4/0Abc", ""},
		// Copied off the screen in two lines.
		{"http://127.0.0.1:5555/?state=st8&co\nde=4%2F0Abc", "4/0Abc", ""},
		{"http://127.0.0.1:5555/?state=other&code=4%2F0Abc", "", "not from this authorization"},
		{"http://127.0.0.1:5555/?error=access_denied&state=st8", "", "access_denied"},
		{"http://127.0.0.1:5555/?state=st8", "", "no code"},
		{"", "", "nothing was pasted"},
	} {
		code, err := f.ParsePasted(c.pasted)
		switch {
		case c.err == "" && (err != nil || code != c.code):
			t.Errorf("%q: %q, %v; want %q", c.pasted, code, err, c.code)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%q: %q, %v; want an error with %q", c.pasted, code, err, c.err)
		}
	}
}

// fakeGoogle serves the token endpoint and the Calendar API calls, and
// points the package at it for the test.
func fakeGoogle(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldToken, oldAPI, oldRevoke := TokenEndpoint, APIBase, RevokeEndpoint
	TokenEndpoint, APIBase, RevokeEndpoint = srv.URL+"/token", srv.URL+"/api", srv.URL+"/revoke"
	t.Cleanup(func() { TokenEndpoint, APIBase, RevokeEndpoint = oldToken, oldAPI, oldRevoke })
}

func TestExchange(t *testing.T) {
	f, err := Start(Client{ID: "cid", Secret: "csecret"})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got url.Values
	fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		got = r.PostForm
		if r.PostForm.Get("code") == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": "invalid_grant", "error_description": "Bad Request"}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"access_token": "at", "refresh_token": "rt", "expires_in": 3600}`)) //nolint:errcheck
	})

	token, err := f.Exchange(context.Background(), "4/abc")
	if err != nil || token != "rt" {
		t.Fatalf("exchange: %q, %v", token, err)
	}
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "4/abc", "code_verifier": f.verifier,
		"redirect_uri": f.redirectURI, "client_id": "cid", "client_secret": "csecret",
	} {
		if got.Get(k) != want {
			t.Errorf("posted %s = %q, want %q", k, got.Get(k), want)
		}
	}
	if _, err := f.Exchange(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "Bad Request") {
		t.Errorf("bad code: %v; want Google's explanation", err)
	}
}

func TestCalendars(t *testing.T) {
	fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token": "at", "expires_in": 3600}`)) //nolint:errcheck
		case r.Header.Get("Authorization") != "Bearer at":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/api/users/me/calendarList" && r.URL.Query().Get("pageToken") == "":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"items":         []Calendar{{ID: "me@example.com", Summary: "me@example.com", Primary: true}},
				"nextPageToken": "p2",
			})
		case r.URL.Path == "/api/users/me/calendarList":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"items": []Calendar{{ID: "c_1@group.calendar.google.com", Summary: "Team", SummaryOverride: "My team"}},
			})
		case r.URL.Path == "/api/calendars/en.usa#holiday@group.v.calendar.google.com":
			w.Write([]byte(`{"id": "en.usa#holiday@group.v.calendar.google.com", "summary": "Holidays"}`)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": {"code": 404, "message": "Not Found"}}`)) //nolint:errcheck
		}
	})
	tokens := Tokens(Client{ID: "cid", Secret: "cs"}, "rt")

	cals, err := ListCalendars(context.Background(), tokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 2 || !cals[0].Primary || cals[1].Name() != "My team" {
		t.Errorf("calendars %+v", cals)
	}
	c, err := LookupCalendar(context.Background(), tokens, "en.usa#holiday@group.v.calendar.google.com")
	if err != nil || c.Name() != "Holidays" {
		t.Errorf("lookup: %+v, %v", c, err)
	}
	if _, err := LookupCalendar(context.Background(), tokens, "nope"); err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("lookup of a calendar that is not there: %v", err)
	}
}
