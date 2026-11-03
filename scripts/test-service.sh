#!/bin/sh
# Testuje paczkę w chroot z BusyBox i atrapami QTS (/sbin/getcfg, /sbin/setcfg,
# /sbin/log_tool), z prawdziwym statycznym qnetspeed i apletem crontab BusyBox.
#
#   scripts/test-service.sh <qnetspeed>          skrypt usługi i package_routines
#                                                (tests/service/steps.sh)
#   scripts/test-service.sh <qnetspeed> <.qpkg>  instalacja `sh plik.qpkg` instalatorem QDK,
#                                                aktualizacja i odinstalowanie (tests/service/install.sh)
#
# Wymaga roota (chroot, unshare, mknod) i statycznego busybox w PATH.
set -eu
bin=$1
qpkg=${2:-}
repo=$(cd "$(dirname "$0")/.." && pwd)
bb=$(command -v busybox)
vol=/share/CACHEDEV1_DATA
root=$vol/.qpkg/qnap-netspeed

R=$(mktemp -d)
trap 'rm -rf "$R"' EXIT
for d in bin sbin usr/bin usr/sbin usr/local/bin etc/config etc/init.d etc/rcS.d etc/rcK.d \
	home/httpd/RSS/images var/run var/log var/spool/cron/crontabs tmp proc dev t mnt/HDA_ROOT "$vol/Public"; do
	mkdir -p "$R/$d"
done
chmod 1777 "$R/tmp"
mknod -m 0666 "$R/dev/null" c 1 3
cp "$bb" "$R/bin/busybox"
# Każdy aplet w /bin, /sbin, /usr/bin i /usr/sbin: QDK i QTS wołają polecenia pełną ścieżką.
for a in $("$bb" --list); do
	for d in bin sbin usr/bin usr/sbin; do
		[ -e "$R/$d/$a" ] || ln -s /bin/busybox "$R/$d/$a"
	done
done
# Różnice BusyBox z Ubuntu względem QTS, od których zależy instalator QDK:
# - dd: nagłówek .qpkg czyta dane przez `dd bs=1024 count=N` z potoku; krótkie odczyty
#   mogą uciąć data.tar.gz, więc w teście wymuszamy pełne bloki;
# - tar: `tar xvf data.tar.gz` na QTS sam rozpoznaje gzip, tutaj trzeba dodać z.
rm -f "$R/bin/dd" "$R/bin/tar"
cat >"$R/bin/dd" <<'EOF2'
#!/bin/busybox sh
exec /bin/busybox dd iflag=fullblock "$@"
EOF2
cat >"$R/bin/tar" <<'EOF2'
#!/bin/busybox sh
case "$1" in
xf | xvf) case "$2" in *.gz | *.tgz) op=$1; shift; exec /bin/busybox tar "z$op" "$@" ;; esac ;;
esac
exec /bin/busybox tar "$@"
EOF2
chmod 0755 "$R/bin/dd" "$R/bin/tar"
for f in getcfg setcfg log_tool; do
	rm -f "$R/sbin/$f"
	cp "$repo/tests/service/fakes/$f" "$R/sbin/$f"
	chmod 0755 "$R/sbin/$f"
done
# crontab to aplet BusyBox (jak na QTS): instaluje plik do /var/spool/cron/crontabs/root.
echo 'root:x:0:0:root:/root:/bin/sh' >"$R/etc/passwd"
cat >"$R/etc/config/crontab" <<'CRON'
# m h dom m dow cmd
0 4 * * * /sbin/other_job # qnap-netspeed-other
CRON
cat >"$R/etc/config/uLinux.conf" <<'CONF'
[System]
Version = 5.2.0
CONF
cat >"$R/etc/config/smb.conf" <<CONF
[Public]
path = $vol/Public
CONF
: >"$R/etc/platform.conf"
cp "$repo/qpkg/package_routines" "$repo/tests/service/steps.sh" "$repo/tests/service/install.sh" "$R/t/"

if [ -z "$qpkg" ]; then
	# Paczka rozłożona tak jak robi to instalator QDK.
	mkdir -p "$R$root/bin" "$R$root/etc"
	cp "$repo/qpkg/shared/qnetspeed.sh" "$R$root/qnetspeed.sh"
	cp -R "$repo/qpkg/shared/etc/." "$R$root/etc/"
	cp "$bin" "$R$root/bin/qnetspeed"
	cp "$repo/tests/service/fakes/iperf3" "$R$root/bin/iperf3"
	chmod 0755 "$R$root/qnetspeed.sh" "$R$root/bin/qnetspeed" "$R$root/bin/iperf3"
	ln -s "$root/qnetspeed.sh" "$R/etc/init.d/qnetspeed.sh"
	cat >"$R/etc/config/qpkg.conf" <<CONF
[qnap-netspeed]
Name = qnap-netspeed
Install_Path = $root
Enable = TRUE
CONF
	script=/t/steps.sh
else
	: >"$R/etc/config/qpkg.conf"
	cp "$qpkg" "$R$vol/Public/"
	script="/t/install.sh $vol/Public/$(basename "$qpkg")"
fi

# Osobne przestrzenie PID i montowań: /proc tylko dla testu, /mnt/HDA_ROOT widoczny
# w /proc/mounts (instalator QDK sprawdza go przed instalacją).
# shellcheck disable=SC2016,SC2086
unshare --pid --fork --mount --mount-proc="$R/proc" \
	sh -c 'mount -t tmpfs hda "$1/mnt/HDA_ROOT" && shift && exec chroot "$@"' sh "$R" "$R" /bin/sh $script
