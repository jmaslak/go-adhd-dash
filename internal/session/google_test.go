package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/google"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// fakeGoogle stands in for Google's token endpoint and Calendar API, and
// records what was revoked.
type fakeGoogle struct {
	mu      sync.Mutex
	revoked []string
	tokens  int // refresh tokens issued
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{}
	long := "A calendar with a name far too long to fit on one row of the list, so it takes two"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/token" && r.PostForm.Get("grant_type") == "authorization_code":
			if r.PostForm.Get("code") != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error": "invalid_grant", "error_description": "Bad code"}`)) //nolint:errcheck
				return
			}
			f.tokens++
			w.Write([]byte(`{"access_token": "at", "refresh_token": "rt` + string(rune('0'+f.tokens)) + `", "expires_in": 3600}`)) //nolint:errcheck
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token": "at", "expires_in": 3600}`)) //nolint:errcheck
		case r.URL.Path == "/revoke":
			f.revoked = append(f.revoked, r.PostForm.Get("token"))
		case r.URL.Path == "/api/users/me/calendarList":
			json.NewEncoder(w).Encode(map[string]any{"items": []google.Calendar{ //nolint:errcheck
				{ID: "me@example.com", Summary: "me@example.com", Primary: true},
				{ID: "c_team@group.calendar.google.com", Summary: "Team"},
				{ID: "c_long@group.calendar.google.com", Summary: long},
			}})
		case r.URL.Path == "/api/calendars/en.usa#holiday@group.v.calendar.google.com":
			w.Write([]byte(`{"id": "en.usa#holiday@group.v.calendar.google.com", "summary": "Holidays"}`)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": {"code": 404, "message": "Not Found"}}`)) //nolint:errcheck
		}
	}))
	t.Cleanup(srv.Close)
	oldToken, oldAPI, oldRevoke := google.TokenEndpoint, google.APIBase, google.RevokeEndpoint
	google.TokenEndpoint, google.APIBase, google.RevokeEndpoint = srv.URL+"/token", srv.URL+"/api", srv.URL+"/revoke"
	t.Cleanup(func() { google.TokenEndpoint, google.APIBase, google.RevokeEndpoint = oldToken, oldAPI, oldRevoke })
	return f
}

// googleRig drives the Google screens as a session does.
type googleRig struct {
	t      *testing.T
	store  *users.Store
	g      googleState
	screen go3270.Screen
	rows   []string
}

func (r *googleRig) draw() {
	r.t.Helper()
	r.screen, _, _ = r.g.build(24, 80, now)
	r.rows = screenText(r.t, r.screen, 24, 80)
}

// key presses aid with every input field as drawn but those in typed, and
// redraws, returning whether the screens were left and what to say then.
func (r *googleRig) key(aid go3270.AID, typed map[string]string) (bool, string) {
	r.t.Helper()
	values := map[string]string{}
	for _, f := range r.screen {
		if f.Write {
			values[f.Name] = f.Content
		}
	}
	for k, v := range typed {
		values[k] = v
	}
	leave, said := r.g.handle(go3270.Response{AID: aid, Values: values}, r.store, func(string, ...any) {})
	if !leave {
		r.draw()
	}
	return leave, said
}

func (r *googleRig) text() string { return strings.Join(r.rows, "\n") }

// link is the user's Google link as saved.
func (r *googleRig) link() *users.GoogleLink {
	r.t.Helper()
	list, _, err := r.store.GoogleClient()
	if err != nil {
		r.t.Fatal(err)
	}
	return list[0].Google
}

// field finds the field called name on the screen.
func (r *googleRig) field(name string) go3270.Field {
	r.t.Helper()
	for _, f := range r.screen {
		if f.Name == name {
			return f
		}
	}
	r.t.Fatalf("no field %q on the screen:\n%s", name, r.text())
	return go3270.Field{}
}

// calIndex is the index of the calendar with id.
func (r *googleRig) calIndex(id string) string {
	for i, c := range r.g.calendars {
		if c.ID == id {
			return string(rune('0' + i))
		}
	}
	r.t.Fatalf("no calendar %q", id)
	return ""
}

