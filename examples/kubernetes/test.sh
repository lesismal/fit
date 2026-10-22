#!/bin/sh
# test.sh runs this example as README.md says, in a kind cluster of its own,
# and checks it: fit, installed with the chart, answers for whoami.example
# through the Gateway and for whoami-ingress.example through the Ingress,
# from both of the app's pods, and writes the Gateway's status. CI runs it.
#
# It needs docker, kind, kubectl, helm and curl. CLUSTER names the cluster
# (fit-examples), deleted at the end unless KEEP is set; PORT (8080) is the
# local port forwarded to fit's Service.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
cluster=${CLUSTER:-fit-examples}
port=${PORT:-8080}
work=$(mktemp -d)
export KUBECONFIG="$work/kubeconfig"
forward=
cleanup() {
	status=$?
	[ "$status" -eq 0 ] || kubectl logs deploy/fit --tail=50 || true
	[ -z "$forward" ] || kill "$forward" 2>/dev/null || true
	[ -n "${KEEP:-}" ] || kind delete cluster --name "$cluster"
	rm -rf "$work"
}
trap cleanup EXIT

kind create cluster --name "$cluster" --wait 120s
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
docker build -t fit:local "$root"
kind load docker-image fit:local --name "$cluster"
helm install fit "$root/deploy/helm/fit" --set image.repository=fit --set image.tag=local --wait --timeout 3m
kubectl apply -f "$here/app.yaml" -f "$here/gateway.yaml" -f "$here/ingress.yaml"
kubectl rollout status deploy/whoami --timeout 2m
kubectl wait gateway/web --for=condition=Programmed --timeout 1m
kubectl port-forward svc/fit "$port:80" > "$work/forward.log" 2>&1 &
forward=$!

# get prints the body and status of a request for host $1.
get() { curl -s -H "Host: $1" -w '\n%{http_code}' "http://127.0.0.1:$port/"; }

# wait_for waits up to 60s for host $1 to answer 200.
wait_for() {
	for _ in $(seq 120); do
		[ "$(get "$1" | tail -n 1)" = 200 ] && return 0
		sleep 0.5
	done
	echo "$1 never answered 200; last: $(get "$1")" >&2
	return 1
}

# pods waits up to 30s for both of the app's pods to have answered for host $1.
pods() {
	: > "$work/seen"
	for _ in $(seq 60); do
		get "$1" | sed -n 's/^Hostname: //p' >> "$work/seen"
		[ "$(sort -u "$work/seen" | wc -l)" -eq 2 ] && return 0
		sleep 0.5
	done
	echo "$1 was answered by $(sort -u "$work/seen" | wc -l) pods, not 2" >&2
	return 1
}

for host in whoami.example whoami-ingress.example; do
	wait_for "$host"
	pods "$host"
done
echo "kubernetes example: ok"
