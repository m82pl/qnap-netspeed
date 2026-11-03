#!/bin/sh
# Test integracyjny na Linuksie runnera CI (plan: "Test integracyjny na Linuksie runnera"):
# prawdziwy statyczny iperf3 i qnetspeed z katalogu <bindir>, serwer iperf3 na loopbacku.
#  1. Przechwytuje prawdziwe wyjście `iperf3 -J` (TCP w obie strony, --bidir, UDP, błąd) do
#     <katalog_przechwyceń> i wypisuje je w logu (bez `intervals`). Z tego powstał testdata/iperf3/3.22/.
#  2. Sprawdza `qnetspeed lan-test` (zwykły, --bidir, --udp) na prawdziwym iperf3.
#  3. Sprawdza `qnetspeed cron` z LAN_SCHEDULE_TARGETS=127.0.0.1: wpis do System Logs raz,
#     bez powtórzenia przy następnym przebiegu, informacja o powrocie do normy.
#  4. Uruchamia prawdziwego demona (`qnetspeed daemon`) z nadzorowanym iperf3 -s i sprawdza
#     REST API, token, test LAN przez API, test z przeglądarki (/api/st), lan-server i zamknięcie.
# /sbin/log_tool jest atrapą zapisującą argumenty (instalowaną tylko na runnerze CI).
# Uruchamiać jako root: sudo sh scripts/integration-test.sh out captures
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 2; }
[ "${CI:-}" = true ] || { echo "refusing to install a fake /sbin/log_tool outside CI" >&2; exit 2; }
[ ! -e /sbin/log_tool ] || { echo "/sbin/log_tool exists, not replacing it" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 2; }

src="$(cd "$1" && pwd)"
cap="$(mkdir -p "$2" && cd "$2" && pwd)"
root="$(mktemp -d)"
port=5288
calls="$root/log_tool.calls"

cleanup() {
	[ -f "$root/iperf3.pid" ] && kill "$(cat "$root/iperf3.pid")" 2>/dev/null
	[ -n "${daemon_pid:-}" ] && kill "$daemon_pid" 2>/dev/null
	rm -f /sbin/log_tool
	rm -rf "$root"
}
trap cleanup EXIT

mkdir -p "$root/bin" "$root/etc" "$root/data"
cp "$src/qnetspeed" "$src/iperf3" "$root/bin/"
chmod 0755 "$root/bin/qnetspeed" "$root/bin/iperf3"
q="$root/bin/qnetspeed"
ip3="$root/bin/iperf3"

cat >/sbin/log_tool <<LT
#!/bin/sh
echo "\$*" >>"$calls"
LT
chmod 0755 /sbin/log_tool
: >"$calls"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}
# expect_json NAME FILTER: the jq filter must be true for NAME's output.
expect_json() {
	printf '%s' "$2" | jq -e "$3" >/dev/null || fail "$1: jq '$3' is false for: $2"
	echo "  ok: $1"
}

"$ip3" -s -p "$port" -D -I "$root/iperf3.pid"
sleep 1

echo "== 1. iperf3 -J captures (loopback, iperf3 $("$ip3" --version | head -n1))"
capture() {
	name="$1"
	shift
	echo "::group::capture $name: iperf3 $*"
	set +e
	"$ip3" -J "$@" >"$cap/$name.raw.json" 2>"$cap/$name.stderr"
	echo "$?" >"$cap/$name.exit"
	set -e
	jq -c 'del(.intervals)' "$cap/$name.raw.json" >"$cap/$name.json" || cp "$cap/$name.raw.json" "$cap/$name.json"
	echo "--- exit: $(cat "$cap/$name.exit")"
	echo "--- stdout (without intervals):"
	cat "$cap/$name.json"
	echo "--- stderr:"
	cat "$cap/$name.stderr"
	echo "::endgroup::"
}
capture tcp-upload -c 127.0.0.1 -p "$port" -t 2 -P 4
capture tcp-download -c 127.0.0.1 -p "$port" -t 2 -P 4 -R
capture tcp-bidir -c 127.0.0.1 -p "$port" -t 2 -P 4 --bidir
capture udp -c 127.0.0.1 -p "$port" -t 2 -u -b 100M
capture error-refused -c 127.0.0.1 -p 1 -t 2

expect_json "tcp upload has end.sum_received" "$(cat "$cap/tcp-upload.json")" '.end.sum_received.bits_per_second > 0 and (.end.sum_sent.retransmits != null) and .end.cpu_utilization_percent.host_total != null'
expect_json "tcp download has end.sum_received" "$(cat "$cap/tcp-download.json")" '.end.sum_received.bits_per_second > 0'
expect_json "bidir has sum_received_bidir_reverse" "$(cat "$cap/tcp-bidir.json")" '.end.sum_received.bits_per_second > 0 and .end.sum_received_bidir_reverse.bits_per_second > 0'
expect_json "udp has end.sum with jitter" "$(cat "$cap/udp.json")" '.end.sum.jitter_ms != null and .end.sum.lost_percent != null'
expect_json "refused has error" "$(cat "$cap/error-refused.json")" '.error != null'

