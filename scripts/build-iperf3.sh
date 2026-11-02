#!/bin/sh
# Buduje statyczny iperf3 na musl w kontenerze Alpine (spec D4, W3).
# Wymaga: docker oraz zmiennych IPERF3_URL, IPERF3_SHA256, IPERF3_VERSION, ALPINE_IMAGE.
# Platforma kontenera = natywna architektura runnera (amd64 lub arm64).
# Użycie: scripts/build-iperf3.sh <katalog_wyjściowy>
set -eu
: "${IPERF3_URL:?}" "${IPERF3_SHA256:?}" "${IPERF3_VERSION:?}" "${ALPINE_IMAGE:?}"
out="$(mkdir -p "$1" && cd "$1" && pwd)"
here="$(cd "$(dirname "$0")" && pwd)"
work="${RUNNER_TEMP:-/tmp}/iperf3-build"

rm -rf "$work"
mkdir -p "$work"
tarball="$work/iperf-$IPERF3_VERSION.tar.gz"
curl -fsSL --retry 3 -o "$tarball" "$IPERF3_URL"
echo "$IPERF3_SHA256  $tarball" | sha256sum -c -
cp "$here/iperf3-in-container.sh" "$work/"

docker run --rm \
	-v "$work:/work" -w /work \
	-e IPERF3_VERSION="$IPERF3_VERSION" \
	-e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	"$ALPINE_IMAGE" sh /work/iperf3-in-container.sh

cp "$work/out/iperf3" "$out/iperf3"
