// Command adhd-dash serves a dashboard to TN3270 (mainframe) terminal
// clients: the busy indicator's state, the calendar's next 24 hours, and the
// open tasks from Trello.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // the calendar's world clocks, on hosts without zoneinfo

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/session"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
)

func main() {
	host := flag.String("host", "localhost", "address to listen on")
	port := flag.Int("port", 3270, "TCP port to listen on")
	refresh := flag.Duration("refresh", 10*time.Second, "how often an idle screen is redrawn")
	busyURL := flag.String("busy-url", "", "busy indicator status feed, e.g. ws://localhost:3334/feed (empty: none)")
	busyFile := flag.String("busy-file", "", "read the busy indicator's status from this JSON file instead of a feed")
	busyControl := flag.String("busy-control", "localhost:3333", "busy indicator UDP control port as host:port, for PF1 (busy) and PF2 (off) (empty: none)")
	calendars := flag.String("calendar", "", "comma-separated Google calendars for the agenda (empty: none)")
	calendarAliases := flag.String("calendar-alias", "", "comma-separated short names for the -calendar calendars, in the same order, shown in brackets before their events")
	agendaFile := flag.String("agenda-file", "", "read the agenda from this JSON file instead of Google Calendar")
	checklistFile := flag.String("checklist-file", checklist.DefaultPath(), "JSON file the checklists are kept in")
	usersFile := flag.String("users-file", users.DefaultPath(), "JSON file the users are kept in")
	auditFile := flag.String("audit-log", defaultAuditPath(), "file logins, logouts and disconnections are logged to (empty: none)")
	agendaRefresh := flag.Duration("agenda-refresh", 5*time.Minute, "how often the calendar is read")
	flag.Parse()

	if *calendars != "" && *agendaFile != "" {
		log.Fatal("-calendar and -agenda-file cannot both be given")
	}
	calendarList := strings.Split(*calendars, ",")
	var aliases []string
	if *calendarAliases != "" {
		if *calendars == "" {
			log.Fatal("-calendar-alias needs -calendar")
		}
		for _, a := range strings.Split(*calendarAliases, ",") {
			aliases = append(aliases, strings.TrimSpace(a))
		}
		if len(aliases) != len(calendarList) {
			log.Fatalf("-calendar-alias has %d names for %d calendars", len(aliases), len(calendarList))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdown := session.NewShutdown()
	taskCache := tasks.NewCache()

	var auditLog *audit.Log
	if *auditFile != "" {
		var err error
		if auditLog, err = audit.Open(*auditFile); err != nil {
			log.Fatal(err)
		}
		defer auditLog.Close() //nolint:errcheck
	}

	// Made now, with its first user, rather than when first looked at.
	userStore := users.NewStore(*usersFile)
	if _, created, err := userStore.Load(); err != nil {
		log.Fatalf("users: %v", err)
	} else if created {
		log.Printf("created %s with the user %q, password %q: change it", userStore.Path(), users.FirstName, users.FirstPassword)
	}
	cfg := session.Config{
		Shutdown: shutdown,
		Refresh:  *refresh, AgendaRefresh: *agendaRefresh,
		Tasks: taskCache, Archiver: taskCache, Adder: taskCache,
		Checklists: checklist.NewStore(*checklistFile),
		Users:      userStore,
		Audit:      auditLog,
		Viewers:    session.NewViewers(),
		Activity:   session.NewActivity(),
		Chat:       session.NewChat(),
	}

	switch {
	case *busyURL != "" && *busyFile != "":
		log.Fatal("-busy-url and -busy-file cannot both be given")
	case *busyURL != "":
		watcher := busy.NewWatcher(*busyURL)
		go watcher.Run(ctx)
		cfg.Busy = watcher
	case *busyFile != "":
		cfg.Busy = busy.File{Path: *busyFile}
	}
	if *busyControl != "" {
		cfg.BusyControl = &busy.Control{Addr: *busyControl}
	}

	var source agenda.Source
	switch {
	case *agendaFile != "":
		source = agenda.File{Path: *agendaFile}
	case *calendars != "":
		source = agenda.NewGoogle(calendarList, aliases)
	}
	if source != nil {
		cfg.Agenda = agenda.NewCache(source)
		go cfg.Agenda.Run(ctx, *agendaRefresh)
	}

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", addr, err)
	}
	log.Printf("adhd-dash listening on %s", addr)

	go func() {
		<-shutdown.Done()
		ln.Close() //nolint:errcheck // ends the accept loop
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if shutdown.Requested() {
				break
			}
			log.Printf("accept: %v", err)
			continue
		}
		go session.Handle(conn, cfg)
	}

	if n := shutdown.Sessions(); n > 0 {
		log.Printf("shutting down: waiting for %d sessions to disconnect", n)
	}
	shutdown.Wait(shutdownGrace)
	log.Printf("shut down")
}

// shutdownGrace is how long sessions have to say goodbye and disconnect on
// shutdown before their connections are closed under them.
const shutdownGrace = 5 * time.Second

// defaultAuditPath is where the audit log goes unless told otherwise:
// .adhd-dash-audit.log in the home directory.
func defaultAuditPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".adhd-dash-audit.log")
	}
	return ".adhd-dash-audit.log"
}
