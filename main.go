// Command adhd-dash serves a dashboard to TN3270 (mainframe) terminal
// clients, each user's: the busy light's state, their calendar's next 24
// hours, and their open tasks from Trello. It also drives the busy light,
// Luxafor flags lit by the meetings of the users who control it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
	_ "time/tzdata" // the calendar's world clocks, on hosts without zoneinfo

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/backup"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/luxafor"
	"github.com/jmaslak/go-adhd-dash/internal/session"
	"github.com/jmaslak/go-adhd-dash/internal/streamdeck"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/web"
)

func main() {
	host := flag.String("host", "localhost", "address to listen on")
	port := flag.Int("port", 3270, "TCP port to listen on")
	refresh := flag.Duration("refresh", time.Second, "how often a screen that redraws itself (the dashboard, calendar, chat, activity viewer) is redrawn")
	useFlag := flag.Bool("flag", true, "drive the Luxafor flags attached to this machine as the busy light")
	useDeck := flag.Bool("streamdeck", true, "use a Stream Deck Mini attached to this machine as the busy light's buttons")
	controlPort := flag.Int("control-port", 0, "UDP port, on -host, for go-busy-indicator's busy command to set the busy light (0: none; unauthenticated)")
	externalRGB := flag.String("externalrgb", "", "command run with red, green and blue arguments (0-255) at each change of the busy light's color")
	agendaFile := flag.String("agenda-file", "", "show every user the agenda in this JSON file instead of their Google calendars")
	checklistDB := flag.String("checklist-db", checklist.DefaultPath(), "SQLite database the checklists are kept in")
	checklistJSON := flag.String("checklist-json", checklist.DefaultJSONPath(), "JSON file checklists were kept in before -checklist-db, imported into it at startup, then renamed with .imported added")
	usersFile := flag.String("users-file", users.DefaultPath(), "JSON file the users are kept in")
	maxConns := flag.Int("max-connections", 64, "most connections open at once, not counting this machine's (0: no limit)")
	maxConnsPerIP := flag.Int("max-connections-per-ip", 16, "most connections open at once from one address, an IPv6 one by its /64 (0: no limit)")
	loginTimeout := flag.Duration("login-timeout", 60*time.Second, "how long the login screen waits for a login")
	auditFile := flag.String("audit-log", defaultAuditPath(), "file logins, logouts and disconnections are logged to (empty: none)")
	agendaRefresh := flag.Duration("agenda-refresh", 5*time.Minute, "how often a calendar is read")
	httpPort := flag.Int("http-port", 3280, "TCP port the web pages (home, privacy policy, terms) are served on over HTTP, on -host (0: none)")
	backupDir := flag.String("backup-dir", backup.DefaultDir(), "directory -backup writes to, and -restore looks in for a file named without a directory")
	doBackup := flag.Bool("backup", false, "back up -users-file and -checklist-db into -backup-dir, then exit (safe while the server runs)")
	restoreFrom := flag.String("restore", "", "restore -users-file and -checklist-db from this backup, backing up what it replaces first, then exit (the server must be stopped)")
	flag.Parse()

	// The files kept at their defaults were dotfiles once; those are
	// moved to their visible names.
	for _, f := range []struct{ path, def string }{
		{*usersFile, users.DefaultPath()},
		{*checklistJSON, checklist.DefaultJSONPath()},
		{*auditFile, defaultAuditPath()},
	} {
		if f.path != f.def {
			continue
		}
		if said, err := moveLegacy(f.path); err != nil {
			log.Fatalf("moving the old %s: %v", f.path, err)
		} else if said != "" {
			log.Print(said)
		}
	}

	files := backup.Files{Users: *usersFile, Checklists: *checklistDB}
	switch {
	case *doBackup && *restoreFrom != "":
		log.Fatal("-backup and -restore cannot be used together")
	case *doBackup:
		path, err := backup.Create(*backupDir, files, time.Now())
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("backed up to %s", path)
		return
	case *restoreFrom != "":
		if err := restore(*restoreFrom, *backupDir, files); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Held while the server runs, so that nothing is restored under it,
	// nor another server started on the same files.
	unlock, err := backup.Lock(files.LockPath())
	if errors.Is(err, backup.ErrInUse) {
		log.Fatalf("another adhd-dash is using %s", *checklistDB)
	} else if err != nil {
		log.Fatal(err)
	}
	defer unlock()

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
		log.Printf("created %s with the user %q, password %q: log in with them, and you are asked to change the password before anything else", userStore.Path(), users.FirstName, users.FirstPassword)
	}

	// Checklists made before each was a user's become the admin's, and any
	// of a user no longer there, which no one could reach, are removed.
	checklistStore, err := checklist.Open(*checklistDB)
	if err != nil {
		log.Fatal(err)
	}
	defer checklistStore.Close() //nolint:errcheck
	if n, err := checklistStore.ImportJSON(*checklistJSON); err != nil {
		log.Fatal(err)
	} else if n > 0 {
		log.Printf("imported %d checklists from %s into %s; it is renamed %s.imported", n, *checklistJSON, *checklistDB, *checklistJSON)
	}
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
		TaskPool:   tasks.NewPool(),
		Checklists: checklistStore,
		Backup: func() (string, error) {
			return backup.Create(*backupDir, files, time.Now())
		},
		Users:        userStore,
		Audit:        auditLog,
		Limits:       session.NewConnLimiter(*maxConns, *maxConnsPerIP),
		LoginTimeout: *loginTimeout,
		Viewers:      session.NewViewers(),
		Activity:     session.NewActivity(),
		Chat:         session.NewChat(),
	}

	// Each user's Google calendars are read while they are in use; a file
	// stands in for them all.
	if *agendaFile != "" {
		cfg.Agenda = agenda.NewCache(agenda.File{Path: *agendaFile})
		go cfg.Agenda.Run(ctx, *agendaRefresh)
	} else {
		cfg.Agendas = agenda.NewPool(ctx, *agendaRefresh)
	}

	// The busy light: lit by the meetings of the users who control it, read
	// at each decision so that it follows changes to them and their
	// calendars.
	var light busy.Light
	if *useFlag {
		f := luxafor.New()
		defer f.Close() //nolint:errcheck
		light = f
	}
	indicator := busy.New(light, func() ([]agenda.Event, int) { return cfg.FlagEvents() })
	indicator.External = *externalRGB
	cfg.Busy, cfg.BusyKeys = indicator, indicator
	go indicator.Run(ctx, busyInterval)

	// The Stream Deck's keys set the light, as the sd program's did.
	deckDone := make(chan struct{})
	if *useDeck {
		deck := streamdeck.New(streamdeck.BusyButtons(indicator.Key, log.Printf))
		go func() { deck.Run(ctx); close(deckDone) }()
	} else {
		close(deckDone)
	}
	// Everyone else sees a busy state of their own, by the same rules.
	cfg.Personal = busy.NewPersonal(func(id int) []agenda.Event { return cfg.UserMeetings(id) })
	if *controlPort != 0 {
		addr := net.JoinHostPort(*host, fmt.Sprint(*controlPort))
		go func() {
			if err := indicator.ListenControl(ctx, addr); err != nil {
				log.Fatal(err)
			}
		}()
		log.Printf("busy light control port on udp %s", addr)
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
	// Stopped, the Stream Deck is blanked; it is given a moment for that.
	cancel()
	select {
	case <-deckDone:
	case <-time.After(2 * time.Second):
	}
	log.Printf("shut down")
}

