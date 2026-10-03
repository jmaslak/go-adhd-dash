#!/bin/bash
set -euo pipefail

#
# Copyright (C) 2026 Joelle Maslak
# All Rights Reserved - See License
#

# Installs adhd-dash on Ubuntu as a systemd service, or reinstalls it (to
# upgrade it, or to repair the installation), restarting it:
#
#   - makes a system user (adhd-dash) whose home, /var/lib/adhd-dash, holds
#     the server's files: the users, checklists and audit log;
#   - installs the program as /usr/local/bin/adhd-dash, built here from this
#     checkout (with Go), or given ready built (--binary);
#   - writes /etc/default/adhd-dash, the server's options, the first time
#     only, so that changes made to it are kept;
#   - writes the udev rules that let that user open the Luxafor flag and the
#     Stream Deck Mini, after moving aside any rules for them that would
#     conflict;
#   - writes the systemd unit, and enables and (re)starts the service.
#
# Run it as root, from the checkout: sudo ./install-ubuntu.sh [options].

usage() {
    cat >&2 <<EOF
usage: sudo $0 [--binary PATH] [--user NAME] [--host ADDRESS] [--port PORT] [--http-port PORT]

  --binary PATH     install this adhd-dash, built for Linux (for example on
                    another machine: GOOS=linux GOARCH=amd64 go build), rather
                    than building it here with Go
  --user NAME       the system user the server runs as (default adhd-dash)
  --host ADDRESS    the address it listens on, the first time the options are
                    written (default localhost: only this machine can connect)
  --port PORT       its TN3270 port, likewise (default 3270)
  --http-port PORT  its web pages' port, likewise (default 3280)
EOF
    exit 2
}

binary=
svc_user=adhd-dash
host=localhost
port=3270
http_port=3280
while [ $# -gt 0 ]; do
    case $1 in
        --binary) binary=${2:?}; shift 2 ;;
        --user) svc_user=${2:?}; shift 2 ;;
        --host) host=${2:?}; shift 2 ;;
        --port) port=${2:?}; shift 2 ;;
        --http-port) http_port=${2:?}; shift 2 ;;
        -h | --help) usage ;;
        *) echo "$0: unknown option $1" >&2; usage ;;
    esac
done