func TestGoogleConnectAndChoose(t *testing.T) {
	fake := newFakeGoogle(t)
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	if _, why := startGoogle(store, 1); !strings.Contains(why, "No Google client") {
		t.Errorf("with no client: %q", why)
	}
	if _, why := startGoogle(nil, 0); !strings.Contains(why, "no user database") {
		t.Errorf("with no user database: %q", why)
	}
	if err := store.SetGoogleClient(&users.GoogleClient{ClientID: "cid", ClientSecret: "cs"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Users: store, Agendas: agenda.NewPool(context.Background(), time.Hour)}
	u := &users.User{ID: 1}
	if cfg.agendaFor(u) != nil {
		t.Errorf("not connected, yet an agenda")
	}

	g, why := startGoogle(store, 1)
	if why != "" || g.step != googleConnect {
		t.Fatalf("start: %q, step %d", why, g.step)
	}
	r := &googleRig{t: t, store: store, g: g}
	defer r.g.close()
	r.draw()
	for _, want := range []string{r.g.flow.ShortURL, "From a browser anywhere else", "PF5=New link"} {
		if !strings.Contains(r.text(), want) {
			t.Errorf("connect screen lacks %q:\n%s", want, r.text())
		}
	}
	if !strings.Contains(strings.ReplaceAll(r.text(), "\n", ""), r.g.flow.AuthURL[:60]) {
		t.Errorf("connect screen lacks the full link:\n%s", r.text())
	}

	// Enter before anything has happened says what to do.
	r.key(go3270.AIDEnter, nil)
	if !r.g.isError || !strings.Contains(r.text(), "Open the link") {
		t.Errorf("Enter too soon: %q", r.g.message)
	}
	// A pasted address from another authorization is refused, and kept.
	r.key(go3270.AIDEnter, map[string]string{gPasteField: "http://127.0.0.1:1/?state=other&code=good-code"})
	if !strings.Contains(r.g.message, "not from this authorization") || r.field(gPasteField).Content == "" {
		t.Errorf("wrong paste: %q, field %q", r.g.message, r.field(gPasteField).Content)
	}

	// The browser on this machine brings the code; Enter connects, and
	// offers the user's own calendar, not yet saved.
	auth, _ := url.Parse(r.g.flow.AuthURL)
	resp, err := http.Get(r.g.flow.ShortURL + "?" + url.Values{"state": {auth.Query().Get("state")}, "code": {"good-code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	r.key(go3270.AIDEnter, nil)
	if l := r.link(); r.g.step != googleChoose || l == nil || l.ClientID != "cid" || l.RefreshToken != "rt1" || len(l.Calendars) != 0 {
		t.Fatalf("after connecting: step %d, link %+v, message %q", r.g.step, l, r.g.message)
	}
	me := r.calIndex("me@example.com")
	if r.field(gSelField+me).Content != "X" || !strings.Contains(r.text(), "not saved") {
		t.Errorf("own calendar not offered:\n%s", r.text())
	}
	// A long name takes two rows.
	if !strings.Contains(r.text(), "so it takes two") {
		t.Errorf("long name cut:\n%s", r.text())
	}

	// An alias with a comma is refused.
	r.key(go3270.AIDEnter, map[string]string{gAliasField + me: "a,b"})
	if !r.g.isError || r.link().Calendars != nil {
		t.Errorf("comma in alias: %q, saved %+v", r.g.message, r.link().Calendars)
	}

	// Enter saves the choices, with a calendar typed by ID across both of
	// a blank entry's rows.
	id := "en.usa#holiday@group.v.calendar.google.com"
	r.key(go3270.AIDEnter, map[string]string{
		gAliasField + me: "me", gSelField + r.calIndex("c_team@group.calendar.google.com"): "x",
		gNewField + "0": id[:20], gNewMore + "0": id[20:], gNewAlias + "0": "hol",
	})
	got := r.link().Calendars
	if len(got) != 3 || got[0].Alias != "me" || got[1].ID != "c_team@group.calendar.google.com" || got[2].ID != id || got[2].Alias != "hol" || got[2].Name != "Holidays" {
		t.Fatalf("saved %+v, message %q", got, r.g.message)
	}
	if !strings.Contains(r.g.message, "Saved: showing 3 calendars.") {
		t.Errorf("message %q", r.g.message)
	}
	// Its agenda is the user's now.
	if c := cfg.agendaFor(u); c == nil {
		t.Errorf("connected with calendars, yet no agenda")
	}

	// A calendar that cannot be read is refused, and left typed.
	r.key(go3270.AIDEnter, map[string]string{gNewField + "0": "nope@example.com"})
	if !r.g.isError || !strings.Contains(r.g.message, "Not Found") || r.field(gNewField+"0").Content != "nope@example.com" {
		t.Errorf("unreadable calendar: %q", r.g.message)
	}

	// PF3 with choices not saved asks first; a second PF3 leaves.
	r.key(go3270.AIDEnter, map[string]string{gNewField + "0": ""})
	if leave, _ := r.key(go3270.AIDPF3, map[string]string{gSelField + me: ""}); leave || !r.g.leaveArmed {
		t.Errorf("PF3 with changes left, or did not ask")
	}
	if leave, _ := r.key(go3270.AIDPF3, nil); !leave {
		t.Errorf("second PF3 did not leave")
	}
	if len(r.link().Calendars) != 3 {
		t.Errorf("leaving saved: %+v", r.link().Calendars)
	}

	// Reconnecting by pasting keeps the calendars; PF3 there goes back.
	r.g, _ = startGoogle(store, 1)
	r.draw()
	r.key(go3270.AIDPF9, nil)
	if r.g.step != googleConnect || !r.g.reconnecting {
		t.Fatalf("PF9: step %d", r.g.step)
	}
	auth, _ = url.Parse(r.g.flow.AuthURL)
	pasted := r.g.flow.ShortURL + "?state=" + auth.Query().Get("state") + "&code=good-code&scope=x"
	r.key(go3270.AIDEnter, map[string]string{gPasteField: pasted})
	if l := r.link(); r.g.step != googleChoose || l.RefreshToken != "rt2" || len(l.Calendars) != 3 {
		t.Fatalf("after reconnecting: step %d, link %+v, message %q", r.g.step, l, r.g.message)
	}

	// Disconnecting asks first, then forgets and revokes the token.
	r.key(go3270.AIDPF6, nil)
	if r.g.step != googleDisconnect || !strings.Contains(r.text(), "Disconnect your Google calendar?") {
		t.Fatalf("PF6:\n%s", r.text())
	}
	r.key(go3270.AIDPF3, nil)
	if r.g.step != googleChoose || r.link() == nil {
		t.Errorf("PF3 on the confirmation disconnected, or stayed")
	}
	r.key(go3270.AIDPF6, nil)
	leave, said := r.key(go3270.AIDPF4, nil)
	if !leave || r.link() != nil || !strings.Contains(said, "Disconnected") {
		t.Errorf("PF4: leave %v, link %+v, said %q", leave, r.link(), said)
	}
	fake.mu.Lock()
	revoked := fake.revoked
	fake.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "rt2" {
		t.Errorf("revoked %v", revoked)
	}
	if cfg.agendaFor(u) != nil {
		t.Errorf("disconnected, yet an agenda")
	}
}

func TestGoogleChooseFits(t *testing.T) {
	newFakeGoogle(t)
	g := googleState{step: googleChoose, client: google.Client{ID: "c", Secret: "s"}, refreshToken: "rt"}
	for i := range 30 {
		g.saved = append(g.saved, users.Calendar{ID: strings.Repeat("x", i) + "@group.calendar.google.com", Name: strings.Repeat("Long name ", i%9)})
	}
	g.loadCalendars()
	for _, size := range [][2]int{{24, 80}, {32, 80}, {27, 132}} {
		seen := map[string]bool{}
		for g.page = 0; ; g.page++ {
			s, _, _ := g.build(size[0], size[1], now)
			screenText(t, s, size[0], size[1]) // fails on fields overlapping
			for _, i := range g.drawn {
				seen[g.calendars[i].ID] = true
			}
			if g.blanks > 0 {
				break
			}
		}
		if len(seen) != len(g.calendars) {
			t.Errorf("%v: %d of %d calendars shown", size, len(seen), len(g.calendars))
		}
	}
}

func TestGoogleClientScreen(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var g googleClientState
	var screen go3270.Screen
	var text string
	draw := func() {
		list, client, err := store.GoogleClient()
		screen, _, _ = buildGoogleClient(24, 80, now, list, client, err, &g)
		text = strings.Join(screenText(t, screen, 24, 80), "\n")
	}
	key := func(aid go3270.AID, typed map[string]string) bool {
		values := map[string]string{}
		for _, f := range screen {
			if f.Write {
				values[f.Name] = f.Content
			}
		}
		for k, v := range typed {
			values[k] = v
		}
		leave := g.handle(go3270.Response{AID: aid, Values: values}, store, func(string, ...any) {})
		draw()
		return leave
	}
	client := func() *users.GoogleClient {
		_, c, _ := store.GoogleClient()
		return c
	}
	draw()
	if !strings.Contains(text, "none set") || !strings.Contains(text, "Desktop app") {
		t.Errorf("no client:\n%s", text)
	}

	key(go3270.AIDEnter, nil)
	if !g.isError || client() != nil {
		t.Errorf("Enter with nothing typed: %q", g.message)
	}
	key(go3270.AIDEnter, map[string]string{gClientIDField: "first.apps.googleusercontent.com"})
	if !strings.Contains(g.message, "secret") || client() != nil {
		t.Errorf("ID without a secret: %q", g.message)
	}
	// The ID typed is kept, across both rows.
	longID := strings.Repeat("1", 12) + "-" + strings.Repeat("a", 32) + ".apps.googleusercontent.com"
	key(go3270.AIDEnter, map[string]string{gClientIDField: longID[:50], gClientIDMore: longID[50:], gClientSecretField: "GOCSPX-one"})
	if c := client(); c == nil || c.ClientID != longID || c.ClientSecret != "GOCSPX-one" {
		t.Fatalf("saved %+v, message %q", c, g.message)
	}
	// The secret is never shown.
	for _, f := range screen {
		if f.Name == gClientSecretField && (!f.Hidden || f.Content != "") {
			t.Errorf("secret field %+v", f)
		}
	}
	if strings.Contains(text, "GOCSPX") {
		t.Errorf("secret on the screen:\n%s", text)
	}
	// A new secret alone is saved as it is.
	key(go3270.AIDEnter, map[string]string{gClientSecretField: "GOCSPX-two"})
	if c := client(); c.ClientID != longID || c.ClientSecret != "GOCSPX-two" || g.confirming {
		t.Errorf("new secret: %+v, confirming %v", c, g.confirming)
	}

	// With a user connected, replacing the client asks first.
	if err := store.Update(func(list *[]users.User, _ func() int) error {
		(*list)[0].Google = &users.GoogleLink{ClientID: longID, RefreshToken: "rt"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key(go3270.AIDEnter, map[string]string{gClientIDField: "second", gClientIDMore: "", gClientSecretField: "s2"})
	if !g.confirming || !strings.Contains(text, "1 user connected through it") || client().ClientID != longID {
		t.Fatalf("replacing: confirming %v:\n%s", g.confirming, text)
	}
	key(go3270.AIDPF3, nil)
	if g.confirming || client().ClientID != longID {
		t.Errorf("PF3 on the confirmation replaced it")
	}
	key(go3270.AIDEnter, map[string]string{gClientIDField: "second", gClientIDMore: "", gClientSecretField: "s2"})
	key(go3270.AIDPF4, nil)
	if c := client(); c.ClientID != "second" || c.ClientSecret != "s2" {
		t.Errorf("after PF4: %+v", c)
	}

	// Blank both, it is removed (no one is connected through this one).
	key(go3270.AIDEnter, map[string]string{gClientIDField: "", gClientIDMore: ""})
	if !g.confirming || !strings.Contains(text, "Remove the Google client?") {
		t.Fatalf("removing:\n%s", text)
	}
	key(go3270.AIDPF4, nil)
	if client() != nil {
		t.Errorf("not removed")
	}

	// PF3 with something typed asks first.
	if key(go3270.AIDPF3, map[string]string{gClientIDField: "x"}) || !g.leaveArmed {
		t.Errorf("PF3 with something typed left, or did not ask")
	}
	if !key(go3270.AIDPF3, nil) {
		t.Errorf("second PF3 did not leave")
	}
}
