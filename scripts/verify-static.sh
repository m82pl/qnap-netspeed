#!/bin/sh
# Sprawdza binaria: statycznie zlinkowane, bez INTERP, właściwa architektura ELF (spec 11.2 pkt 4).
# Użycie: scripts/verify-static.sh <X86-64|AArch64> <plik>...
set -eu
want="$1"
shift
rc=0
for f in "$@"; do
	desc="$(file -b "$f")"
	machine="$(readelf -h "$f" | sed -n 's/^ *Machine: *//p')"
	err=""
	case "$desc" in
	*"statically linked"* | *"static-pie linked"*) ;;
	*) err="$err not-static" ;;
	esac
	if readelf -l "$f" | grep -q INTERP; then
		err="$err has-INTERP"
	fi
	case "$want:$machine" in
	X86-64:*X86-64*) ;;
	AArch64:*AArch64*) ;;
	*) err="$err machine=$machine" ;;
	esac
	if [ -n "$err" ]; then
		echo "FAIL $f:$err ($desc)" >&2
		rc=1
	else
		echo "OK   $f: $machine, $desc"
	fi
done
exit "$rc"
