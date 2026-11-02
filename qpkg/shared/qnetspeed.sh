#!/bin/sh
# qnap-netspeed: skrypt usługi QPKG (spec pkt 8.2).
# Wywołuje go QTS: start | stop | restart | status | enable | disable | remove.
# Tylko POSIX sh (BusyBox ash 1.24.1), polecenia QTS pełną ścieżką.

QPKG_NAME=qnap-netspeed
CONF=/etc/config/qpkg.conf
CRONTAB=/etc/config/crontab
CRON_MARK='# qnap-netspeed'
PIDFILE=/var/run/qnetspeed.pid
LINK=/usr/bin/qnetspeed
PATH=/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin
export PATH

# QTS uruchamia nas przez dowiązanie /etc/init.d/qnetspeed.sh, czasem z
# tymczasowego folderu, który potem usuwa: najpierw ustal SELF, potem cd /.
SELF=$(readlink -f "$0" 2>/dev/null)
[ -n "$SELF" ] || SELF=$0
cd / || exit 1

QPKG_ROOT=
BIN=
LOG=

# log LEVEL MESSAGE: System Logs (0 info, 1 warning, 2 error) and stdout.
log() {
	msg=$(printf '%s' "$2" | tr '\n' ' ' | cut -c1-180)
	[ -x /sbin/log_tool ] && /sbin/log_tool -t "$1" -a "[$QPKG_NAME] $msg" >/dev/null 2>&1
	echo "$QPKG_NAME: $msg"
}

# find_root sets QPKG_ROOT, BIN and LOG; returns 1 if the package is not registered.
find_root() {
	QPKG_ROOT=$(/sbin/getcfg "$QPKG_NAME" Install_Path -f "$CONF" 2>/dev/null)
	[ -n "$QPKG_ROOT" ] && [ -d "$QPKG_ROOT" ] || return 1
	BIN=$QPKG_ROOT/bin/qnetspeed
	LOG=$QPKG_ROOT/data/qnetspeed.log
	return 0
}

# alive PID: the process exists and is not a zombie.
alive() {
	[ -d "/proc/$1" ] || return 1
	! grep -q '^State:[[:space:]]*Z' "/proc/$1/status" 2>/dev/null
}

# daemon_pid prints the PID of our running daemon, or returns 1.
daemon_pid() {
	[ -f "$PIDFILE" ] || return 1
	pid=$(cat "$PIDFILE" 2>/dev/null)
	case "$pid" in '' | *[!0-9]*) return 1 ;; esac
	alive "$pid" || return 1
	exe=$(readlink "/proc/$pid/exe" 2>/dev/null)
	case "$exe" in
	*/qnap-netspeed/bin/qnetspeed | */qnap-netspeed/bin/qnetspeed' (deleted)') ;;
	*) return 1 ;;
	esac
	echo "$pid"
}

# port_listening PORT: something listens on TCP PORT (any address).
port_listening() {
	netstat -tln 2>/dev/null | awk -v p=":$1" '
		$1 ~ /^tcp/ && length($4) > length(p) && substr($4, length($4) - length(p) + 1) == p { found = 1 }
		END { exit !found }'
}

cron_line() {
	printf '%s [ -x "%s" ] && "%s" cron >/dev/null 2>&1 %s\n' "$1" "$BIN" "$BIN" "$CRON_MARK"
}

# cron_write FILE: install FILE as the crontab (same filesystem, then reload).
cron_write() {
	mv -f "$1" "$CRONTAB" && crontab "$CRONTAB"
}

cron_add() {
	line=$(cron_line "$1")
	if [ -f "$CRONTAB" ] && [ "$(grep -c " $CRON_MARK\$" "$CRONTAB")" = 1 ] && grep -qxF "$line" "$CRONTAB"; then
		return 0
	fi
	tmp=$CRONTAB.qnetspeed.tmp
	{
		[ -f "$CRONTAB" ] && grep -v " $CRON_MARK\$" "$CRONTAB"
		printf '%s\n' "$line"
	} >"$tmp" && cron_write "$tmp"
}

cron_remove() {
	[ -f "$CRONTAB" ] && grep -q " $CRON_MARK\$" "$CRONTAB" || return 0
	tmp=$CRONTAB.qnetspeed.tmp
	grep -v " $CRON_MARK\$" "$CRONTAB" >"$tmp"
	cron_write "$tmp"
}

# /usr/bin/qnetspeed: only ours, never over another program's file.
link_create() {
	if [ -L "$LINK" ]; then
		case "$(readlink "$LINK")" in
		"$BIN") return 0 ;;
		*/qnap-netspeed/bin/qnetspeed) ln -sf "$BIN" "$LINK" ;;
		*) echo "$QPKG_NAME: $LINK belongs to another program, not replaced" ;;
		esac
	elif [ -e "$LINK" ]; then
		echo "$QPKG_NAME: $LINK belongs to another program, not replaced"
	else
		ln -s "$BIN" "$LINK"
	fi
}

