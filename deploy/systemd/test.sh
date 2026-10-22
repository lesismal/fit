#!/bin/sh
# test.sh installs fit with these units as deploy/README.md says and checks
# them: fit serves port 80 as the user fit, takes a changed config on
# `systemctl reload`, refuses an invalid one there while it keeps serving,
# and fit@.service runs a second instance. CI runs it as root on a throwaway
# runner: it installs to /usr/local/bin, /etc and /usr/lib, and leaves the
# user fit behind.
#
# Usage: sudo deploy/systemd/test.sh <fit binary>
set -eu
here=$(cd "$(dirname "$0")" && pwd)
install -m 0755 "$1" /usr/local/bin/fit
install -m 0644 "$here/fit.service" "$here/fit@.service" /etc/systemd/system/
install -m 0644 "$here/fit.sysusers" /usr/lib/sysusers.d/fit.conf
systemd-sysusers
mkdir -p /etc/fit
cleanup() {
	status=$?
	[ "$status" -eq 0 ] || journalctl -u fit -u fit@second --no-pager -n 50 || true
	systemctl stop fit fit@second || true
	rm -f /etc/systemd/system/fit.service /etc/systemd/system/fit@.service /etc/fit/fit.json /etc/fit/second.json
	systemctl daemon-reload
}
trap cleanup EXIT

# config writes a config to $1 serving body $3 on port $2.
config() {
	cat > "$1" <<END
{
  "listeners": [{"name": "web", "address": ":$2"}],
  "routes": [{"name": "hello", "handler": {"module": "respond", "params": {"body": "$3"}}}]
}
END
}

# expect waits up to 10s for port $1 to answer body $2.
expect() {
	for _ in $(seq 20); do
		[ "$(curl -s "http://127.0.0.1:$1/")" = "$2" ] && return 0
		sleep 0.5
	done
	echo "port $1 never answered $2; last: $(curl -s "http://127.0.0.1:$1/")" >&2
	return 1
}

systemd-analyze verify /etc/systemd/system/fit.service /etc/systemd/system/fit@second.service
config /etc/fit/fit.json 80 one
systemctl daemon-reload
systemctl start fit
expect 80 one
[ "$(ps -o user= -p "$(systemctl show -p MainPID --value fit)")" = fit ] || { echo "fit does not run as fit" >&2; exit 1; }

config /etc/fit/fit.json 80 two
systemctl reload fit
expect 80 two

echo '{"listeners": []}' > /etc/fit/fit.json
if systemctl reload fit 2>/dev/null; then echo "an invalid config reloaded" >&2; exit 1; fi
expect 80 two
systemctl is-active fit

config /etc/fit/second.json 8081 second
systemctl start fit@second
expect 8081 second
[ -d /var/lib/fit/second ] || { echo "no state directory for fit@second" >&2; exit 1; }
echo "systemd units: ok"
