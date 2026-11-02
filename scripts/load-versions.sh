#!/bin/sh
# Wypisuje third_party/versions.env jako linie KLUCZ=wartość (bez komentarzy),
# np. do $GITHUB_ENV. Odrzuca linie, które nie są prostym przypisaniem.
set -eu
f="${1:-third_party/versions.env}"
grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$f" | while IFS= read -r line; do
	case "$line" in
	[A-Z_]*=*)
		key=${line%%=*}
		case "$key" in
		*[!A-Z0-9_]*) echo "versions.env: bad key: $key" >&2; exit 1 ;;
		esac
		printf '%s\n' "$line"
		;;
	*) echo "versions.env: bad line: $line" >&2; exit 1 ;;
	esac
done
