#!/bin/sh
# Kontrola gotowego .qpkg (spec pkt 11.2 krok 6 + smoke test z planu):
# rozmiar >= 90% sumy binariów po gzip, zawartość po rozpakowaniu (jak instalator QDK),
# binaria identyczne z wejściowymi, skrypt usługi wykonywalny, wersja w qpkg.cfg.
# Użycie: scripts/check-qpkg.sh <plik.qpkg> <x86_64|arm_64> <katalog z binariami>
set -eu
qpkg=$1
arch=$2
bins=$3

fail() {
	echo "::error::$qpkg: $*" >&2
	[ -n "${x:-}" ] && ls -laR "$x" >&2
	exit 1
}

case "$arch" in
x86_64) machine="X86-64" ;;
arm_64) machine="AArch64" ;;
*) fail "unknown arch $arch" ;;
esac

# Próg rozmiaru: spec zakłada 70% surowych binariów, ale binaria Go po gzip mają ok. 40%
# (pomiar F1), więc porównujemy z sumą rozmiarów binariów skompresowanych gzip -9;
# paczka musi mieć co najmniej 90% tej sumy. Pusta paczka (qbuild bez rsync) ma kilka KB.
size=$(wc -c <"$qpkg")
sum=0
for b in qnetspeed librespeed-cli iperf3; do
	sum=$((sum + $(gzip -9 -c "$bins/$b" | wc -c)))
done
min=$((sum * 90 / 100))
echo "$qpkg: $size bytes, gzipped binaries $sum bytes, minimum $min"
[ "$size" -ge "$min" ] || fail "only $size bytes, expected at least $min (90% of gzipped binaries): empty package?"

x=$(mktemp -d)
trap 'rm -rf "$x"' EXIT
# Rozpakowanie tak jak nagłówek instalatora QDK, ale z pełnymi blokami: `qbuild --extract`
# używa `dd bs=1024 count=N` z potoku i na GNU dd potrafi uciąć koniec data.tar.gz.
sed -n '1,/^exit 1/p' "$qpkg" >"$x/header"
script_len=$(sed -n 's/^script_len=\([0-9]*\)$/\1/p' "$x/header" | head -n 1)
blocks=$(sed -n 's/.* bs=1024 count=\([0-9]*\) .*/\1/p' "$x/header" | head -n 1)
if [ -z "$script_len" ] || [ -z "$blocks" ]; then fail "cannot parse the QPKG header"; fi
mkdir -p "$x/tree/control" "$x/tree/data"
tail -c +$((script_len + 1)) "$qpkg" | head -c 20480 | tar -xO | tar -xz -C "$x/tree/control" ||
	fail "cannot extract control files"
tail -c +$((script_len + 20480 + 1)) "$qpkg" | head -c $((blocks * 1024)) >"$x/data.tar.gz"
# Za archiwum gzip leży stopka QPKG (podpis, nazwa, wersja): gzip ostrzega o niej, tar nie.
{ gzip -dc "$x/data.tar.gz" 2>/dev/null || true; } | tar -x -C "$x/tree/data" || fail "cannot extract data files"

# Każdy plik i katalog w danych paczki musi należeć do roota (0/0) i nie może być zapisywalny
# dla grupy ani innych: skrypt usługi i binaria działają na NAS-ie jako root.
bad_owner=$({ gzip -dc "$x/data.tar.gz" 2>/dev/null || true; } | tar -tv --numeric-owner | awk '$2 != "0/0"')
[ -z "$bad_owner" ] || fail "files not owned by root: $bad_owner"
bad_mode=$({ gzip -dc "$x/data.tar.gz" 2>/dev/null || true; } | tar -tv | awk 'substr($1, 6, 1) == "w" || substr($1, 9, 1) == "w"')
[ -z "$bad_mode" ] || fail "group/world-writable files: $bad_mode"

# find_one SUFFIX: ścieżka jedynego pliku kończącego się na /SUFFIX.
find_one() {
	n=$(find "$x/tree" -path "*/$1" -type f | wc -l)
	[ "$n" -ge 1 ] || fail "missing $1"
	find "$x/tree" -path "*/$1" -type f | head -n 1
}

for f in qnetspeed.sh etc/qnetspeed.conf.default \
	licenses/qnap-netspeed-LICENSE licenses/librespeed-cli/LICENSE licenses/librespeed-cli/SOURCE \
	licenses/librespeed-cli/COPYING.GPL-3 licenses/iperf3/LICENSE \
	.qpkg_icon.gif .qpkg_icon_80.gif .qpkg_icon_gray.gif qpkg.cfg package_routines; do
	find_one "$f" >/dev/null
done
[ -x "$(find_one qnetspeed.sh)" ] || fail "qnetspeed.sh is not executable"

for b in qnetspeed librespeed-cli iperf3; do
	p=$(find_one "bin/$b")
	[ -x "$p" ] || fail "bin/$b is not executable"
	cmp -s "$p" "$bins/$b" || fail "bin/$b differs from the built binary"
	readelf -h "$p" | grep -q "Machine:.*$machine" || fail "bin/$b is not $machine"
done

ver=$(sed -n 's/^QPKG_VER="\(.*\)"$/\1/p' "$(find_one qpkg.cfg)")
case "$qpkg" in
*"_${ver}_${arch}.qpkg") ;;
*) fail "file name does not match QPKG_VER=$ver" ;;
esac
grep -q '^QPKG_AUTHOR="Marcin Tarapata"$' "$(find_one qpkg.cfg)" || fail "QPKG_AUTHOR missing"
grep -q 'PKG_POST_REMOVE=' "$(find_one package_routines)" || fail "package_routines without PKG_POST_REMOVE"
head -c 200 "$qpkg" | grep -q '^#!/bin/sh' || fail "not a QPKG shell header"
echo "$qpkg: OK ($arch, QPKG_VER=$ver)"
