# librespeed-cli v1.0.14: wyjście `--json` (W1)

Pliki `captured-ci-*` pochodzą z prawdziwego uruchomienia w CI (workflow `w1-capture.yml`,
2026-11-02, runner GitHub `ubuntu-24.04`, automatyczny wybór serwera). Mają pierwszeństwo w testach parsera (F2).
Pliki `synthetic-*` są odtworzone z kodu źródłowego v1.0.14 dla przypadków, których CI nie pokazał
(wypełnione pola `client`); liczby i nazwy są przykładowe.

| Plik | Przypadek | Kod wyjścia | stdout | stderr |
|---|---|---|---|---|
| `captured-ci-success.json` | test udany (backend bez danych ISP: `client.*` puste) | 0 | tablica JSON z 1 elementem | pusto |
| `captured-ci-text-success.stdout` | ten sam przebieg w trybie `--simple` (porównanie jednostek) | 0 | `Download rate: … Mbps` | pusto |
| `synthetic-success.json` | test udany, backend zwrócił `rawIspInfo` z IP | 0 | tablica JSON, `client.ip` wypełnione | pusto |
| `synthetic-success-no-client-ip.json` | jw. bez `rawIspInfo` | 0 | `client.ip` = `""` | pusto |
| `captured-ci-server-not-responding.stdout` | `--server ID`, serwer nie odpowiada | **0** | **`null`** | pusto |
| `captured-ci-no-server.stderr` | żaden serwer z listy nie odpowiada | 1 | pusto | `No server is currently available, please try again later.` |
| `captured-ci-unknown-server-id.stderr` | `--server ID` spoza listy | 1 | pusto | `Error when fetching server list: …` + `Terminated due to error: …` |

Przy `--json` komunikaty postępu są wyłączone, więc stderr jest pusty, poza błędami.
Szczegóły i wnioski dla parsera: [docs/F0.md](../../../docs/F0.md#w1).
