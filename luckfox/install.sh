#!/bin/sh
# Installs ad-mute on the Luckfox board from the folder that holds this
# script, then restarts it. Deploy-Luckfox.ps1 copies the folder to the
# board and runs this script. You can also copy the folder by hand and run:
#   sh install.sh
#
# The folder must hold these files: ad-mute (the build for this board),
# ad-mute.conf, S99ad-mute and ad-mute.service.
#
# The script never changes /etc/ad-mute. That folder holds the ADB key that
# the TV trusts, so the TV does not ask to allow the board again.
set -e
cd "$(dirname "$0")"

for f in ad-mute ad-mute.conf S99ad-mute ad-mute.service; do
    if [ ! -f "$f" ]; then
        echo "install.sh: the file $f is missing" >&2
        exit 1
    fi
done

# Files from a Windows checkout can have CRLF line ends, which break sh.
lf() { tr -d '\r' < "$1" > "$2"; }

if [ -d /run/systemd/system ]; then init=systemd; else init=sysv; fi

echo "Stopping ad-mute"
if [ "$init" = systemd ]; then
    systemctl stop ad-mute 2>/dev/null || true
elif [ -x /etc/init.d/S99ad-mute ]; then
    /etc/init.d/S99ad-mute stop || true
    sleep 1
fi

# A rename replaces the program in one step, even if an old copy still runs.
echo "Installing /usr/bin/ad-mute"
cp ad-mute /usr/bin/ad-mute.new
chmod 755 /usr/bin/ad-mute.new
mv -f /usr/bin/ad-mute.new /usr/bin/ad-mute

echo "Installing /etc/ad-mute.conf"
lf ad-mute.conf /etc/ad-mute.conf

if [ "$init" = systemd ]; then
    echo "Installing /etc/systemd/system/ad-mute.service"
    lf ad-mute.service /etc/systemd/system/ad-mute.service
    systemctl daemon-reload
    systemctl enable ad-mute
    systemctl restart ad-mute
    sleep 3
    echo "--- Last log lines"
    journalctl -u ad-mute -n 5 --no-pager || true
    if ! systemctl is-active --quiet ad-mute; then
        echo "install.sh: ad-mute did not start" >&2
        exit 1
    fi
else
    echo "Installing /etc/init.d/S99ad-mute"
    lf S99ad-mute /etc/init.d/S99ad-mute
    chmod 755 /etc/init.d/S99ad-mute
    echo "--- Deployed $(date)" >> /tmp/ad-mute.log
    /etc/init.d/S99ad-mute start
    sleep 3
    echo "--- Last log lines"
    tail -n 5 /tmp/ad-mute.log
    if ! pidof ad-mute > /dev/null; then
        echo "install.sh: ad-mute did not start" >&2
        exit 1
    fi
fi
echo "ad-mute is running"
