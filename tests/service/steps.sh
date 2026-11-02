#!/bin/sh
# Scenariusze skryptu usługi qnetspeed.sh, uruchamiane w chroot z BusyBox
# i atrapami QTS (scripts/test-service.sh). Każdy krok: komunikat, potem asercje.
# Pokrywa automatycznie część A1, A3, A11, A13, A15 (spec pkt 16.2).

ROOT=/share/CACHEDEV1_DATA/.qpkg/qnap-netspeed
SVC=/etc/init.d/qnetspeed.sh
CONF=/etc/config/qpkg.conf
CRONTAB=/etc/config/crontab
LOGTOOL=/var/log/log_tool.log
SPOOL=/var/spool/cron/crontabs/root
failures=0

ok() { echo "  ok: $1"; }
bad() {
	echo "  FAIL: $1"
	failures=$((failures + 1))
}
check() { # check DESCRIPTION COMMAND...
	d=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}
check_not() {
	d=$1
	shift
	if "$@" >/dev/null 2>&1; then bad "$d"; else ok "$d"; fi
}
step() { echo "== $*"; }

listening() {
	netstat -tln | awk -v p=":$1" '$1 ~ /^tcp/ && substr($4, length($4) - length(p) + 1) == p { f = 1 } END { exit !f }'
}
marker_lines() { grep -c ' # qnap-netspeed$' "$CRONTAB"; }
running_pid() { cat /var/run/qnetspeed.pid 2>/dev/null; }
is_alive() { [ -n "$1" ] && [ -d "/proc/$1" ] && ! grep -q '^State:[[:space:]]*Z' "/proc/$1/status"; }
expect_code() { # expect_code WANT DESCRIPTION COMMAND...
	want=$1 d=$2
	shift 2
	"$@" >/tmp/out 2>&1
	got=$?
	if [ "$got" = "$want" ]; then ok "$d (exit $got)"; else
		bad "$d: exit $got, want $want"
		sed 's/^/    | /' /tmp/out
	fi
}

step "start (A1)"
expect_code 0 "start" $SVC start
pid=$(running_pid)
check "pid file has a live process" is_alive "$pid"
check "listening on 8888" listening 8888
check "status page" sh -c 'wget -qO- http://127.0.0.1:8888/ | grep -q "Net Speed"'
check "status API" sh -c 'wget -qO- http://127.0.0.1:8888/api/v1/status | grep -q "\"name\":\"qnap-netspeed\""'
check "config created from default" test -f $ROOT/etc/qnetspeed.conf
check "data dir is 0750" sh -c "ls -ld $ROOT/data | grep -q '^drwxr-x---'"
check "/usr/bin/qnetspeed links to the package" sh -c "[ \"\$(readlink /usr/bin/qnetspeed)\" = $ROOT/bin/qnetspeed ]"
check "qnetspeed in PATH" sh -c 'qnetspeed version | grep -q "^qnetspeed "'
check "one cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 1 ]"
check "cron line content" grep -qxF "17 */6 * * * [ -x \"$ROOT/bin/qnetspeed\" ] && \"$ROOT/bin/qnetspeed\" cron >/dev/null 2>&1 # qnap-netspeed" $CRONTAB
check "other cron lines kept" grep -qxF "0 4 * * * /sbin/other_job # qnap-netspeed-other" $CRONTAB
check "crontab reloaded" cmp -s $CRONTAB $SPOOL
check "Web_Port in qpkg.conf" sh -c "[ \"\$(/sbin/getcfg qnap-netspeed Web_Port -f $CONF)\" = 8888 ]"
check "System Logs: started" grep -q '^-t 0 -a \[qnap-netspeed\] Started' $LOGTOOL

step "start again is idempotent"
rm -f $SPOOL
expect_code 0 "second start" $SVC start
check "same pid" sh -c "[ \"\$(cat /var/run/qnetspeed.pid)\" = $pid ]"
check "still one cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 1 ]"
check_not "crontab not reloaded again" test -e $SPOOL
expect_code 0 "status while running" $SVC status

step "cron entry runs"
line=$(grep ' # qnap-netspeed$' $CRONTAB | sed 's/^\([^ ]* \)\{5\}//')
expect_code 0 "cron command" sh -c "$line"
check "cron run logged" grep -q 'cron: run' $ROOT/data/qnetspeed.log

step "config change of port applies on restart"
$ROOT/bin/qnetspeed config set WEB_PORT 8899 >/dev/null
$ROOT/bin/qnetspeed config set CRON_SCHEDULE "5 * * * *" >/dev/null
expect_code 0 "restart" $SVC restart
check "old daemon gone" sh -c "! [ -d /proc/$pid ] || grep -q '^State:.*Z' /proc/$pid/status"
check "listening on 8899" listening 8899
check_not "not listening on 8888" listening 8888
check "Web_Port updated" sh -c "[ \"\$(/sbin/getcfg qnap-netspeed Web_Port -f $CONF)\" = 8899 ]"
check "cron schedule updated" grep -q '^5 \* \* \* \* .* # qnap-netspeed$' $CRONTAB
check "still one cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 1 ]"
$ROOT/bin/qnetspeed config set WEB_PORT 8888 >/dev/null
$ROOT/bin/qnetspeed config set CRON_SCHEDULE "17 */6 * * *" >/dev/null
expect_code 0 "restart back to 8888" $SVC restart

