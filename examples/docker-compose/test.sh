#!/bin/sh
# test.sh runs this example as README.md says and checks it: fit answers for
# whoami.localhost from both replicas, drops the route when they stop, and
# takes it back when they start again. CI runs it; it needs Docker Compose
# and curl, and port 8080 free.
set -eu
cd "$(dirname "$0")"
seen=$(mktemp)
trap 'docker compose logs fit | tail -50; docker compose down --remove-orphans; rm -f "$seen"' EXIT

docker compose up -d --build

# get prints the status and body of a request for whoami.localhost.
get() { curl -s -H 'Host: whoami.localhost' -w '\n%{http_code}' http://127.0.0.1:8080/; }

# wait_for waits up to 30s for get's last line, the status, to be $1.
wait_for() {
	for _ in $(seq 60); do
		[ "$(get | tail -n 1)" = "$1" ] && return 0
		sleep 0.5
	done
	echo "whoami.localhost never answered $1; last: $(get)" >&2
	return 1
}

# replicas waits up to 30s for both replicas to have answered, each as it
# passes its first health check.
replicas() {
	for _ in $(seq 60); do
		get | sed -n 's/^Hostname: //p' >> "$seen"
		[ "$(sort -u "$seen" | wc -l)" -eq 2 ] && return 0
		sleep 0.5
	done
	echo "answered by $(sort -u "$seen" | wc -l) replicas, not 2" >&2
	return 1
}

wait_for 200
replicas

docker compose stop whoami
wait_for 404
docker compose start whoami
wait_for 200
echo "docker-compose example: ok"
