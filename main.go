// Copyright (C) 2026 Joelle Maslak
// All Rights Reserved - See License

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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // the calendar's world clocks, on hosts without zoneinfo

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/audit"
	"github.com/jmaslak/go-adhd-dash/internal/backup"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/certs"
	"github.com/jmaslak/go-adhd-dash/internal/checklist"
	"github.com/jmaslak/go-adhd-dash/internal/db"
	"github.com/jmaslak/go-adhd-dash/internal/luxafor"
	"github.com/jmaslak/go-adhd-dash/internal/recurring"
	"github.com/jmaslak/go-adhd-dash/internal/session"
	"github.com/jmaslak/go-adhd-dash/internal/streamdeck"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
	"github.com/jmaslak/go-adhd-dash/internal/timers"
	"github.com/jmaslak/go-adhd-dash/internal/users"
	"github.com/jmaslak/go-adhd-dash/internal/weather"
	"github.com/jmaslak/go-adhd-dash/internal/web"
	"github.com/jmaslak/go-adhd-dash/internal/web3270"
)

func main() {
	host := flag.String("host", "localhost", "address to listen on")
	port := flag.Int("port", 3270, "TCP port to listen on (TLS from the first byte while there is a TLS domain)")
	refresh := flag.Duration("refresh", time.Second, "how often a screen that redraws itself (the dashboard, calendar, chat, activity viewer) is redrawn")
	useFlag := flag.Bool("flag", true, "drive the Luxafor flags attached to this machine as the busy light")
	useDeck := flag.Bool("streamdeck", true, "use a Stream Deck Mini attached to this machine as the busy light's buttons")
	controlPort := flag.Int("control-port", 0, "UDP port, on -host, for go-busy-indicator's busy command to set the busy light (0: none; unauthenticated)")
	externalRGB := flag.String("externalrgb", "", "command run with red, green and blue arguments (0-255) at each change of the busy light's color")
	agendaFile := flag.String("agenda-file", "", "show every user the agenda in this JSON file instead of their Google calendars")
	dbPath := flag.String("db", db.DefaultPath(), "SQLite database the checklists, recurring tasks and each user's timer are kept in")
	checklistJSON := flag.String("checklist-json", checklist.DefaultJSONPath(), "JSON file checklists were kept in before there was a database, imported into -db at startup, then renamed with .imported added")
	usersFile := flag.String("users-file", users.DefaultPath(), "JSON file the users are kept in")
	maxConns := flag.Int("max-connections", 64, "most connections open at once (0: no limit)")
	maxConnsPerIP := flag.Int("max-connections-per-ip", 16, "most connections open at once from one address, an IPv6 one by its /64 (0: no limit)")
	loginTimeout := flag.Duration("login-timeout", 60*time.Second, "how long the login screen waits for a login")
	auditFile := flag.String("audit-log", defaultAuditPath(), "file logins, logouts and disconnections are logged to (empty: none)")
	agendaRefresh := flag.Duration("agenda-refresh", 5*time.Minute, "how often a calendar is read")
	httpPort := flag.Int("http-port", 3280, "TCP port the web pages (home, privacy policy, terms) are served on over HTTP, on -host (0: none)")
	backupDir := flag.String("backup-dir", backup.DefaultDir(), "directory -backup writes to, and -restore looks in for a file named without a directory")
	doBackup := flag.Bool("backup", false, "back up -users-file and -db into -backup-dir, then exit (safe while the server runs)")
	restoreFrom := flag.String("restore", "", "restore -users-file and -db from this backup, backing up what it replaces first, then exit (the server must be stopped)")
	tlsDomainFile := flag.String("tls-domain-file", certs.DefaultDomainFile(), "plain text file the TLS domain, set on the admin menu, is kept in; a domain there turns on TLS on -port and HTTPS on -https-port, with Let's Encrypt certificates")
	tlsEmail := flag.String("tls-email", "", "contact email given to the certificate authority (empty: none)")
	tlsDir := flag.String("tls-dir", certs.DefaultDir(), "directory the certificates, their keys and the ACME account key are kept in")
	tlsACME := flag.String("tls-acme-url", certs.LetsEncryptURL, "the certificate authority's ACME directory")
	tlsImportCert := flag.String("tls-import-cert", "", "import this certificate chain (PEM), with -tls-import-key, into -tls-dir for the names it is for, then exit")
	tlsImportKey := flag.String("tls-import-key", "", "the private key (PEM) of -tls-import-cert")
	httpsPort := flag.Int("https-port", 443, "TCP port the web pages are served on over HTTPS, on -host, while there is a TLS domain (0: none); the certificate authority's tls-alpn-01 challenges come to 443")
	acmeHTTPPort := flag.Int("acme-http-port", 80, "TCP port, on -host, answering the certificate authority's http-01 challenges and sending browsers to HTTPS, while there is a TLS domain (0: none)")
	web3270Bin := flag.String("web3270-bin", "web3270", "moshix's web3270, a TN3270 terminal in the browser, run while enabled on the admin menu and there is a TLS domain (a path, or a name looked for in $PATH)")
	web3270File := flag.String("web3270-file", web3270.DefaultFile(), "plain text file saying the web terminal is enabled, set on the admin menu")
	web3270Port := flag.Int("web3270-port", 3271, "TCP port web3270 listens on, over HTTP, on the loopback address")
	web3270ProxyPort := flag.Int("web3270-proxy-port", 32700, "TCP port, on -host, web3270 is served on over HTTPS, through a proxy here")
	web3270TN3270Port := flag.Int("web3270-tn3270-port", 3272, "TCP port, on the loopback address, of the unencrypted TN3270 port web3270 is locked to")
	flag.Parse()

	if *tlsImportCert != "" || *tlsImportKey != "" {
		if err := importCert(*tlsDir, *tlsDomainFile, *tlsImportCert, *tlsImportKey); err != nil {
			log.Fatal(err)
		}
		return
	}

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

	// The database at its default was named for the checklists, when it
	// held only them; it is renamed.
	if *dbPath == db.DefaultPath() {
		if said, err := moveOldDB(*dbPath); err != nil {
			log.Fatal(err)
		} else if said != "" {
			log.Print(said)
		}
	}

	files := backup.Files{Users: *usersFile, DB: *dbPath}
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
		log.Fatalf("another adhd-dash is using %s", *dbPath)
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

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close() //nolint:errcheck

	// Checklists made before each was a user's become the admin's, and any
	// of a user no longer there, which no one could reach, are removed.
	checklistStore, err := checklist.New(database)
	if err != nil {
		log.Fatal(err)
	}
	if n, err := checklistStore.ImportJSON(*checklistJSON); err != nil {
		log.Fatal(err)
	} else if n > 0 {
		log.Printf("imported %d checklists from %s into %s; it is renamed %s.imported", n, *checklistJSON, *dbPath, *checklistJSON)
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
	// Any timer of a user no longer there, or of no one, is removed.
	timerStore, err := timers.New(database)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := timerStore.RemoveOwned(func(owner int) bool { return !userIDs[owner] }); err != nil {
		log.Fatalf("timers: %v", err)
	}
	// So is any recurring task of a user no longer there.
	recurringStore, err := recurring.New(database)
	if err != nil {
		log.Fatal(err)
	}
	if n, err := recurringStore.RemoveOwned(func(owner int) bool { return !userIDs[owner] }); err != nil {
		log.Fatalf("recurring tasks: %v", err)
	} else if n > 0 {
		log.Printf("removed %d recurring tasks of users no longer there", n)
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
		Timers:     timerStore,
		Recurring:  recurringStore,
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
		Weather: weather.NewCache(&weather.Client{}, func() string {
			key, err := userStore.AirNowKey()
			if err != nil {
				log.Printf("AirNow key: %v", err)
			}
			return key
		}),
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

	// The web pages, over HTTP for a web server in front, which gives
	// their public HTTPS address, set on the admin menu.
	webHandler := &web.Server{Users: userStore, Audit: auditLog}
	if *httpPort != 0 {
		httpAddr := net.JoinHostPort(*host, fmt.Sprint(*httpPort))
		serveHTTP(httpAddr, webHandler, shutdown)
		cfg.HTTPListen = "http://" + httpAddr
	}

	// TLS, on while there is a domain, set on the admin menu: certificates
	// got and renewed from Let's Encrypt, the web pages over HTTPS, and the
	// TN3270 port in TLS.
	certMgr, err := certs.New(certs.Config{Dir: *tlsDir, Email: *tlsEmail, DirectoryURL: *tlsACME})
	if err != nil {
		log.Fatal(err)
	}
	// The web terminal, made below, is restarted when the domain changes,
	// and reached from the HTTPS site at /3270.
	var webTerm *web3270.Supervisor
	tlsService := certs.NewService(certs.ServiceConfig{
		Manager: certMgr, File: *tlsDomainFile,
		Host: *host, HTTPSPort: *httpsPort, ACMEPort: *acmeHTTPPort,
		Web: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/3270" || r.URL.Path == "/3270/" {
				if u := webTerm.URL(r.Host); u != "" {
					http.Redirect(w, r, u, http.StatusFound)
					return
				}
			}
			webHandler.ServeHTTP(w, r)
		}),
		OnChange: func() { webTerm.Reconcile() },
	})
	// A domain that cannot be served is not quietly left unencrypted.
	if err := tlsService.Start(); err != nil {
		log.Fatalf("tls (to turn it off, remove %s): %v", *tlsDomainFile, err)
	}
	cfg.TLS = tlsService
	go func() {
		<-shutdown.Done()
		tlsService.Close()
	}()
	// The web terminal: web3270, run here in kiosk mode, locked to a TN3270
	// port of its own on the loopback address (it speaks only unencrypted
	// TN3270), and served over HTTPS through a proxy.
	webTerm = web3270.New(web3270.Config{
		Bin: *web3270Bin, File: *web3270File,
		Host: *host, ProxyPort: *web3270ProxyPort,
		ListenPort: *web3270Port, TN3270Port: *web3270TN3270Port,
		Serve:  func(c net.Conn) { session.Handle(c, cfg) },
		Domain: tlsService.Domain,
		TLS:    certMgr.TN3270Config(),
	})
	// Set before it starts, since its sessions are given cfg.
	cfg.Web3270 = webTerm
	if err := webTerm.Start(); err != nil {
		log.Fatalf("web3270 (%s): %v", *web3270File, err)
	}
	// Stopped before the server ends (below), so that it does not outlive
	// it.
	defer webTerm.Close()

	// SIGTERM (systemctl stop) and SIGINT shut down as the admin menu
	// does: sessions are told, and web3270 is stopped.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt)
	go func() {
		sig := <-sigs
		log.Printf("shutting down on %v", sig)
		shutdown.Request()
	}()

	// Started once the challenges can be answered, it gets any certificate
	// missing, rather than at the first connection, and says each day how
	// long they have left.
	go certMgr.Watch(ctx, 24*time.Hour, certWarnLeft, log.Printf)

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", addr, err)
	}
	if d := tlsService.Domain(); d != "" {
		log.Printf("adhd-dash listening on %s, over TLS for %s", addr, d)
	} else {
		log.Printf("adhd-dash listening on %s", addr)
	}

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
		// In TLS while it is on, the handshake done in the session, under
		// its negotiation's time limit.
		go session.Handle(tlsService.Wrap(conn), cfg)
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

