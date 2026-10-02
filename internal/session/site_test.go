package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/racingmars/go3270"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func TestSiteScreen(t *testing.T) {
	store := users.NewStore(filepath.Join(t.TempDir(), "users.json"))
	if _, _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var s siteState
	var screen go3270.Screen
	var text string
	draw := func() {
		site, err := store.Site()
		screen, _, _ = buildSite(24, 80, now, site, err, "localhost:3280", &s)
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
		leave := s.handle(go3270.Response{AID: aid, Values: values}, store, func(string, ...any) {})
		draw()
		return leave
	}
	saved := func() *users.Site {
		site, err := store.Site()
		if err != nil {
			t.Fatal(err)
		}
		return site
	}

	draw()
	if !strings.Contains(text, "served over HTTP at localhost:3280") || !strings.Contains(text, "by path alone") {
		t.Errorf("unset:\n%s", text)
	}

	// Bad entries are refused, and left typed.
	for typed, want := range map[[3]string]string{
		{"", "Example Org", ""}:                                              "address too",
		{"adhd.example.com", "", ""}:                                         "https://",
		{"https://adhd.example.com", "", "not-an-email"}:                     "not an email",
		{"https://adhd.example.com", strings.Repeat("x", siteMaxText+1), ""}: "too long",
	} {
		key(go3270.AIDEnter, map[string]string{siteURLField: typed[0], siteOrgField: typed[1], siteContactField: typed[2]})
		if !s.isError || !strings.Contains(s.message, want) || saved() != nil {
			t.Errorf("%q: message %q, saved %+v", typed, s.message, saved())
		}
		if s.typed[siteURLField] != typed[0] {
			t.Errorf("%q: not left typed", typed)
		}
	}

	// A good one is saved, the address ending in a slash, and the pages'
	// addresses listed for Google's consent screen.
	key(go3270.AIDEnter, map[string]string{siteURLField: "https://adhd.example.com", siteOrgField: "Example Org", siteContactField: "it@example.com"})
	if got := saved(); got == nil || *got != (users.Site{BaseURL: "https://adhd.example.com/", Organization: "Example Org", Contact: "it@example.com"}) {
		t.Fatalf("saved %+v, message %q", got, s.message)
	}
	for _, want := range []string{"https://adhd.example.com/privacy", "https://adhd.example.com/terms", "Saved."} {
		if !strings.Contains(text, want) {
			t.Errorf("after saving, lacks %q:\n%s", want, text)
		}
	}

	// PF3 with a change typed asks first.
	if key(go3270.AIDPF3, map[string]string{siteOrgField: "Other"}) || !s.leaveArmed || saved().Organization != "Example Org" {
		t.Errorf("PF3 with a change left, or saved it")
	}
	if !key(go3270.AIDPF3, nil) {
		t.Errorf("second PF3 did not leave")
	}

	// Blanking everything removes the settings.
	key(go3270.AIDEnter, map[string]string{siteURLField: "", siteOrgField: "", siteContactField: ""})
	if saved() != nil || s.message != "Removed the web site's settings." {
		t.Errorf("after blanking: %+v, %q", saved(), s.message)
	}

	// Not served, the screen says so.
	site, err := store.Site()
	screen, _, _ = buildSite(24, 80, now, site, err, "", &s)
	if text := strings.Join(screenText(t, screen, 24, 80), "\n"); !strings.Contains(text, "not served (-http-port 0)") {
		t.Errorf("not served:\n%s", text)
	}
}
