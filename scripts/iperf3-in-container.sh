#!/bin/sh
# Uruchamiany wewnątrz kontenera Alpine przez build-iperf3.sh.
# Plan A (spec): --enable-static-bin. Plan B (W3): LDFLAGS=-static.
# Wynik: /work/out/iperf3 (statyczny, po strip). Do logu trafia, który plan zadziałał.
set -eu
apk add --no-cache build-base linux-headers file binutils >/dev/null

cd /work
tar -xzf "iperf-$IPERF3_VERSION.tar.gz"
src="/work/iperf-$IPERF3_VERSION"
mkdir -p /work/out

is_static() {
	# Statyczne: brak nagłówka INTERP i brak sekcji dynamicznej z NEEDED.
	! readelf -l "$1" | grep -q 'INTERP' && ! readelf -d "$1" 2>/dev/null | grep -q 'NEEDED'
}

build() {
	plan="$1"
	shift
	log="/work/build-$plan.log"
	echo "== iperf3 $IPERF3_VERSION: plan $plan: configure $*"
	(
		cd "$src"
		make distclean >/dev/null 2>&1 || true
		./configure --disable-shared --without-openssl --without-sctp "$@" &&
			make -j"$(nproc)"
	) >"$log" 2>&1 || {
		echo "plan $plan: build failed"
		tail -n 40 "$log"
		return 1
	}
	if ! is_static "$src/src/iperf3"; then
		echo "plan $plan: binary is not static"
		file "$src/src/iperf3"
		return 1
	fi
	echo "W3: plan $plan OK"
}

if ! build A --enable-static-bin; then
	build B LDFLAGS=-static
fi

strip "$src/src/iperf3"
cp "$src/src/iperf3" /work/out/iperf3
file /work/out/iperf3
/work/out/iperf3 --version
chown -R "${HOST_UID:-0}:${HOST_GID:-0}" /work/out