// certWarnLeft is how little of a certificate's life left is warned of:
// renewal begins with a third of it left, 30 days of Let's Encrypt's 90,
// and is retried until it works.
const certWarnLeft = 14 * 24 * time.Hour

// shutdownGrace is how long sessions have to say goodbye and disconnect on
// shutdown before their connections are closed under them.
const shutdownGrace = 5 * time.Second

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
	if r.DB {
		log.Printf("restored %s from %s, made %s", files.DB, path, r.Created.Local().Format(time.DateTime))
	}
	return nil
}

// defaultAuditPath is where the audit log goes unless told otherwise:
// adhd-dash-audit.log in the home directory.
func defaultAuditPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "adhd-dash-audit.log")
	}
	return "adhd-dash-audit.log"
}

// serveHTTP serves h at addr over HTTP until shutdown.
func serveHTTP(addr string, h http.Handler, shutdown *session.Shutdown) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", addr, err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("web pages served on http://%s", addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web: %v", err)
		}
	}()
	go func() {
		<-shutdown.Done()
		srv.Close() //nolint:errcheck
	}()
}

// importCert imports the certificate in certFile, with its key in keyFile,
// into dir for the names it is for, and those in domainFile it is good for
// by a wildcard, saying what it did.
func importCert(dir, domainFile, certFile, keyFile string) error {
	if certFile == "" || keyFile == "" {
		return errors.New("-tls-import-cert and -tls-import-key go together")
	}
	var domains []string
	if data, err := os.ReadFile(domainFile); err == nil {
		if domains, err = certs.ParseDomains(string(data)); err != nil {
			return fmt.Errorf("%s: %v", domainFile, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	imported, err := certs.Import(dir, domains, certPEM, keyPEM, time.Now())
	for _, im := range imported {
		if im.Kept {
			log.Printf("kept the certificate for %s in %s, expiring %s, no sooner than %s's", im.Domain, dir, im.NotAfter.Local().Format(time.DateTime), certFile)
		} else {
			log.Printf("imported the certificate for %s into %s, expiring %s", im.Domain, dir, im.NotAfter.Local().Format(time.DateTime))
		}
	}
	if err != nil {
		return fmt.Errorf("importing %s: %v", certFile, err)
	}
	return nil
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

// moveOldDB gives path, the database's default, the database kept at its
// old default, adhd-dash-checklists.db, if there is one and path has none
// yet, reporting what it did. With both, path is used, and the old one
// left alone. An older server still using the old one, holding its lock,
// is not moved under: that is an error.
func moveOldDB(path string) (string, error) {
	old := db.OldDefaultPath()
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
	unlock, err := backup.Lock(backup.LockPath(old))
	if errors.Is(err, backup.ErrInUse) {
		return "", fmt.Errorf("an older adhd-dash is using %s; stop it, so that it can be renamed %s", old, path)
	} else if err != nil {
		return "", err
	}
	defer unlock()
	moved, err := db.Move(old, path)
	if err != nil || !moved {
		return "", err
	}
	// Removed while held, so that no server takes it meanwhile; where
	// there is no flock, there is none.
	if err := os.Remove(backup.LockPath(old)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return fmt.Sprintf("moved %s to %s", old, path), nil
}