// busyInterval is how often the busy light is decided afresh: often enough
// that it comes on close to two minutes before a meeting.
const busyInterval = 15 * time.Second

// shutdownGrace is how long sessions have to say goodbye and disconnect on
// shutdown before their connections are closed under them.
const shutdownGrace = 5 * time.Second

// defaultAuditPath is where the audit log goes unless told otherwise:
// adhd-dash-audit.log in the home directory.
// restore restores files from the backup at path, or, if path names no
// directory and is not in the current one, from that file in dir, and
// says what it did.
func restore(path, dir string, files backup.Files) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && filepath.Base(path) == path {
		path = filepath.Join(dir, path)
	}
	r, err := backup.Restore(path, files, dir, time.Now())
	if err != nil {
		return err
	}
	if r.Saved != "" {
		log.Printf("backed up what was there to %s", r.Saved)
	}
	if r.Users {
		log.Printf("restored %s from %s, made %s", files.Users, path, r.Created.Local().Format(time.DateTime))
	}
	if r.Checklists {
		log.Printf("restored %s from %s, made %s", files.Checklists, path, r.Created.Local().Format(time.DateTime))
	}
	return nil
}

func defaultAuditPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-audit.log")
	}
	return "adhd-dash-audit.log"
}

// moveLegacy gives path the file that was kept, before the files were
// visible, under its name with a dot before it, if there is one and path
// has none yet, reporting what it did. With both, path is left as it is,
// and the old one too, for someone to look at.
func moveLegacy(path string) (string, error) {
	old := filepath.Join(filepath.Dir(path), "."+filepath.Base(path))
	if _, err := os.Lstat(old); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	switch _, err := os.Lstat(path); {
	case err == nil:
		return fmt.Sprintf("both %s and %s exist; using %s, and leaving %s alone", path, old, path, old), nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	if err := os.Rename(old, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s to %s", old, path), nil
}
