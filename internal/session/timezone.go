// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

package session

import (
	_ "embed"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/users"
)

// zones are the time zones loaded, by name, as loading one reads a file,
// and the zone is looked up on every redraw.
var zones sync.Map

// loadZone is the time zone called name, an IANA name such as
// America/Denver; the server's for "".
func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	// "Local" would load the server's zone under a name that says nothing
	// of where it is; blank says that.
	if name == "Local" {
		return nil, errors.New(`leave it blank for the server's`)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	zones.Store(name, loc)
	return loc, nil
}

// locationOf is the time zone prefs shows times in: the server's when they
// name none, or one that will not load, as the server's zone files may have
// changed since it was chosen.
func locationOf(prefs users.Preferences) *time.Location {
	loc, err := loadZone(prefs.TimeZone)
	if err != nil {
		return time.Local
	}
	return loc
}

// locationFor is the time zone u's times are shown in: the server's for no
// one, or with no user database.
func (cfg Config) locationFor(u *users.User) *time.Location {
	if u == nil {
		return time.Local
	}
	return locationOf(prefsOf(cfg.Users, u.ID))
}

// zoneText names loc for a person, by its name and abbreviation at now:
// "America/Denver (MDT)".
func zoneText(loc *time.Location, now time.Time) string {
	abbr, _ := now.In(loc).Zone()
	name := loc.String()
	if name == "Local" {
		name = "server time"
	}
	if abbr == name {
		return name
	}
	return name + " (" + abbr + ")"
}

// zoneNamesText is every time zone the preferences screen lists, one per
// line, as zonenames_gen.go wrote it.
//
//go:generate go run zonenames_gen.go
//go:embed zonenames.txt
var zoneNamesText string

// zoneNames is every time zone the preferences screen lists, in order.
var zoneNames = strings.Fields(zoneNamesText)

// zoneColumnGap is the space between the time zone list's columns.
const zoneColumnGap = 2

// zonePage is the names, of zoneNames, on one page of the time zone list:
// from first, in columns perColumn long, each as wide as its longest name.
type zonePage struct {
	first, count int
	widths       []int
}

// zonePages lays zoneNames out in pages of columns perColumn names long,
// as many columns to a page as fit in width.
func zonePages(perColumn, width int) []zonePage {
	perColumn = max(perColumn, 1)
	var pages []zonePage
	for first := 0; first < len(zoneNames); {
		page := zonePage{first: first}
		used := 0
		for next := first; next < len(zoneNames); {
			column := zoneNames[next:min(next+perColumn, len(zoneNames))]
			w := 0
			for _, n := range column {
				w = max(w, len(n))
			}
			// The first column always goes in, however narrow the screen.
			if len(page.widths) > 0 && used+w > width {
				break
			}
			page.widths = append(page.widths, w)
			used += w + zoneColumnGap
			next += len(column)
			page.count += len(column)
		}
		pages = append(pages, page)
		first += page.count
	}
	return pages
}

// zonePageOf is the page of pages that lists name, or the first.
func zonePageOf(pages []zonePage, name string) int {
	i := slices.Index(zoneNames, name)
	for p, page := range pages {
		if i >= page.first && i < page.first+page.count {
			return p
		}
	}
	return 0
}
