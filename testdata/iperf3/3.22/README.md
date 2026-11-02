# iperf3 3.22: wyjście `-J` (pliki syntetyczne)

Wszystkie pliki są **syntetyczne**: odtworzone z dokumentacji i kodu iperf3 (pola `end.sum_sent`,
`end.sum_received`, `end.sum`, `end.cpu_utilization_percent`, `error`), a nie przechwycone z prawdziwego
uruchomienia. Liczby są przykładowe. Prawdziwe przechwycenie (serwer i klient na `127.0.0.1` w CI,
oraz TS-464 → inny host) zastąpi je w F3; do tego czasu `bidir` jest szczególnie niepewne
(`sum_received_bidir_reverse` występuje tylko w iperf3 ≥ 3.7).
