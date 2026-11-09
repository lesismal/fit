```
host: ubuntu-System-Product-Name, 16 CPUs, 7.0.0-38-generic
size=1073741824 peers=4 rounds=3 server_cpus=0-3 client_cpus=8-15
fit: fit v1.0.1-0.20261109020149-12f4795eaf16+dirty
2026-11-09T02:40:16Z
```

| Scenario | Connections | Client | Rounds | MiB/s | CPU % | CPU s per GiB | Peak RSS MiB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| download | mse | anacrolix | 3 | 759 | 283 | 3.81 | 1052 |
| download | mse | fib | 3 | 363 | 137 | 3.85 | 152 |
| download | mse | fit | 3 | 553 | 199 | 3.67 | 176 |
| download | plain | anacrolix | 3 | 1039 | 225 | 2.23 | 1050 |
| download | plain | fib | 3 | 478 | 106 | 2.28 | 163 |
| download | plain | fit | 3 | 705 | 158 | 2.28 | 179 |
| seed | mse | anacrolix | 3 | 744 | 199 | 2.74 | 1060 |
| seed | mse | fib | 3 | 937 | 217 | 2.36 | 43 |
| seed | mse | fit | 3 | 933 | 215 | 2.35 | 56 |
| seed | plain | anacrolix | 3 | 1110 | 133 | 1.23 | 1058 |
| seed | plain | fib | 3 | 1670 | 93 | 0.57 | 34 |
| seed | plain | fit | 3 | 1688 | 98 | 0.60 | 46 |

| Scenario | Connections | fit / anacrolix MiB/s | fit / anacrolix CPU per GiB | fit / fib MiB/s | fit / fib CPU per GiB |
| --- | --- | --- | --- | --- | --- |
| download | mse | 0.73 | 0.96 | 1.52 | 0.95 |
| download | plain | 0.68 | 1.02 | 1.47 | 1.00 |
| seed | mse | 1.25 | 0.86 | 1.00 | 1.00 |
| seed | plain | 1.52 | 0.49 | 1.01 | 1.05 |
