// Command adhd-dash serves a dashboard to TN3270 (mainframe) terminal
// clients: the busy indicator's state, the calendar's next 24 hours, and the
// open tasks from Trello.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
	_ "time/tzdata" // the calendar's world clocks, on hosts without zoneinfo

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/session"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/web"
)

func main() {
	host := flag.String("host", "localhost", "address to listen on")
	port := flag.Int("port", 3270, "TCP port to listen on")
	refresh := flag.Duration("refresh", 10*time.Second, "how often an idle screen is redrawn")
	busyURL := flag.String("busy-url", "", "busy indicator status feed, e.g. ws://localhost:3334/feed (empty: none)")
	busyFile := flag.String("busy-file", "", "read the busy indicator's status from this JSON file instead of a feed")
	busyControl := flag.String("busy-control", "localhost:3333", "busy indicator UDP control port as host:port, for PF1 (busy) and PF2 (off) (empty: none)")
	agendaFile := flag.String("agenda-file", "", "show every user the agenda in this JSON file instead of their Google calendars")
	checklistFile := flag.String("checklist-file", checklist.DefaultPath(), "JSON file the checklists are kept in")
	usersFile := flag.String("users-file", users.DefaultPath(), "JSON file the users are kept in")
	maxConns := flag.Int("max-connections", 64, "most connections open at once, not counting this machine's (0: no limit)")
	maxConnsPerIP := flag.Int("max-connections-per-ip", 16, "most connections open at once from one address, an IPv6 one by its /64 (0: no limit)")
	loginTimeout := flag.Duration("login-timeout", 60*time.Second, "how long the login screen waits for a login")
	auditFile := flag.String("audit-log", defaultAuditPath(), "file logins, logouts and disconnections are logged to (empty: none)")
	agendaRefresh := flag.Duration("agenda-refresh", 5*time.Minute, "how often a calendar is read")
	httpPort := flag.Int("http-port", 3280, "TCP port the web pages (home, privacy policy, terms) are served on over HTTP, on -host (0: none)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdown := session.NewShutdown()

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
	userList, created, err := userStore.Load()
	if err != nil {
		log.Fatalf("users: %v", err)
	} else if created {
		log.Printf("created %s with the user %q, password %q, which is refused at the login screen: connect as the CONSOLE LU from this machine and change it", userStore.Path(), users.FirstName, users.FirstPassword)
	}

	// Checklists made before each was a user's become the admin's, and any
	// of a user no longer there, which no one could reach, are removed.
	checklistStore := checklist.NewStore(*checklistFile)
	userIDs := map[int]bool{}
	for _, u := range userList {
		userIDs[u.ID] = true
	}
	if n, err := checklistStore.RemoveOwned(func(owner int) bool { return owner != 0 && !userIDs[owner] }); err != nil {
		log.Fatalf("checklists: %v", err)
	} else if n > 0 {
		log.Printf("removed %d checklists of users no longer there", n)
	}
	if admin, ok := users.Admin(userList); ok {
		if n, err := checklistStore.AssignUnowned(admin.ID); err != nil {
			log.Fatalf("checklists: %v", err)
		} else if n > 0 {
			log.Printf("gave %d checklists with no owner to %q", n, admin.Name)
		}
	}
	cfg := session.Config{
		Shutdown: shutdown,
		Refresh:  *refresh, AgendaRefresh: *agendaRefresh,
		TaskPool:     tasks.NewPool(),
		Checklists:   checklistStore,
		Users:        userStore,
		Audit:        auditLog,
		Limits:       session.NewConnLimiter(*maxConns, *maxConnsPerIP),
		LoginTimeout: *loginTimeout,
		Viewers:      session.NewViewers(),
		Activity:     session.NewActivity(),
		Chat:         session.NewChat(),
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

	// Each user's Google calendars are read while they are in use; a file
	// stands in for them all.
	if *agendaFile != "" {
		cfg.Agenda = agenda.NewCache(agenda.File{Path: *agendaFile})
		go cfg.Agenda.Run(ctx, *agendaRefresh)
	} else {
		cfg.Agendas = agenda.NewPool(ctx, *agendaRefresh)
	}

	// The web pages, for a web server in front to give their public HTTPS
	// address, set on the admin menu.
	if *httpPort != 0 {
		httpAddr := net.JoinHostPort(*host, fmt.Sprint(*httpPort))
		httpLn, err := net.Listen("tcp", httpAddr)
		if err != nil {
			log.Fatalf("listen on %s: %v", httpAddr, err)
		}
		cfg.HTTPListen = httpAddr
		srv := &http.Server{
			Handler:           &web.Server{Users: userStore, Audit: auditLog},
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    16 << 10,
		}
		log.Printf("web pages served on http://%s", httpAddr)
		go func() {
			if err := srv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("web: %v", err)
			}
		}()
		go func() {
			<-shutdown.Done()
			srv.Close() //nolint:errcheck
		}()
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