say() { printf '\n==> %s\n' "$*"; }
die() { echo "$0: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run this as root: sudo $0"
command -v systemctl >/dev/null || die "systemd is needed"
command -v udevadm >/dev/null || die "udev is needed"

src=$(cd "$(dirname "$0")" && pwd)
home=/var/lib/$svc_user
prog=/usr/local/bin/adhd-dash
unit=/etc/systemd/system/adhd-dash.service
options=/etc/default/adhd-dash
rules=/etc/udev/rules.d/60-adhd-dash.rules
backup=/var/backups/adhd-dash-$(date +%Y%m%d-%H%M%S)

# A reinstall finds the service there already, and whether it is running.
reinstall=0
was_running=0
if [ -f "$unit" ] || systemctl cat adhd-dash >/dev/null 2>&1; then
    reinstall=1
    if systemctl is-active --quiet adhd-dash; then
        was_running=1
    fi
fi

# The devices' USB IDs: the Luxafor flag, and the Stream Deck Mini and Mini
# MK.2.
luxafor_vendor=04d8
luxafor_product=f372
deck_vendor=0fd9
deck_products="0063 0090"

# ---------------------------------------------------------------------------
say "Building adhd-dash"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if [ -n "$binary" ]; then
    [ -f "$binary" ] || die "no such file: $binary"
    cp "$binary" "$tmp/adhd-dash"
    echo "using $binary"
else
    go=$(command -v go || true)
    [ -n "$go" ] || [ ! -x /usr/local/go/bin/go ] || go=/usr/local/go/bin/go
    [ -n "$go" ] || die "Go is not installed here (it needs Go 1.27 or newer); install it, or build elsewhere and give --binary"
    # Built as whoever ran sudo, so that their Go caches stay theirs.
    builder=${SUDO_USER:-root}
    chown "$builder" "$tmp"
    (cd "$src" && sudo -u "$builder" env "PATH=$PATH" CGO_ENABLED=0 "$go" build -o "$tmp/adhd-dash" .)
    echo "built from $src with $("$go" version)"
fi
chmod 755 "$tmp/adhd-dash"
# A Linux program starts with the ELF magic number: 0x7f, then "ELF".
[ "$(dd if="$tmp/adhd-dash" bs=1 skip=1 count=3 2>/dev/null)" = ELF ] ||
    die "that is not a Linux program; build it with GOOS=linux"

# ---------------------------------------------------------------------------
say "Making the user $svc_user"

if id "$svc_user" >/dev/null 2>&1; then
    echo "$svc_user exists"
else
    useradd --system --user-group --home-dir "$home" --create-home \
        --shell /usr/sbin/nologin --comment "adhd-dash server" "$svc_user"
    echo "made $svc_user, home $home"
fi
install -d -m 750 -o "$svc_user" -g "$svc_user" "$home"

# ---------------------------------------------------------------------------
say "Installing $prog"

# Replaced by renaming the new one over it, which a running server does not
# notice: it goes on running the old one until it is restarted, once all
# else is in place, so that a failure on the way leaves it running.
install -m 755 "$tmp/adhd-dash" "$prog.new"
mv -f "$prog.new" "$prog"
if [ "$was_running" -eq 1 ]; then
    echo "installed; the running server is restarted on it below"
else
    echo "installed"
fi

# ---------------------------------------------------------------------------
say "Writing $options"

if [ -f "$options" ]; then
    echo "kept as it is (it exists)"
else
    cat >"$options" <<EOF
# adhd-dash's options, as its command line takes them (see adhd-dash -help,
# and the README). Its files are in $home, its user's home: the users
# (adhd-dash-users.json), checklists (adhd-dash-checklists.db) and audit
# log (adhd-dash-audit.log).
#
# -host localhost lets only this machine connect. To let terminals and a web
# server in front elsewhere connect, set it to this machine's address, or
# 0.0.0.0 for all of them, and firewall the ports.
#
# After a change: sudo systemctl restart adhd-dash
ADHD_DASH_OPTS="-host $host -port $port -http-port $http_port"
EOF
    chmod 644 "$options"
    echo "written"
fi

# ---------------------------------------------------------------------------
say "Writing the udev rules"

mkdir -p "$backup"
moved=0

# set_aside moves file into the backup directory if every rule in it is for
# the flag or the deck; otherwise it keeps a copy there and comments those
# rules out in place, so that the file's other rules still apply.
set_aside() {
    local file=$1 pattern=$2 others
    others=$(grep -Evi -e '^[[:space:]]*(#|$)' -e "$pattern" "$file" || true)
    if [ -z "$others" ]; then
        mv "$file" "$backup/"
        echo "moved $file to $backup"
    else
        cp -p "$file" "$backup/"
        sed -i -E "/$pattern/I s/^/# disabled by adhd-dash's installer: /" "$file"
        echo "commented out the flag's and deck's rules in $file (a copy is in $backup)"
    fi
    moved=1
}

# Any other rule for the flag or the deck: an old udev rule giving another
# group the device (busy-indicator's, or python-elgato-streamdeck's for the
# sd program), which would fight this one over its permissions.
device_pattern="($luxafor_vendor.*$luxafor_product|$luxafor_product.*$luxafor_vendor|$deck_vendor)"
for file in /etc/udev/rules.d/*.rules; do
    [ -f "$file" ] && [ "$file" != "$rules" ] || continue
    if grep -Eqi "$device_pattern" "$file"; then
        set_aside "$file" "$device_pattern"
    fi
done

# The Raku busy indicator's usbhid quirk unbinds the flag from usbhid, which
# leaves it no /dev/hidraw node for this server to open.
quirk_pattern="quirks=0x$luxafor_vendor:0x$luxafor_product"
quirk=0
for file in /etc/modprobe.d/*.conf; do
    [ -f "$file" ] || continue
    if grep -qi "$quirk_pattern" "$file"; then
        set_aside "$file" "$quirk_pattern"
        quirk=1
    fi
done
[ "$moved" -eq 1 ] || rmdir "$backup"
if [ "$quirk" -eq 1 ] && command -v update-initramfs >/dev/null; then
    # usbhid's options can be built into the initramfs too.
    update-initramfs -u
fi

{
    echo "# adhd-dash: lets its user ($svc_user) open the Luxafor flag and the"
    echo "# Stream Deck Mini, through their /dev/hidraw nodes. Written by"
    echo "# install-ubuntu.sh; it is rewritten when that is run again."
    echo "SUBSYSTEM==\"hidraw\", ATTRS{idVendor}==\"$luxafor_vendor\", ATTRS{idProduct}==\"$luxafor_product\", MODE=\"0660\", GROUP=\"$svc_user\""
    for product in $deck_products; do
        echo "SUBSYSTEM==\"hidraw\", ATTRS{idVendor}==\"$deck_vendor\", ATTRS{idProduct}==\"$product\", MODE=\"0660\", GROUP=\"$svc_user\""
    done
} >"$rules"
chmod 644 "$rules"
echo "wrote $rules"

udevadm control --reload-rules
udevadm trigger --subsystem-match=hidraw --action=change
echo "reloaded the rules, and applied them to the devices attached"

# ---------------------------------------------------------------------------
say "Writing $unit"

cat >"$unit" <<EOF
# adhd-dash, a dashboard for TN3270 terminals, driving the busy light.
# Written by install-ubuntu.sh; it is rewritten when that is run again. Its
# options are in $options.
[Unit]
Description=adhd-dash TN3270 dashboard and busy light
Documentation=file://$src/README.md
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$svc_user
Group=$svc_user
WorkingDirectory=$home
Environment=HOME=$home
EnvironmentFile=$options
ExecStart=$prog \$ADHD_DASH_OPTS
# Shutting it down from its admin menu ends it with status 0, and so stops
# it; it is restarted only after a failure.
Restart=on-failure
RestartSec=5

# Sandboxing: it writes only its home, and of the devices, sees only the
# HID ones (the flag and the deck), including those plugged in later.
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$home
PrivateTmp=yes
DevicePolicy=closed
DeviceAllow=char-hidraw rw
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
chmod 644 "$unit"
echo "written"

# ---------------------------------------------------------------------------
if [ "$reinstall" -eq 1 ]; then
    say "Restarting adhd-dash"
else
    say "Starting adhd-dash"
fi

systemctl daemon-reload
# Unmasked and enabled, in case it had been masked or disabled, and cleared
# of past failures, which could otherwise stop systemd restarting it.
systemctl unmask adhd-dash >/dev/null 2>&1 || true
systemctl enable adhd-dash >/dev/null
systemctl reset-failed adhd-dash >/dev/null 2>&1 || true
systemctl restart adhd-dash
sleep 2
if ! systemctl is-active --quiet adhd-dash; then
    systemctl status adhd-dash --no-pager || true
    die "adhd-dash did not start; see: journalctl -u adhd-dash"
fi
# The program it runs is the one just installed: a restarted server whose
# program was replaced shows it as deleted.
pid=$(systemctl show -p MainPID --value adhd-dash)
if [ -n "$pid" ] && [ "$pid" != 0 ] && [ "$(readlink "/proc/$pid/exe")" != "$prog" ]; then
    die "adhd-dash is running $(readlink "/proc/$pid/exe"), not $prog"
fi
case "$reinstall$was_running" in
    11) echo "restarted on the new program" ;;
    10) echo "started (it was not running)" ;;
    *) echo "started" ;;
esac

# ---------------------------------------------------------------------------
if [ "$reinstall" -eq 1 ]; then
    say "Reinstalled"
else
    say "Installed"
fi

journalctl -u adhd-dash --no-pager -n 10 --since "-1min" || true
cat <<EOF

adhd-dash runs as $svc_user, with its files in $home.

  status and log:  systemctl status adhd-dash;  journalctl -u adhd-dash -f
  options:         $options, then: sudo systemctl restart adhd-dash
  upgrade, repair: run this script again; it reinstalls and restarts it
EOF
if [ "$reinstall" -eq 0 ]; then
    cat <<EOF

A new server has one user, admin, whose password admin works only on the
console: connect from this machine as the CONSOLE LU (for example
c3270 CONSOLE@localhost:$port), and change it on the admin menu, option 4.
There, give the users who control the busy light Y under Flag.
EOF
fi
if [ "$quirk" -eq 1 ]; then
    cat <<EOF

The flag's usbhid quirk was removed: unplug the flag and plug it in again
(or reboot) for it to have a /dev/hidraw node.
EOF
fi
for prog_name in busy-indicator sd.py; do
    if pgrep -f "$prog_name" >/dev/null; then
        echo
        echo "Warning: $prog_name is running here; stop it, or it and adhd-dash will both drive the devices."
    fi
done
