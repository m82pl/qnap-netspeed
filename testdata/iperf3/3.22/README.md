# iperf3 3.22: wyjście `-J`

## Prawdziwe przechwycenia (`captured-ci-*.json`)

Zebrane 2026-11-02 przez job `integration` w CI (`scripts/integration-test.sh`): statyczny iperf3 3.22
zbudowany przez `scripts/build-iperf3.sh`, serwer i klient na `127.0.0.1:5288`, runner ubuntu-24.04,
`-t 2 -P 4`. Wartości prędkości mówią o pamięci runnera, nie o sieci.

| Plik | Polecenie klienta |
|---|---|
| `captured-ci-tcp-upload.json` | `iperf3 -c 127.0.0.1 -p 5288 -t 2 -P 4 -J` |
| `captured-ci-tcp-download.json` | jak wyżej z `-R` |
| `captured-ci-tcp-bidir.json` | jak wyżej z `--bidir` (8 strumieni; potwierdza `end.sum_received_bidir_reverse`) |
| `captured-ci-udp.json` | `iperf3 -c … -t 2 -u -b 100M -J` |
| `captured-ci-error-refused.json` | `iperf3 -c 127.0.0.1 -p 1 -t 2 -J` (port zamknięty) |

Z plików TCP usunięto `intervals`, `start.connected` i `end.streams` (powtarzalne dane per strumień,
których program nie używa); w UDP i błędzie zostało wszystko poza `intervals`. Reszta jest bez zmian.

Co wynika z przechwyceń (i czego syntetyczne pliki nie pokazywały):

- `end.sum_received` jest w TCP w obu kierunkach i jest wynikiem; w `--bidir` kierunek z klienta to
  `sum_received`, a z serwera `sum_received_bidir_reverse`.
- UDP: jitter i utrata pakietów są w `end.sum_received` (widok odbiorcy) i w `end.sum`; `end.sum_sent` ma
  `jitter_ms` równe 0.
- `cpu_utilization_percent.host_total` to czas CPU wszystkich wątków względem jednego rdzenia:
  251% przy 4 strumieniach. Próg `cpu_limited` skaluje się więc liczbą strumieni i rdzeni.
- Błąd połączenia: `error` bez przedrostka `error - ` (3.22), `end` puste.

## Pliki syntetyczne (`synthetic-*.json`)

Odtworzone z dokumentacji i kodu iperf3, liczby przykładowe. Zostały jako małe, czytelne wejścia dla
testów, które nie dotyczą kształtu wyjścia iperf3 (np. progi, powiadomienia). Kształt wyjścia sprawdzają
pliki `captured-ci-*`.
