#!/bin/sh
# Instalacja prawdziwego .qpkg instalatorem QDK w chroot z atrapami QTS
# (scripts/test-service.sh <qnetspeed> <.qpkg>). Pokrywa automatycznie część
# A1 (instalacja `sh plik.qpkg`), A12 (aktualizacja zachowuje konfigurację i dane)
# i A13 (odinstalowanie przez .uninstall.sh). Użycie: install.sh <ścieżka .qpkg>

QPKG=$1
ROOT=/share/CACHEDEV1_DATA/.qpkg/qnap-netspeed
CONF=/etc/config/qpkg.conf
CRONTAB=/etc/config/crontab
failures=0

ok() { echo "  ok: $1"; }
bad() {
	echo "  FAIL: $1"
	failures=$((failures + 1))
}
check() {
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
wait_listening() { # wait_listening PORT: up to 5 s (iperf3 is started by the daemon right after the web port)
	i=0
	while ! listening "$1" && [ $i -lt 50 ]; do
		usleep 100000
		i=$((i + 1))
	done
	listening "$1"
}
cfg() { /sbin/getcfg qnap-netspeed "$1" -f $CONF; }
install_qpkg() {
	# BusyBox 1.36 szuka pliku z `. qpkg.cfg` tylko w PATH; ash z QTS 1.24 także w bieżącym katalogu.
	PATH="$PATH:." sh "$QPKG" >/tmp/install.log 2>&1
	code=$?
	# Nagłówek QDK kończy się kodem 10 ("usuń plik .qpkg po instalacji") przy sukcesie.
	if [ "$code" = 10 ] && ! grep -q 'Installation Abort' /tmp/install.log; then ok "sh $(basename "$QPKG") (exit 10)"; else
		bad "sh $(basename "$QPKG"): exit $code"
		sed 's/^/    | /' /tmp/install.log
	fi
}

step "install: sh <file>.qpkg from a shared folder (A1)"
install_qpkg
check "package dir" test -d $ROOT
check "Install_Path registered" test "$(cfg Install_Path)" = $ROOT
check "Enable = TRUE" test "$(cfg Enable)" = TRUE
check "Version registered" test -n "$(cfg Version)"
check "Web_Port = 8888" test "$(cfg Web_Port)" = 8888
check "init.d link" test -x /etc/init.d/qnetspeed.sh
check "boot link QS190" sh -c 'ls /etc/rcS.d | grep -q "^QS190qnap-netspeed$"'
check "binaries installed" sh -c "[ -x $ROOT/bin/qnetspeed ] && [ -x $ROOT/bin/librespeed-cli ] && [ -x $ROOT/bin/iperf3 ]"
check "licenses installed" test -f $ROOT/licenses/librespeed-cli/LICENSE
check "icons installed" test -f /home/httpd/RSS/images/qnap-netspeed.gif
check "uninstall script" test -x $ROOT/.uninstall.sh
check "service started" listening 8888
check "iperf3 server listening on 5288 (real static iperf3)" wait_listening 5288
check "admin token is 0600" sh -c "ls -l $ROOT/data/web.token | grep -q '^-rw-------'"
check "status API" sh -c 'wget -qO- http://127.0.0.1:8888/api/v1/status | grep -q "\"name\":\"qnap-netspeed\""'
check "cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 1 ]"
check "qnetspeed in PATH" sh -c 'qnetspeed version | grep -q "^qnetspeed "'
check "config created" test -f $ROOT/etc/qnetspeed.conf
check "all package files owned by root" sh -c "[ -z \"\$(find $ROOT ! -user 0)\" ]"
check "no group/world-writable binaries or script" sh -c "[ -z \"\$(find $ROOT/bin $ROOT/qnetspeed.sh -perm -002 -o -perm -020)\" ]"
pid1=$(cat /var/run/qnetspeed.pid)

step "update keeps configuration and data (A12)"
$ROOT/bin/qnetspeed config set LANG pl >/dev/null
echo '{"probe":"kept"}' >$ROOT/data/wan.jsonl
install_qpkg
check "config kept" grep -qx 'LANG=pl' $ROOT/etc/qnetspeed.conf
check "data kept" grep -q kept $ROOT/data/wan.jsonl
check "service listening after update" listening 8888
check "iperf3 server listening again after the update" wait_listening 5288
check "daemon restarted by the update" test "$(cat /var/run/qnetspeed.pid)" != "$pid1"
check "Polish page after update" sh -c 'wget -qO- http://127.0.0.1:8888/ | grep -q "Usługa qnap-netspeed działa"'
check "still one cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 1 ]"

step "uninstall (A13)"
pid=$(cat /var/run/qnetspeed.pid)
sh $ROOT/.uninstall.sh >/tmp/uninstall.log 2>&1
code=$?
if [ "$code" = 0 ]; then ok ".uninstall.sh (exit 0)"; else
	bad ".uninstall.sh: exit $code"
	sed 's/^/    | /' /tmp/uninstall.log
fi
check_not "process gone" sh -c "[ -d /proc/$pid ] && ! grep -q '^State:.*Z' /proc/$pid/status"
check_not "not listening" listening 8888
check_not "iperf3 server gone" listening 5288
check_not "package dir gone" test -e $ROOT
check_not "init.d link gone" test -e /etc/init.d/qnetspeed.sh
check "no cron line" sh -c "[ \"\$(grep -c ' # qnap-netspeed\$' $CRONTAB)\" = 0 ]"
check "other cron lines kept" grep -qxF "0 4 * * * /sbin/other_job # qnap-netspeed-other" $CRONTAB
check_not "no link" test -e /usr/bin/qnetspeed

echo
if [ "$failures" -gt 0 ]; then
	echo "install tests: $failures FAILED"
	exit 1
fi
echo "install tests: all passed"
