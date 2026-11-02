#!/bin/sh
# Buduje paczki qnap-netspeed_<ver>_<arch>.qpkg przez QDK qbuild (spec pkt 11.2, job `package`).
# Użycie: scripts/package-qpkg.sh <katalog z bin-x86_64/ i bin-arm_64/> <katalog wyjściowy>
# Wymaga: qbuild (QDK 2.5.0) w PATH. GITHUB_REF=refs/tags/vX.Y.Z wymusza zgodność z QPKG_VER.
set -eu
artifacts=$(cd "$1" && pwd)
mkdir -p "$2"
out=$(cd "$2" && pwd)
repo=$(cd "$(dirname "$0")/.." && pwd)
archs="x86_64 arm_64"

fail() {
	echo "::error::$*" >&2
	exit 1
}

ver=$(sed -n 's/^QPKG_VER="\(.*\)"$/\1/p' "$repo/qpkg/qpkg.cfg")
[ -n "$ver" ] || fail "QPKG_VER not found in qpkg/qpkg.cfg"
[ "${#ver}" -le 10 ] || fail "QPKG_VER=$ver is longer than 10 characters"
case "$ver" in
*[!0-9.]* | .* | *. | *..*) fail "QPKG_VER=$ver: only digits and dots allowed" ;;
esac
case "${GITHUB_REF:-}" in
refs/tags/v*)
	[ "${GITHUB_REF#refs/tags/v}" = "$ver" ] || fail "tag ${GITHUB_REF#refs/tags/} does not match QPKG_VER=$ver"
	;;
esac
echo "QPKG_VER=$ver"

# Drzewo wejściowe QDK w katalogu roboczym (repozytorium zostaje czyste).
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$repo/qpkg/." "$work/"
rm -rf "$work/build"
mkdir -p "$work/shared/licenses"
cp -R "$repo/third_party/licenses/." "$work/shared/licenses/"
cp "$repo/LICENSE" "$work/shared/licenses/qnap-netspeed-LICENSE"
chmod 0755 "$work/shared/qnetspeed.sh"
for arch in $archs; do
	mkdir -p "$work/$arch/bin"
	for b in qnetspeed librespeed-cli iperf3; do
		[ -s "$artifacts/bin-$arch/$b" ] || fail "missing $artifacts/bin-$arch/$b"
		cp "$artifacts/bin-$arch/$b" "$work/$arch/bin/$b"
		chmod 0755 "$work/$arch/bin/$b"
	done
done

# qdk.conf z paczki .deb wylicza QDK_PATH z bieżącego katalogu (działa tylko w ścieżce
# zawierającej "QDK"), więc katalogi QDK podajemy jawnie, względem położenia qbuild.
qbuild_path=$(command -v qbuild) || fail "qbuild not found (install QDK)"
qdk=$(dirname "$(dirname "$(readlink -f "$qbuild_path")")")
export QDK_SCRIPTS_DIR="$qdk/scripts" QDK_TEMPLATE_DIR="$qdk/template"
[ -f "$QDK_SCRIPTS_DIR/qinstall.sh" ] || fail "QDK scripts not found in $qdk"

cd "$work"
for arch in $archs; do
	echo "== qbuild $arch"
	qbuild --build-arch "$arch" --build-dir "$work/build" --exclude .gitkeep
	f="qnap-netspeed_${ver}_${arch}.qpkg"
	[ -f "build/$f" ] || fail "qbuild did not produce $f: $(ls build)"
	cp "build/$f" "$out/$f"
done

cd "$out"
for arch in $archs; do
	f="qnap-netspeed_${ver}_${arch}.qpkg"
	sh "$repo/scripts/check-qpkg.sh" "$f" "$arch" "$artifacts/bin-$arch"
done
sha256sum qnap-netspeed_*.qpkg >SHA256SUMS
cat SHA256SUMS
ls -l "$out"