echo "== 2. qnetspeed lan-test against the real iperf3"
out=$("$q" lan-test 127.0.0.1 --time 2 --json) || fail "lan-test exit code $?"
expect_json "lan-test tcp" "$out" '.status == "ok" and .results.upload_mbps > 0 and .results.download_mbps > 0 and .params.protocol == "tcp"'
out=$("$q" lan-test 127.0.0.1 --time 2 --bidir --json) || fail "lan-test --bidir exit code $?"
expect_json "lan-test --bidir" "$out" '.status == "ok" and .results.upload_mbps > 0 and .results.download_mbps > 0 and .params.bidir == true'
out=$("$q" lan-test 127.0.0.1 --time 2 --udp --bitrate 100M --json) || fail "lan-test --udp exit code $?"
expect_json "lan-test --udp" "$out" '.status == "ok" and .results.jitter_ms != null and .params.protocol == "tcp+udp"'
echo "== 3. qnetspeed cron: System Logs entry once, then back to normal"
"$q" config set WAN_SCHEDULE_ENABLE 0 >/dev/null
"$q" config set LAN_TEST_DURATION 2 >/dev/null
"$q" config set LAN_SCHEDULE_TARGETS 127.0.0.1 >/dev/null
"$q" config set MIN_LAN_MBPS 1000000 >/dev/null # 1 Tb/s: loopback never gets there
: >"$calls"
"$q" cron || fail "cron exit code $?"
[ "$(wc -l <"$calls")" = 1 ] || fail "expected one log_tool call, got: $(cat "$calls")"
grep -q '^-t 1 -a \[qnap-netspeed\] LAN 127.0.0.1: upload .* Mbps < 1000000 Mbps$' "$calls" || fail "unexpected entry: $(cat "$calls")"
echo "  ok: warning written once: $(cat "$calls")"
"$q" cron || fail "second cron exit code $?"
[ "$(wc -l <"$calls")" = 1 ] || fail "the warning was repeated at the next run: $(cat "$calls")"
echo "  ok: not repeated at the next run"
"$q" notify status | grep -q 'lan_min:127.0.0.1' || fail "notify status does not list lan_min"
"$q" config set MIN_LAN_MBPS 0 >/dev/null
"$q" cron || fail "third cron exit code $?"
[ "$(wc -l <"$calls")" = 2 ] || fail "expected a recovery entry: $(cat "$calls")"
tail -n1 "$calls" | grep -q '^-t 0 -a \[qnap-netspeed\] LAN 127.0.0.1: back to normal' || fail "unexpected recovery: $(tail -n1 "$calls")"
echo "  ok: recovery written: $(tail -n1 "$calls")"
"$q" notify status | grep -q 'No open problems' || fail "problems still open after recovery"
[ "$(wc -l <"$root/data/link.jsonl")" = 3 ] || fail "link.jsonl should have one record per cron run"
[ "$(wc -l <"$root/data/lan.jsonl")" -ge 6 ] || fail "lan.jsonl should have the 3 manual and 3 scheduled tests"

echo "== 4. qnetspeed daemon: API, token, supervised iperf3 server, browser test"
web=8890
ip_port=5290
"$q" config set WEB_BIND 127.0.0.1 >/dev/null
"$q" config set WEB_PORT "$web" >/dev/null
"$q" config set IPERF_PORT "$ip_port" >/dev/null
"$q" config set LAN_SCHEDULE_TARGETS "" >/dev/null
"$q" daemon >"$root/data/qnetspeed.log" 2>&1 &
daemon_pid=$!
base="http://127.0.0.1:$web"
i=0
until curl -fsS "$base/api/v1/status" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 100 ] || { cat "$root/data/qnetspeed.log"; fail "the daemon does not answer"; }
	sleep 0.1
