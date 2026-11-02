# librespeed-cli v1.0.14: wyjście `--json` (W1)

Pliki `synthetic-*` są odtworzone **z kodu źródłowego** v1.0.14 (commit `42e5f76c`,
`report/json.go`, `speedtest/helper.go`, `defs/bytes_counter.go`). Liczby i nazwy są przykładowe.
Pliki `captured-*` pochodzą z prawdziwych uruchomień w CI (workflow `w1-capture.yml`) lub z NAS-a
i mają pierwszeństwo przed `synthetic-*` w testach parsera (F2).

| Plik | Przypadek | Kod wyjścia | stdout | stderr |
|---|---|---|---|---|
| `synthetic-success.json` | test udany, backend zwrócił `rawIspInfo` jako obiekt | 0 | tablica JSON z 1 elementem | komunikaty postępu |
| `synthetic-success-no-client-ip.json` | test udany, backend bez `rawIspInfo` (pole puste lub brak) | 0 | jw., `client.ip` = `""` | jw. |
| `synthetic-server-not-responding.stdout` | wybrany serwer nie odpowiada (`--server ID`) | **0** | `[]` | `Selected server … is not responding …` |
| `synthetic-no-server.stderr` | żaden serwer z listy nie odpowiada | 1 | pusto | `No server is currently available, please try again later.` |
| (bez pliku) | inny błąd (lista serwerów, IP info, ping, download, upload) | 1 | pusto | `Terminated due to error: …` |

Szczegóły i wnioski dla parsera: [docs/F0.md](../../../docs/F0.md#w1).
