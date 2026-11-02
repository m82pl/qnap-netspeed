# qnap-netspeed

Natywna paczka QPKG dla QNAP NAS: testy prędkości WAN (librespeed-cli) i LAN (iperf3),
monitorowanie łączy fizycznych, harmonogram, powiadomienia w System Logs i GUI na porcie 8888.

Projekt w budowie, wersja docelowa 1.0.0. Stan: faza F0 (fundamenty), zob. [docs/F0.md](docs/F0.md).

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
```

## Licencja

`qnap-netspeed`: MIT, © 2026 Marcin Tarapata, zob. [LICENSE](LICENSE).
Dołączone programy: librespeed-cli (LGPL-3.0) i iperf3 (BSD-3-Clause), zob. [`third_party/licenses/`](third_party/licenses/).
