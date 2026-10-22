#!/bin/sh
# run.sh runs the Gateway API's conformance suite against fit: it makes fit's
# GatewayClass in the cluster KUBECONFIG names, starts fit with the
# Kubernetes provider, whose Gateways listen on 127.0.0.1 and say so in their
# status, and runs the suite with the profile fit claims, GATEWAY-HTTP. The
# cluster's pods must be reachable from this machine, as they are from CI's
# runner and the benchmark host through a route to the kind node.
#
# Usage: KUBECONFIG=<path> test/conformance/run.sh <fit> <suite> [suite flags...]
#
# <fit> is fit's binary, and <suite> this package's test binary (go test -c).
# Gateways listen on ports 80 and 443, so fit needs to bind them. fit's output
# goes to $FIT_LOG (fit.log when unset), and the suite's report to $REPORT
# (report.yaml).
set -eu
: "${KUBECONFIG:?KUBECONFIG must name the cluster's kubeconfig}"
fit=$(realpath "$1")
suite=$(realpath "$2")
shift 2
kubeconfig=$(realpath "$KUBECONFIG")
class=fit
work=$(mktemp -d)

kubectl apply -f - <<END
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: $class}
spec: {controllerName: lesismal.github.io/fit}
END
cat > "$work/fit.json" <<END
{
  "listeners": [{"name": "base", "address": "127.0.0.1:0"}],
  "providers": [{"kubernetes": {"kubeconfig": "$kubeconfig", "listen_host": "127.0.0.1", "addresses": ["127.0.0.1"]}}]
}
END
"$fit" run -c "$work/fit.json" > "${FIT_LOG:-fit.log}" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null || true; wait $pid 2>/dev/null || true; rm -rf "$work"' EXIT

version=$("$fit" version | awk '{print $2; exit}')
"$suite" -test.v -test.timeout 60m -gateway-class "$class" -conformance-profiles GATEWAY-HTTP \
	-organization lesismal -project fit -url https://github.com/lesismal/fit -version "$version" \
	-contact https://github.com/lesismal/fit/issues -report-output "${REPORT:-report.yaml}" "$@"
