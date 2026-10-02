#!/bin/bash
set -e

#
# Copyright (C) 2026 Joelle Maslak
# All Rights Reserved - See License
#

doit() {
    python3 - <<'EOF'
import json, datetime as dt
now = dt.datetime.now().astimezone().replace(second=0, microsecond=0)
today = now.replace(hour=0, minute=0)
def ev(summary, start, mins, cal, all_day=False):
    return {"summary": summary, "start": start.isoformat(),
            "end": (start + dt.timedelta(minutes=mins)).isoformat(),
            "all_day": all_day, "calendar": cal}
json.dump([
    ev("Standup", now - dt.timedelta(minutes=10), 30, "work"),   # NOW
    ev("1:1 with manager", now + dt.timedelta(minutes=12), 30, "work"),
    ev("Dentist", now + dt.timedelta(hours=3), 60, "home"),
    ev("Planning", now + dt.timedelta(hours=20), 60, "work"),
], open("agenda.json", "w"), indent=1)
EOF

    # The busy light is decided in the server, from the agenda file for
    # the users marked Flag; the real flag and Stream Deck are left alone.
    go build -o adhd-dash . && ./adhd-dash -agenda-file agenda.json -port 3299 -flag=false -streamdeck=false
}

doit "$@"

