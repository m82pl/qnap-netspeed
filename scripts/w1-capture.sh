#!/bin/sh
# W1: przechwytuje prawdziwe wyjście librespeed-cli (sukces, błędy) do katalogu wyjściowego
# i wypisuje je do logu. Nie przerywa się na kodach wyjścia ≠ 0 (są częścią wyniku).
# Użycie: scripts/w1-capture.sh <ścieżka_do_librespeed-cli> <katalog_wyjściowy>
set -u
bin="$1"
out="$(mkdir -p "$2" && cd "$2" && pwd)"

# Lista z jednym serwerem, który nie odpowiada (port discard na loopback).
cat >"$out/dead-servers.json" <<'JSON'
[{"id":1,"name":"Dead local server","server":"http://127.0.0.1:9/","dlURL":"garbage.php","ulURL":"empty.php","pingURL":"empty.php","getIpURL":"getIP.php"}]
JSON

capture() {
	name="$1"
	shift
	echo "::group::$name: librespeed-cli $*"
	"$bin" "$@" >"$out/$name.stdout" 2>"$out/$name.stderr"
	echo "$?" >"$out/$name.exit"
	echo "--- exit: $(cat "$out/$name.exit")"
	echo "--- stdout:"
	cat "$out/$name.stdout"
	echo "--- stderr:"
	cat "$out/$name.stderr"
	echo "::endgroup::"
}

"$bin" --version >"$out/version.txt" 2>&1
cat "$out/version.txt"

capture json-success --json --no-icmp --duration 5 --timeout 15
capture text-success --simple --no-icmp --duration 5 --timeout 15
capture json-unknown-server-id --json --no-icmp --duration 5 --timeout 15 --server 999999
capture json-no-server --json --no-icmp --duration 5 --timeout 5 --local-json "$out/dead-servers.json"
capture json-server-not-responding --json --no-icmp --duration 5 --timeout 5 --local-json "$out/dead-servers.json" --server 1

# Porównanie jednostek: tryb tekstowy podaje Mbps; JSON z tej samej wersji powinien mieć ten sam rząd wielkości.
echo "== unit check (JSON download vs text 'Download rate')"
grep -o '"download":[0-9.e+]*' "$out/json-success.stdout" || true
grep -i 'download' "$out/text-success.stdout" || true
exit 0
