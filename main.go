// Command adhd-dash serves a dashboard to TN3270 (mainframe) terminal
// clients: the busy indicator's state, the calendar's next 24 hours, and the
// open tasks from the task program.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/jmaslak/go-adhd-dash/internal/agenda"
	"github.com/jmaslak/go-adhd-dash/internal/busy"
	"github.com/jmaslak/go-adhd-dash/internal/session"
	"github.com/jmaslak/go-adhd-dash/internal/tasks"
)

func main() {
	host := flag.String("host", "localhost", "address to listen on")
	port := flag.Int("port", 3270, "TCP port to listen on")
	refresh := flag.Duration("refresh", 10*time.Second, "how often an idle screen is redrawn")
	tasksDir := flag.String("tasks-dir", tasks.DefaultDir(), "task program directory")
	busyURL := flag.String("busy-url", "", "busy indicator status feed, e.g. ws://localhost:3334/feed (empty: none)")
	busyFile := flag.String("busy-file", "", "read the busy indicator's status from this JSON file instead of a feed")
	busyControl := flag.String("busy-control", "localhost:3333", "busy indicator UDP control port as host:port, for PF1 (busy) and PF2 (off) (empty: none)")
	calendars := flag.String("calendar", "", "comma-separated Google calendars for the agenda (empty: none)")
	calendarAliases := flag.String("calendar-alias", "", "comma-separated short names for the -calendar calendars, in the same order, shown in brackets before their events")
	agendaFile := flag.String("agenda-file", "", "read the agenda from this JSON file instead of Google Calendar")
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

	ctx := context.Background()
	cfg := session.Config{TasksDir: *tasksDir, Refresh: *refresh, AgendaRefresh: *agendaRefresh}

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

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go session.Handle(conn, cfg)
	}
}
