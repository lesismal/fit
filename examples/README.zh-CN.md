# 示例

[English](README.md) | [简体中文](README.zh-CN.md)

| 示例 | 内容 |
| --- | --- |
| [hello.caddyfile](hello.caddyfile)、[hello.yaml](hello.yaml)、[hello.json](hello.json) | 在 8080 端口返回固定响应，三种格式各一份：`fit run -c examples/hello.caddyfile`。 |
| [proxy.caddyfile](proxy.caddyfile)、[proxy.yaml](proxy.yaml)、[proxy.json](proxy.json) | 反向代理到两个上游，带限制、超时和健康检查。 |
| [docker-compose](docker-compose/README.zh-CN.md) | fit 的镜像位于一个应用的容器前面，按容器的 label 路由。 |
| [kubernetes](kubernetes/README.zh-CN.md) | 在 kind 集群里用 fit 的 Helm chart 服务一个 Gateway 和一个 Ingress。 |

每个目录下的 `test.sh` 按其 README 的步骤运行示例并检查响应；CI 会运行它们。fit 的打包方式见
[deploy](../deploy/README.zh-CN.md)。
