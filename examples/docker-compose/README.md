# fit with Docker Compose

[English](README.md) | [简体中文](README.zh-CN.md)

fit, built from this repository's [Dockerfile](../../Dockerfile), in front of two replicas of
[whoami](https://github.com/traefik/whoami), a small app that answers with its container's name.
fit finds them with its Docker provider: the app's labels in [compose.yaml](compose.yaml) are all of
its routing, and fit follows the containers as they start and stop. [fit.json](fit.json) has fit's
listener, the provider, and a 404 for every other host.

```bash
cd examples/docker-compose
docker compose up -d --build
curl -H 'Host: whoami.localhost' http://127.0.0.1:8080/
```

whoami answers with `Hostname: <container>`; ask again and the other replica answers, once both
have passed their first health check. The labels say:

| Label | Means |
| --- | --- |
| `fit.enable=true` | Take this container. |
| `fit.port=80` | Send to its port 80. |
| `fit.routes.web.host=whoami.localhost` | A route for this host, proxying to the upstream of the app's containers, `fit-example-whoami`. |
| `fit.upstream={"health_check": ...}` | More of the upstream's settings, as in the config: here, an active health check. |

Scale the app and fit follows:

```bash
docker compose up -d --scale whoami=3
docker compose stop whoami      # whoami.localhost now gets 404
docker compose start whoami
docker compose down
```

fit runs as root here because the Docker socket is root's; see [deploy](../../deploy/README.md) for
a socket proxy instead. [test.sh](test.sh) runs these steps and checks each answer.