done
token=$(cat "$root/data/web.token")
[ "$("$q" web-token)" = "$token" ] || fail "web-token differs from data/web.token"
[ "$(stat -c %a "$root/data/web.token")" = 600 ] || fail "web.token is not 0600"
api() { # api METHOD PATH [JSON]: admin call, prints the body
	if [ $# -ge 3 ]; then
		curl -sS -X "$1" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' --data "$3" "$base$2"
	else
		curl -sS -X "$1" -H "Authorization: Bearer $token" "$base$2"
	fi
}
code() { # code METHOD PATH [extra curl args]: HTTP status only
	m=$1 p=$2
	shift 2
	curl -sS -o /dev/null -w '%{http_code}' -X "$m" "$@" "$base$p"
}
wait_iperf() { # wait_iperf STATE: up to 10 s
	i=0
	while [ "$(curl -sS "$base/api/v1/status" | jq -r .lan_server.state)" != "$1" ]; do
		i=$((i + 1))
		[ "$i" -lt 100 ] || fail "iperf3 server did not reach state $1: $(curl -sS "$base/api/v1/status")"
		sleep 0.1
	done
}

wait_iperf running
out=$(curl -sS "$base/api/v1/status")
expect_json "status: version, lan_server on IPERF_PORT, no test" "$out" '.name == "qnap-netspeed" and .lan_server.port == '"$ip_port"' and .lan_server.pid > 0 and .test_running == null and (.warnings | type) == "array"'

# A8: another device runs `iperf3 -c <NAS>` against the server of the package.
out=$("$ip3" -c 127.0.0.1 -p "$ip_port" -t 2 -J) || fail "iperf3 -c against the supervised server failed"
expect_json "A8: iperf3 -c against the supervised server" "$out" '.end.sum_received.bits_per_second > 0'

# Authorization.
[ "$(code POST /api/v1/lan-server --data '{"action":"stop"}')" = 401 ] || fail "lan-server without a token was not refused"
[ "$(code GET /api/v1/config)" = 401 ] || fail "config without a token was not refused"
[ "$(code GET /api/v1/status)" = 200 ] || fail "status is public (WEB_PUBLIC_READ=1)"
echo "  ok: admin endpoints refuse requests without a token"

# Supervision: kill -9 of iperf3 brings it back.
pid1=$(curl -sS "$base/api/v1/status" | jq -r .lan_server.pid)
kill -9 "$pid1"
i=0
while :; do
	st=$(curl -sS "$base/api/v1/status" | jq -c .lan_server)
	pid2=$(printf '%s' "$st" | jq -r .pid)
	[ "$(printf '%s' "$st" | jq -r .state)" = running ] && [ "$pid2" != "$pid1" ] && [ "$pid2" != null ] && break
	i=$((i + 1))
	[ "$i" -lt 150 ] || fail "iperf3 was not restarted after kill -9: $st"
	sleep 0.1
done
expect_json "iperf3 restarted after kill -9" "$st" '.restarts >= 1'
"$ip3" -c 127.0.0.1 -p "$ip_port" -t 1 -J | jq -e '.end.sum_received.bits_per_second > 0' >/dev/null || fail "restarted server does not work"

# lan-server stop / start from the CLI (through the daemon).
"$q" lan-server stop | grep -q stopped || fail "lan-server stop"
wait_iperf stopped
if "$ip3" -c 127.0.0.1 -p "$ip_port" -t 1 >/dev/null 2>&1; then fail "iperf3 server still answers after stop"; fi
"$q" lan-server status >/dev/null && fail "lan-server status should exit 1 when stopped"
"$q" lan-server start >/dev/null || fail "lan-server start"
wait_iperf running
out=$("$q" lan-server status --json)
expect_json "lan-server status --json" "$out" '.state == "running" and .port == '"$ip_port"
echo "  ok: lan-server stop/start"

# LAN test through the API against the server of this very daemon; a second test and a
# browser stream are refused with 409 while it runs.
out=$(api POST /api/v1/tests/lan '{"target":"127.0.0.1","time":2,"parallel":2}')
expect_json "POST /tests/lan -> 202 body" "$out" '.test_id != null and .state == "running"'
test_id=$(printf '%s' "$out" | jq -r .test_id)
[ "$(code POST /api/v1/tests/lan -H "Authorization: Bearer $token" --data '{"target":"127.0.0.1"}')" = 409 ] || fail "a second test was not refused"
[ "$(code GET '/api/st/download?mb=1')" = 409 ] || fail "browser download during a test was not refused"
out=$(curl -sS "$base/api/v1/status")
expect_json "status shows the running test" "$out" '.test_running.type == "lan-test"'
i=0
while :; do
	out=$(curl -sS "$base/api/v1/tests/$test_id")
	[ "$(printf '%s' "$out" | jq -r .state)" != running ] && break
	i=$((i + 1))
	[ "$i" -lt 300 ] || fail "API test did not finish: $out"
	sleep 0.1
done
expect_json "API LAN test finished" "$out" '.state == "done" and .results[0].status == "ok" and .results[0].trigger == "web" and .results[0].results.upload_mbps > 0 and .results[0].results.download_mbps > 0'

# Browser test endpoints.
[ "$(code GET /api/st/ping)" = 204 ] || fail "ping"
size=$(curl -sS -o /dev/null -w '%{size_download}' "$base/api/st/download?mb=50")
[ "$size" = 52428800 ] || fail "download of 50 MiB returned $size bytes"
out=$(head -c 10485760 /dev/zero | curl -sS --data-binary @- -H 'Content-Type: application/octet-stream' "$base/api/st/upload")
expect_json "upload of 10 MiB" "$out" '.bytes == 10485760 and .seconds >= 0'
out=$(curl -sS -A 'Mozilla/5.0 (integration test)' -H 'Content-Type: application/json' \
	--data '{"download_mbps":812.5,"upload_mbps":300.25,"ping_ms":2.5,"jitter_ms":0.4,"duration_s":10,"parallel":4}' "$base/api/st/result")
expect_json "POST /api/st/result" "$out" '.id != null'
tail -n1 "$root/data/lan.jsonl" | jq -e '.type == "LAN_BROWSER" and .trigger == "web" and .client_ip == "127.0.0.1" and .results.download_mbps == 812.5 and (.user_agent | startswith("Mozilla"))' >/dev/null || fail "LAN_BROWSER record: $(tail -n1 "$root/data/lan.jsonl")"
echo "  ok: LAN_BROWSER record written"
# The lock is let go shortly after the last stream (cron and API tests can run again).
i=0
while [ "$(curl -sS "$base/api/v1/status" | jq -r .test_running.type)" != null ]; do
	i=$((i + 1))
	[ "$i" -lt 100 ] || fail "browser test still holds the lock"
	sleep 0.1
done
echo "  ok: lock released after the browser test"

# History, config, link, routes.
out=$(curl -sS "$base/api/v1/history?type=lan&last=10")
expect_json "history (lan)" "$out" '(.records | length) >= 2 and ([.records[].type] | index("LAN_BROWSER")) != null'
curl -sS "$base/api/v1/history?type=lan&format=csv" | head -n1 | grep -q '^timestamp,type,target' || fail "history csv header"
expect_json "link" "$(curl -sS "$base/api/v1/link")" '.type == "LINK" and (.interfaces | length) > 0'
expect_json "routes" "$(curl -sS "$base/api/v1/routes")" 'type == "array"'
out=$(api PUT /api/v1/config '{"values":{"MIN_WAN_DL_MBPS":"123"}}')
expect_json "PUT config" "$out" '.saved == ["MIN_WAN_DL_MBPS"] and .restart_required == []'
[ "$("$q" config get MIN_WAN_DL_MBPS)" = 123 ] || fail "config file not changed by PUT"
out=$(api PUT /api/v1/config '{"values":{"WEB_PORT":"80","NOPE":"1"}}')
expect_json "PUT config with errors saves nothing" "$out" '.error == "invalid_config" and (.fields | keys) == ["NOPE","WEB_PORT"]'
[ "$("$q" config get WEB_PORT)" = "$web" ] || fail "WEB_PORT changed by a rejected PUT"
expect_json "GET config" "$(api GET /api/v1/config)" '(.keys | length) > 20 and ([.keys[] | select(.name == "MIN_WAN_DL_MBPS")][0].value == "123")'

# Too many wrong tokens lock the address out for a while (last, it also blocks the CLI).
for _ in 1 2 3 4 5 6 7 8 9 10; do code GET /api/v1/config -H 'Authorization: Bearer wrong' >/dev/null; done
[ "$(code GET /api/v1/config -H "Authorization: Bearer $token")" = 429 ] || fail "10 wrong tokens did not lock the address out"
echo "  ok: lockout after 10 wrong tokens"

# Shutdown: SIGTERM stops the daemon and the iperf3 child.
child=$(curl -sS "$base/api/v1/status" | jq -r .lan_server.pid)
kill -TERM "$daemon_pid"
i=0
while kill -0 "$daemon_pid" 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -lt 150 ] || fail "the daemon did not stop within 15 s"
	sleep 0.1
done
wait "$daemon_pid" || fail "daemon exit code $?"
daemon_pid=
! kill -0 "$child" 2>/dev/null || fail "iperf3 child $child outlived the daemon"
grep -q 'iperf3 server: started' "$root/data/qnetspeed.log" || fail "no iperf3 start in the daemon log"
grep -q 'admin: POST /api/v1/tests/lan from 127.0.0.1' "$root/data/qnetspeed.log" || fail "no admin action in the daemon log"
if grep -q "$token" "$root/data/qnetspeed.log"; then fail "the token is in the log"; fi
echo "  ok: shutdown stops the iperf3 child; log has start and admin actions, no token"
echo "== integration test passed"
