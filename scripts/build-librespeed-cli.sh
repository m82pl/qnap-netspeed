#!/bin/sh
# Buduje librespeed-cli z przypiętego tagu i weryfikuje SHA commita.
# Wymaga zmiennych z third_party/versions.env:
#   LIBRESPEED_CLI_REPO, LIBRESPEED_CLI_VERSION, LIBRESPEED_CLI_COMMIT
# Użycie: scripts/build-librespeed-cli.sh <goarch> <katalog_wyjściowy> [katalog_roboczy]
set -eu
arch="$1"
out="$(mkdir -p "$2" && cd "$2" && pwd)"
work="${3:-${RUNNER_TEMP:-/tmp}/librespeed-src}"

: "${LIBRESPEED_CLI_REPO:?}" "${LIBRESPEED_CLI_VERSION:?}" "${LIBRESPEED_CLI_COMMIT:?}"

rm -rf "$work"
git -c advice.detachedHead=false clone --quiet --depth 1 \
	--branch "$LIBRESPEED_CLI_VERSION" "$LIBRESPEED_CLI_REPO" "$work"
head="$(git -C "$work" rev-parse HEAD)"
if [ "$head" != "$LIBRESPEED_CLI_COMMIT" ]; then
	echo "librespeed-cli: tag $LIBRESPEED_CLI_VERSION points to $head, expected $LIBRESPEED_CLI_COMMIT" >&2
	exit 1
fi

defs=github.com/librespeed/speedtest-cli/defs
cd "$work"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOFLAGS=-mod=readonly \
	go build -trimpath \
	-ldflags "-s -w -X $defs.ProgName=librespeed-cli -X $defs.ProgVersion=$LIBRESPEED_CLI_VERSION" \
	-o "$out/librespeed-cli" .