link_remove() {
	[ -L "$LINK" ] || return 0
	case "$(readlink "$LINK")" in
	*/qnap-netspeed/bin/qnetspeed) rm -f "$LINK" ;;
	esac
}

do_start() {
	if ! find_root; then
		log 2 "Install_Path not found in $CONF"
		exit 1
	fi
	if [ "$(/sbin/getcfg "$QPKG_NAME" Enable -u -d TRUE -f "$CONF")" = FALSE ]; then
		echo "$QPKG_NAME is disabled."
		exit 1
	fi
	if [ ! -x "$BIN" ]; then
		log 2 "Start failed: $BIN is missing"
		exit 1
	fi

	# data/ is readable by everyone so that `qnetspeed history` works without root
	# (spec pkt 6); it holds results only, secrets stay in etc/.
	mkdir -p "$QPKG_ROOT/data" && chmod 0755 "$QPKG_ROOT/data"
	for f in "$QPKG_ROOT"/data/*.jsonl; do
		[ -f "$f" ] && chmod 0644 "$f"
	done
	if [ ! -f "$QPKG_ROOT/etc/qnetspeed.conf" ] && [ -f "$QPKG_ROOT/etc/qnetspeed.conf.default" ]; then
		cp "$QPKG_ROOT/etc/qnetspeed.conf.default" "$QPKG_ROOT/etc/qnetspeed.conf"
	fi
	if ! out=$("$BIN" config check 2>&1); then
		log 2 "Start failed: invalid configuration: $(printf '%s\n' "$out" | grep 'error' | head -n 2)"
		exit 1
	fi
	if ! web_port=$("$BIN" config get WEB_PORT) ||
		! schedule=$("$BIN" config get CRON_SCHEDULE); then
		log 2 "Start failed: cannot read configuration"
		exit 1
	fi

	if pid=$(daemon_pid); then
		/sbin/setcfg "$QPKG_NAME" Web_Port "$web_port" -f "$CONF"
		link_create
		cron_add "$schedule"
		echo "$QPKG_NAME is already running (pid $pid)."
		exit 0
	fi
	rm -f "$PIDFILE"

	# Tylko WEB_PORT: serwer iperf3 (IPERF_PORT) uruchamia demon dopiero od F4 i wtedy
	# dojdzie tu kontrola IPERF_PORT przy LAN_SERVER_ENABLE=1.
	if port_listening "$web_port"; then
		log 2 "Start failed: port $web_port is already in use by another program"
		exit 1
	fi

	/sbin/setcfg "$QPKG_NAME" Web_Port "$web_port" -f "$CONF"
	link_create
	cron_add "$schedule"

	(trap '' HUP && exec "$BIN" daemon </dev/null >>"$LOG" 2>&1) &
	pid=$!
	echo "$pid" >"$PIDFILE"

	i=0
	while [ "$i" -lt 10 ]; do
		if port_listening "$web_port"; then
			log 0 "Started (pid $pid, port $web_port)."
			exit 0
		fi
		alive "$pid" || break
		sleep 1
		i=$((i + 1))
	done
	log 2 "Start failed: daemon is not listening on port $web_port, see $LOG"
	alive "$pid" && kill -KILL "$pid" 2>/dev/null
	rm -f "$PIDFILE"
	cron_remove
	link_remove
	exit 1
}

do_stop() {
	find_root
	if pid=$(daemon_pid); then
		kill -TERM "$pid" 2>/dev/null
		i=0
		while alive "$pid" && [ "$i" -lt 20 ]; do
			sleep 1
			i=$((i + 1))
		done
		alive "$pid" && kill -KILL "$pid" 2>/dev/null
		log 0 "Stopped."
	fi
	rm -f "$PIDFILE"
	cron_remove
	link_remove
	return 0
}

case "$1" in
start)
	do_start
	;;
stop)
	do_stop
	;;
restart)
	"$SELF" stop || exit 1
	"$SELF" start
	exit $?
	;;
status)
	if pid=$(daemon_pid); then
		echo "$QPKG_NAME is running (pid $pid)."
		exit 0
	fi
	echo "$QPKG_NAME is stopped."
	exit 3
	;;
enable)
	/sbin/setcfg "$QPKG_NAME" Enable TRUE -f "$CONF"
	;;
disable)
	/sbin/setcfg "$QPKG_NAME" Enable FALSE -f "$CONF"
	;;
remove)
	cron_remove
	link_remove
	;;
*)
	echo "Usage: $0 {start|stop|restart|status|enable|disable|remove}"
	exit 1
	;;
esac

exit 0
