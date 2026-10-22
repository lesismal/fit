# fit in Kubernetes

[English](README.md) | [简体中文](README.zh-CN.md)

fit installed with its [Helm chart](../../deploy/helm/fit) in a [kind](https://kind.sigs.k8s.io)
cluster, serving two replicas of [whoami](https://github.com/traefik/whoami) through a Gateway and
an HTTPRoute ([gateway.yaml](gateway.yaml)), and through an Ingress ([ingress.yaml](ingress.yaml)).
[app.yaml](app.yaml) is the app and its Service. It needs Docker with BuildKit (buildx), kind,
kubectl and Helm.

From the repository's root:

```bash
kind create cluster --name fit-examples
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
docker build -t fit:local .
kind load docker-image fit:local --name fit-examples
helm install fit deploy/helm/fit --set image.repository=fit --set image.tag=local --wait
kubectl apply -f examples/kubernetes/app.yaml -f examples/kubernetes/gateway.yaml -f examples/kubernetes/ingress.yaml
kubectl wait gateway/web --for=condition=Programmed
```

The Gateway API's CRDs go in before the chart, so that it makes fit's GatewayClass. A cluster that
can pull `ghcr.io/lesismal/fit` once it is published needs neither the build nor
`kind load`, nor the `--set`s.

kind has no load balancer, so reach fit's Service through a port forward:

```bash
kubectl port-forward svc/fit 8080:80 &
curl -H 'Host: whoami.example' http://127.0.0.1:8080/
curl -H 'Host: whoami-ingress.example' http://127.0.0.1:8080/
```

Each answers with `Hostname: <pod>`, from one whoami pod and then the other. fit writes the
Gateway's and the HTTPRoute's status:

```bash
kubectl get gateway web
kubectl describe httproute whoami
```

Afterwards: `kind delete cluster --name fit-examples`. [test.sh](test.sh) runs these steps in a
cluster of its own and checks each answer.
