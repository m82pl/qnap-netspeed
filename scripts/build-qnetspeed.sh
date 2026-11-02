#!/bin/sh
# Buduje statyczne binarium qnetspeed.
# Użycie: scripts/build-qnetspeed.sh <goarch> <katalog_wyjściowy> [wersja]
set -eu
arch="$1"
out="$2"
ver="${3:-dev}"
mkdir -p "$out"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
	go build -trimpath -ldflags "-s -w -X main.version=$ver" \
	-o "$out/qnetspeed" ./cmd/qnetspeed