step "stop"
pid=$(running_pid)
expect_code 0 "stop" $SVC stop
check_not "process gone" is_alive "$pid"
check_not "pid file removed" test -e /var/run/qnetspeed.pid
check_not "not listening" listening 8888
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
check "other cron lines kept" grep -qxF "0 4 * * * /sbin/other_job # qnap-netspeed-other" $CRONTAB
check_not "link removed" test -e /usr/bin/qnetspeed
check "history and config kept" test -f $ROOT/etc/qnetspeed.conf
expect_code 3 "status when stopped" $SVC status
expect_code 0 "stop when stopped" $SVC stop

step "disabled package does not start (A3)"
expect_code 0 "disable" $SVC disable
expect_code 1 "start while disabled" $SVC start
check_not "not listening" listening 8888
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
check_not "no link" test -e /usr/bin/qnetspeed
expect_code 0 "enable" $SVC enable

step "port in use (A11)"
# Inny program na porcie 8888: drugi qnetspeed z innym katalogiem, poza paczką.
mkdir -p /t/other
QNETSPEED_ROOT=/t/other $ROOT/bin/qnetspeed daemon >/dev/null 2>&1 &
ncpid=$!
i=0
while ! listening 8888 && [ $i -lt 50 ]; do
	usleep 100000
	i=$((i + 1))
done
expect_code 1 "start with port taken" $SVC start
check "System Logs: port 8888" grep -q '^-t 2 -a \[qnap-netspeed\] Start failed: port 8888 is already in use' $LOGTOOL
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
kill $ncpid
wait $ncpid 2>/dev/null

step "port 5288 (IPERF_PORT) taken by another program does not block start (no iperf3 server before F4)"
mkdir -p /t/other5288/etc
echo 'WEB_PORT=5288' >/t/other5288/etc/qnetspeed.conf
echo 'IPERF_PORT=5289' >>/t/other5288/etc/qnetspeed.conf
QNETSPEED_ROOT=/t/other5288 $ROOT/bin/qnetspeed daemon >/dev/null 2>&1 &
otherpid=$!
i=0
while ! listening 5288 && [ $i -lt 50 ]; do
	usleep 100000
	i=$((i + 1))
done
check "5288 is taken" listening 5288
expect_code 0 "start with 5288 taken" $SVC start
check "listening on 8888" listening 8888
expect_code 0 "stop" $SVC stop
kill $otherpid
wait $otherpid 2>/dev/null

step "invalid configuration"
cp $ROOT/etc/qnetspeed.conf /tmp/conf.bak
echo 'WEB_PORT=80' >>$ROOT/etc/qnetspeed.conf
expect_code 1 "start with WEB_PORT=80" $SVC start
check "System Logs: invalid configuration" grep -q 'Start failed: invalid configuration:.*WEB_PORT' $LOGTOOL
mv /tmp/conf.bak $ROOT/etc/qnetspeed.conf

step "daemon that cannot listen is cleaned up"
cp $ROOT/etc/qnetspeed.conf /tmp/conf.bak
echo 'WEB_BIND=192.0.2.1' >>$ROOT/etc/qnetspeed.conf
expect_code 1 "start with unusable WEB_BIND" $SVC start
check "System Logs: not listening" grep -q 'Start failed: daemon is not listening on port 8888' $LOGTOOL
check_not "pid file removed" test -e /var/run/qnetspeed.pid
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
check_not "no link" test -e /usr/bin/qnetspeed
mv /tmp/conf.bak $ROOT/etc/qnetspeed.conf

step "foreign /usr/bin/qnetspeed is never touched"
echo 'not ours' >/usr/bin/qnetspeed
expect_code 0 "start" $SVC start
expect_code 0 "stop" $SVC stop
check "foreign file intact" grep -qx 'not ours' /usr/bin/qnetspeed
rm -f /usr/bin/qnetspeed

step "uninstall (A13)"
expect_code 0 "start" $SVC start
pid=$(running_pid)
# Same order as QDK's .uninstall.sh: stop + remove, PKG_PRE_REMOVE, delete, PKG_POST_REMOVE.
(
	# shellcheck disable=SC1091
	. /t/package_routines
	{
		echo '#!/bin/sh'
		echo "$SVC stop"
		echo "$SVC remove"
		echo "$PKG_PRE_REMOVE"
		echo "rm -rf $ROOT"
		echo "rm -f $SVC"
		echo "$PKG_POST_REMOVE"
	} >/tmp/uninstall.sh
)
# A crontab line left behind (e.g. package deleted without stop) must also go.
echo "17 */6 * * * [ -x \"$ROOT/bin/qnetspeed\" ] && \"$ROOT/bin/qnetspeed\" cron >/dev/null 2>&1 # qnap-netspeed" >>$CRONTAB
ln -sf $ROOT/bin/qnetspeed /usr/bin/qnetspeed
expect_code 0 "uninstall script" sh /tmp/uninstall.sh
check_not "process gone" is_alive "$pid"
check_not "package dir gone" test -e $ROOT
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
check "crontab reloaded" cmp -s $CRONTAB $SPOOL
check "other cron lines kept" grep -qxF "0 4 * * * /sbin/other_job # qnap-netspeed-other" $CRONTAB
check_not "no link" test -L /usr/bin/qnetspeed

step "nothing left in /tmp by the package (A15)"
check "no package files in /tmp" sh -c '! ls /tmp | grep -qv -e "^out$" -e "^uninstall.sh$"'

echo
if [ "$failures" -gt 0 ]; then
	echo "service tests: $failures FAILED"
	exit 1
fi
echo "service tests: all passed"
