package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBegin(t *testing.T) {
	if _, err := Begin(Client{}, "https://x/"); err == nil {
		t.Errorf("began with no client")
	}
	a, err := Begin(Client{ID: "id.apps.googleusercontent.com", Secret: "s"}, "https://adhd.example.com/google/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := auth.Query()
	for k, want := range map[string]string{
		"client_id": "id.apps.googleusercontent.com", "redirect_uri": "https://adhd.example.com/google/callback", "scope": Scope,
		"access_type": "offline", "code_challenge_method": "S256", "response_type": "code", "state": a.State,
	} {
		if q.Get(k) != want {
			t.Errorf("auth URL %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if b, _ := Begin(Client{ID: "i", Secret: "s"}, "https://x/"); b.State == a.State || b.Verifier == a.Verifier {
		t.Errorf("two authorizations share a state or verifier")
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
	client := Client{ID: "cid", Secret: "csecret"}
	a, err := Begin(client, "https://adhd.example.com/google/callback")
	if err != nil {
		t.Fatal(err)
	}
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

	token, err := a.Exchange(context.Background(), client, "4/abc")
	if err != nil || token != "rt" {
		t.Fatalf("exchange: %q, %v", token, err)
	}
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "4/abc", "code_verifier": a.Verifier,
		"redirect_uri": a.RedirectURI, "client_id": "cid", "client_secret": "csecret",
	} {
		if got.Get(k) != want {
			t.Errorf("posted %s = %q, want %q", k, got.Get(k), want)
		}
	}
	if _, err := a.Exchange(context.Background(), client, "bad"); err == nil || !strings.Contains(err.Error(), "Bad Request") {
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

func TestEvents(t *testing.T) {
	// Each event's attendees, by its title, and whether it is to be marked
	// as declined by every other person; "" for one left out.
	page1 := `{"items": [
		{"summary": "alone", "start": {"dateTime": "2026-09-27T09:00:00Z"}, "end": {"dateTime": "2026-09-27T10:00:00Z"}},
		{"summary": "all declined", "start": {"dateTime": "2026-09-27T10:00:00Z"}, "end": {"dateTime": "2026-09-27T11:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "accepted"}, {"responseStatus": "declined"}, {"responseStatus": "declined"}]},
		{"summary": "one coming", "start": {"dateTime": "2026-09-27T11:00:00Z"}, "end": {"dateTime": "2026-09-27T12:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "accepted"}, {"responseStatus": "declined"}, {"responseStatus": "needsAction"}]},
		{"summary": "room only", "start": {"dateTime": "2026-09-27T12:00:00Z"}, "end": {"dateTime": "2026-09-27T13:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "accepted"}, {"resource": true, "responseStatus": "declined"}]},
		{"summary": "declined but the room", "start": {"dateTime": "2026-09-27T13:00:00Z"}, "end": {"dateTime": "2026-09-27T14:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "accepted"}, {"responseStatus": "declined"}, {"resource": true, "responseStatus": "accepted"}]}
	], "nextPageToken": "p2"}`
	page2 := `{"items": [
		{"summary": "I declined", "start": {"dateTime": "2026-09-27T14:00:00Z"}, "end": {"dateTime": "2026-09-27T15:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "declined"}, {"responseStatus": "declined"}]},
		{"summary": "cancelled", "status": "cancelled", "start": {"dateTime": "2026-09-27T15:00:00Z"}, "end": {"dateTime": "2026-09-27T16:00:00Z"}},
		{"summary": "too many to list", "attendeesOmitted": true, "start": {"dateTime": "2026-09-27T16:00:00Z"}, "end": {"dateTime": "2026-09-27T17:00:00Z"},
		 "attendees": [{"self": true, "responseStatus": "accepted"}]},
		{"summary": "holiday", "start": {"date": "2026-09-28"}, "end": {"date": "2026-09-29"},
		 "attendees": [{"responseStatus": "declined"}]}
	]}`
	var queries []url.Values
	fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token": "at", "expires_in": 3600}`)) //nolint:errcheck
		case r.URL.Path != "/api/calendars/c_1@group.calendar.google.com/events" || r.Header.Get("Authorization") != "Bearer at":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Query().Get("pageToken") == "":
			queries = append(queries, r.URL.Query())
			w.Write([]byte(page1)) //nolint:errcheck
		default:
			queries = append(queries, r.URL.Query())
			w.Write([]byte(page2)) //nolint:errcheck
		}
	})
	loc := time.FixedZone("MDT", -6*3600)
	from := time.Date(2026, 9, 27, 0, 0, 0, 0, loc)
	events, err := Events(context.Background(), "", Tokens(Client{ID: "cid", Secret: "cs"}, "rt"), "c_1@group.calendar.google.com", from, from.AddDate(0, 0, 2), loc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"alone": false, "all declined": true, "one coming": false, "room only": false,
		"declined but the room": true, "too many to list": false, "holiday": true,
	}
	if len(events) != len(want) {
		t.Errorf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for _, e := range events {
		if marked, ok := want[e.Summary]; !ok {
			t.Errorf("%q not left out", e.Summary)
		} else if e.OthersDeclined != marked {
			t.Errorf("%q: OthersDeclined %v, want %v", e.Summary, e.OthersDeclined, marked)
		}
		if e.Start.Location() != loc {
			t.Errorf("%q starts in %v, want %v", e.Summary, e.Start.Location(), loc)
		}
	}
	if last := events[len(events)-1]; !last.AllDay || !last.Start.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, loc)) {
		t.Errorf("all-day event: %+v", last)
	}
	if len(queries) != 2 || queries[1].Get("pageToken") != "p2" {
		t.Fatalf("queries %v; want two pages", queries)
	}
	for k, want := range map[string]string{"singleEvents": "true", "orderBy": "startTime", "timeMin": "2026-09-27T00:00:00-06:00"} {
		if queries[0].Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, queries[0].Get(k), want)
		}
	}
	if f := queries[0].Get("fields"); !strings.Contains(f, "attendees(self,resource,responseStatus)") || !strings.Contains(f, "attendeesOmitted") {
		t.Errorf("fields %q do not ask for the attendees", f)
	}
}
