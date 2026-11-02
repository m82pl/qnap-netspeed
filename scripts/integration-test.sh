#!/bin/sh
# Test integracyjny na Linuksie runnera CI (plan: "Test integracyjny na Linuksie runnera"):
# prawdziwy statyczny iperf3 i qnetspeed z katalogu <bindir>, serwer iperf3 na loopbacku.
#  1. Przechwytuje prawdziwe wyjście `iperf3 -J` (TCP w obie strony, --bidir, UDP, błąd) do
#     <katalog_przechwyceń> i wypisuje je w logu (bez `intervals`). Z tego powstał testdata/iperf3/3.22/.
#  2. Sprawdza `qnetspeed lan-test` (zwykły, --bidir, --udp) na prawdziwym iperf3.
#  3. Sprawdza `qnetspeed cron` z LAN_SCHEDULE_TARGETS=127.0.0.1: wpis do System Logs raz,
#     bez powtórzenia przy następnym przebiegu, informacja o powrocie do normy.
# /sbin/log_tool jest atrapą zapisującą argumenty (instalowaną tylko na runnerze CI).
# Uruchamiać jako root: sudo sh scripts/integration-test.sh out captures
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 2; }
[ "${CI:-}" = true ] || { echo "refusing to install a fake /sbin/log_tool outside CI" >&2; exit 2; }
[ ! -e /sbin/log_tool ] || { echo "/sbin/log_tool exists, not replacing it" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

src="$(cd "$1" && pwd)"
cap="$(mkdir -p "$2" && cd "$2" && pwd)"
root="$(mktemp -d)"
port=5288
calls="$root/log_tool.calls"

cleanup() {
	[ -f "$root/iperf3.pid" ] && kill "$(cat "$root/iperf3.pid")" 2>/dev/null
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
echo "== integration test passed"
