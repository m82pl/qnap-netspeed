# qnap-netspeed

Natywna paczka QPKG dla QNAP NAS: testy prędkości WAN (librespeed-cli) i LAN (iperf3),
monitorowanie łączy fizycznych, harmonogram, powiadomienia w System Logs i GUI na porcie 8888.

Projekt w budowie, wersja docelowa 1.0.0. Stan: faza F1 (walking skeleton: paczka instaluje się,
startuje i ma wpis crona; testów prędkości jeszcze nie ma), zob. [docs/F1.md](docs/F1.md)
i [docs/F0.md](docs/F0.md).

## Instalacja (wersja testowa, paczka niepodpisana)

1. Pobierz artefakt `qnap-netspeed-qpkg` z przebiegu workflow **build** w GitHub Actions.
2. Skopiuj `.qpkg` właściwej architektury (`uname -m`: `x86_64` → `x86_64`, `aarch64` → `arm_64`)
   do folderu udostępnionego, np. `/share/Public/`.
3. W SSH jako admin: `sh /share/Public/qnap-netspeed_<wersja>_x86_64.qpkg`.
4. Sprawdź `qnetspeed version` i `http://<NAS>:8888/`.

Szczegóły i lista kontrolna bramki G1: [docs/F1.md](docs/F1.md).

## Platformy

| Paczka | NAS | Status |
|---|---|---|
| `x86_64` | QTS 5.x / 6.x | testowana na TS-464 |
| `arm_64` | QTS 5.x / 6.x (aarch64) | eksperymentalna: budowana i sprawdzana tylko w CI |
| — | QuTS hero | eksperymentalny: nietestowany na sprzęcie |

## Budowanie

Wszystko buduje GitHub Actions (`.github/workflows/build.yml`); wersje narzędzi i źródeł są przypięte
w [`third_party/versions.env`](third_party/versions.env). Lokalnie (Linux, Go z `go.mod`):

```sh
go test ./...
sh scripts/build-qnetspeed.sh amd64 out
sudo sh scripts/test-service.sh out/qnetspeed   # skrypt usługi pod BusyBox (wymaga busybox-static)
```

Paczki `.qpkg` buduje `scripts/package-qpkg.sh` (QDK 2.5.0 `qbuild`), wywoływany w jobie `package`.

## Licencja

`qnap-netspeed`: MIT, © 2026 Marcin Tarapata, zob. [LICENSE](LICENSE).
Dołączone programy: librespeed-cli (LGPL-3.0) i iperf3 (BSD-3-Clause), zob. [`third_party/licenses/`](third_party/licenses/).
