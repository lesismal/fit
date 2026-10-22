# 在 Kubernetes 中运行 fit

[English](README.md) | [简体中文](README.zh-CN.md)

在 [kind](https://kind.sigs.k8s.io) 集群里用 fit 的 [Helm chart](../../deploy/helm/fit) 安装 fit，通过一个 Gateway
和 HTTPRoute（[gateway.yaml](gateway.yaml)），以及一个 Ingress（[ingress.yaml](ingress.yaml)），服务
[whoami](https://github.com/traefik/whoami) 的两个副本。[app.yaml](app.yaml) 是应用和它的 Service。需要带
BuildKit（buildx）的 Docker、kind、kubectl 和 Helm。

在仓库根目录执行：

```bash
kind create cluster --name fit-examples
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
docker build -t fit:local .
kind load docker-image fit:local --name fit-examples
helm install fit deploy/helm/fit --set image.repository=fit --set image.tag=local --wait
kubectl apply -f examples/kubernetes/app.yaml -f examples/kubernetes/gateway.yaml -f examples/kubernetes/ingress.yaml
kubectl wait gateway/web --for=condition=Programmed
```

Gateway API 的 CRD 要在 chart 之前安装，chart 才会创建 fit 的 GatewayClass。`ghcr.io/lesismal/fit` 发布后，
能拉取它的集群就不需要构建、`kind load` 和那两个 `--set`。

kind 没有负载均衡器，所以通过端口转发访问 fit 的 Service：

```bash
kubectl port-forward svc/fit 8080:80 &
curl -H 'Host: whoami.example' http://127.0.0.1:8080/
curl -H 'Host: whoami-ingress.example' http://127.0.0.1:8080/
```

两者都返回 `Hostname: <pod>`，依次来自两个 whoami pod。fit 会写入 Gateway 和 HTTPRoute 的状态：

```bash
kubectl get gateway web
kubectl describe httproute whoami
```

结束后执行 `kind delete cluster --name fit-examples`。[test.sh](test.sh) 会在自己的集群里执行这些步骤并检查每次的响应。
