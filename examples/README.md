# Examples

[English](README.md) | [简体中文](README.zh-CN.md)

| Example | What it shows |
| --- | --- |
| [hello.konf](hello.konf), [hello.yaml](hello.yaml), [hello.json](hello.json) | A fixed response on port 8080, in each format: `fit run -c examples/hello.konf`. |
| [proxy.konf](proxy.konf), [proxy.yaml](proxy.yaml), [proxy.json](proxy.json) | A reverse proxy to two upstreams, with limits, timeouts and health checks. |
| [docker-compose](docker-compose/README.md) | fit's image in front of an app's containers, routed by their labels. |
| [kubernetes](kubernetes/README.md) | fit's Helm chart in a kind cluster, serving a Gateway and an Ingress. |

Each directory's `test.sh` runs its example as its README says and checks the answers; CI runs
them. How fit is packaged is in [deploy](../deploy/README.md).
