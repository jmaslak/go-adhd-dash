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

    if [ \! -d tasks ] ; then
        mkdir tasks
    fi
    printf 'Title: Write the quarterly report\nTags: work\n' > tasks/1-report.task
    printf 'Title: Grocery shopping\nTags: personal\n' > tasks/2-shopping.task

    echo '{"status": "red", "minutes-to-next": 12}' > busy.json

    go build -o adhd-dash . && ./adhd-dash -agenda-file agenda.json -port 3299 -busy-file busy.json
}

doit "$@"

