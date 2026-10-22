# 用 Docker Compose 运行 fit

[English](README.md) | [简体中文](README.zh-CN.md)

用本仓库的 [Dockerfile](../../Dockerfile) 构建 fit，放在 [whoami](https://github.com/traefik/whoami) 的两个副本
前面；whoami 是一个用容器名回答请求的小应用。fit 用 Docker provider 发现它们：[compose.yaml](compose.yaml)
里应用的 label 就是全部路由配置，容器启动和停止时 fit 随之更新。[fit.json](fit.json) 包含 fit 的 listener、
provider，以及其他 host 一律返回的 404。

```bash
cd examples/docker-compose
docker compose up -d --build
curl -H 'Host: whoami.localhost' http://127.0.0.1:8080/
```

whoami 返回 `Hostname: <容器名>`；两个副本都通过首次健康检查后，再请求一次会由另一个副本回答。各 label 的含义：

| Label | 含义 |
| --- | --- |
| `fit.enable=true` | 接管这个容器。 |
| `fit.port=80` | 发往它的 80 端口。 |
| `fit.routes.web.host=whoami.localhost` | 为这个 host 建一条路由，代理到应用容器组成的上游 `fit-example-whoami`。 |
| `fit.upstream={"health_check": ...}` | 上游的其他设置，与配置文件写法相同：这里是主动健康检查。 |

扩缩应用，fit 会随之更新：

```bash
docker compose up -d --scale whoami=3
docker compose stop whoami      # whoami.localhost 此时返回 404
docker compose start whoami
docker compose down
```

这里 fit 以 root 运行，因为 Docker socket 属于 root；改用 socket 代理的方法见 [deploy](../../deploy/README.zh-CN.md)。
[test.sh](test.sh) 会执行这些步骤并检查每次的响应。
