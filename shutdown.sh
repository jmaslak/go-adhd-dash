#!/bin/bash
set -e

#
# Copyright (C) 2026 Joelle Maslak
# All Rights Reserved - See License
#

# Shuts down an adhd-dash server by driving s3270 through its screens: log
# in, type admin on the dashboard, option 1 on the admin menu, then PF4 on
# the confirmation. Each screen is checked before a key is pressed on it;
# anything unexpected stops the script, showing the screen.
#
# The user name and password come from ADHD_USER and ADHD_PASSWORD
# (default admin and admin). The server refuses admin with the password
# admin at its login screen, so with those, use -c: it connects as the
# CONSOLE LU, which is logged in without a password, and works only from
# the server's own machine, with no other console connected.

usage() {
    echo "usage: $0 [-c] [-h host] [-p port]" >&2
    exit 2
}

host=${ADHD_HOST-127.0.0.1}
port=3270
console=
while getopts ch:p: opt; do
    case $opt in
        c) console=1 ;;
        h) host=$OPTARG ;;
        p) port=$OPTARG ;;
        *) usage ;;
    esac
done
shift $((OPTIND - 1))
[ $# -eq 0 ] || usage

user=${ADHD_USER-admin}
password=${ADHD_PASSWORD-admin}

# s3270 reads actions on one FIFO and answers on the other: data: lines,
# a status line, then ok or error.
dir=$(mktemp -d)
mkfifo "$dir/in" "$dir/out"
s3270 <"$dir/in" >"$dir/out" &
pid=$!
exec 3>"$dir/in" 4<"$dir/out"
trap 'status=$?; set +e; exec 3>&- 4<&-; kill $pid 2>/dev/null; wait $pid 2>/dev/null; rm -rf "$dir"; exit $status' EXIT

# run sends one action, leaving its data: lines in out. It fails if the
# action does, or s3270 does not answer within a minute.
out=()
run() {
    local line
    out=()
    printf '%s\n' "$1" >&3
    while IFS= read -r -t 60 line <&4; do
        case $line in
            ok) return 0 ;;
            error)
                echo "$0: $1 failed: ${out[*]}" >&2
                return 1
                ;;
            data:*) out+=("${line#data: }") ;;
        esac
    done
    echo "$0: no answer from s3270 to $1" >&2
    return 1
}

# typetext types s where the cursor is. String() takes a backslash as the
# start of an escape (\n is Enter, \t Tab), and a quoted argument ending in
# one as an escaped quote, so each backslash is typed on its own and only
# the text between them goes through String(), its quotes escaped.
typetext() {
    local s=$1 chunk
    while :; do
        chunk=${s%%\\*}
        [ -z "$chunk" ] || run "String(\"${chunk//\"/\\\"}\")"
        [ "$chunk" != "$s" ] || return 0
        run 'Key(backslash)'
        s=${s#*\\}
    done
}

# expect fails, showing the screen, unless it holds text.
expect() {
    run 'Ascii()'
    local row
    for row in "${out[@]}"; do
        case $row in
            *"$1"*) return 0 ;;
        esac
    done
    echo "$0: expected \"$1\" on the screen, which is:" >&2
    printf '%s\n' "${out[@]}" >&2
    return 1
}

# key presses a key and waits for the server's next screen.
key() {
    run "$1"
    run 'Wait(30,InputField)'
}

if [ -n "$console" ]; then
    run "Connect(CONSOLE@$host:$port)"
    run 'Wait(30,InputField)'
else
    run "Connect($host:$port)"
    run 'Wait(30,InputField)'
    expect 'Log in to continue.'
    typetext "$user"
    run 'Tab()'
    typetext "$password"
    key 'Enter()'
fi

expect 'PF3=Exit'
run 'String("admin")'
key 'Enter()'

expect 'ADMIN MENU'
run 'String("1")'
key 'Enter()'

expect 'Shut down the server?'
run 'PF(4)'
run 'Wait(30,Disconnect)'
echo "shutdown requested; the server has disconnected this session"
