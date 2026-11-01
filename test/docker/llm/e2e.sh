#!/usr/bin/env bash
# Start / stop the docker end-to-end test environment of fit's LLM feature and run the full test.
#
#   test/docker/llm/e2e.sh up        build the images and start every role (keeps existing data)
#   test/docker/llm/e2e.sh test      reset, start, run the whole test; the environment keeps running for inspection
#   test/docker/llm/e2e.sh all       like test, then remove everything (CI)
#   test/docker/llm/e2e.sh stop      stop the containers, keep their data
#   test/docker/llm/e2e.sh down      stop and remove the containers and their data
#   test/docker/llm/e2e.sh ps | logs [service...] | build
#
# The pages log in as admin with the password fit-e2e-pass: configs/*.json hold its bcrypt hash, as
# `echo fit-e2e-pass | fit hash-password` makes it. Environment: FIT_E2E_STORE_PASSWORD (the password every role's
# store is encrypted with, default fit-e2e-store-pass), GOPROXY (module proxy for the image build; the host's
# `go env GOPROXY` when unset), GO_IMAGE / BASE_IMAGE (the build and runtime base images, default
# golang:1.27-bookworm and debian:bookworm-slim, e.g. a mirror or an image already pulled), GO_TEST_RUN (extra
# -test.run filter for the TestE2E phase).
set -euo pipefail

cd "$(dirname "$0")"
ROLES=(hub node1 node2 node3 proxy1 proxy2)
export FIT_E2E_STORE_PASSWORD="${FIT_E2E_STORE_PASSWORD:-fit-e2e-store-pass}"
if [[ -z "${GOPROXY:-}" ]] && command -v go >/dev/null 2>&1; then
  export GOPROXY="$(go env GOPROXY)"
fi

compose() { docker compose --profile test "$@"; }
log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

build() {
  log "building images"
  compose build
}

up() {
  build
  log "writing the self-signed certificate (kept while the certs volume is)"
  docker compose run --rm certs
  log "starting ${ROLES[*]} and the mock upstream"
  docker compose up -d --wait upstream "${ROLES[@]}"
  cat <<EOF

Pages, over TLS with a self-signed certificate (user admin, password fit-e2e-pass):
  hub      https://127.0.0.1:19200
  node1    https://127.0.0.1:19101    node2 https://127.0.0.1:19102    node3 https://127.0.0.1:19103
  proxy1   https://127.0.0.1:19301    proxy2 https://127.0.0.1:19302
Proxy APIs (/v1):  proxy1 https://127.0.0.1:19401    proxy2 https://127.0.0.1:19402
Mock upstream:     http://127.0.0.1:18000 (mockllm)
EOF
}

down() {
  log "removing containers and data"
  docker compose --profile test --profile setup down -v --remove-orphans
}

stop() {
  log "stopping containers (data kept)"
  compose stop
}

# run_tester runs the compiled e2e test in the tester container.
run_tester() {
  compose run --rm --no-deps tester -test.v -test.count=1 -test.timeout=20m "$@"
}

# startup_checks: a fit whose store password is wrong, or missing, exits non-zero with an error instead of starting.
# fit keeps no lock file, so there is no second process to check for.
startup_checks() {
  log "startup checks: wrong store password, missing store password"
  local out
  if out=$(docker compose exec -T -e FIT_STORE_PASSWORD=wrong-password node1 fit run -c /etc/fit/node1.json 2>&1); then
    fail "node1's store opened with a wrong password"
  fi
  grep -q "does not open with the password" <<<"$out" || fail "wrong password: unexpected output: $out"
  if out=$(docker compose exec -T -e FIT_STORE_PASSWORD= node1 fit run -c /etc/fit/node1.json 2>&1); then
    fail "node1 started without its store password"
  fi
  grep -q "FIT_STORE_PASSWORD is empty or not set" <<<"$out" || fail "missing password: unexpected output: $out"
  echo "ok"
}

test_all() {
  down
  up
  log "phase 1: TestE2E (configure the topology through the pages, exercise every role)"
  run_tester -test.run "^TestE2E\$${GO_TEST_RUN:+/$GO_TEST_RUN}" || { docker compose logs --tail=80 "${ROLES[@]}"; fail "TestE2E"; }
  startup_checks
  log "restarting every role (graceful stop, config read back from the encrypted store)"
  docker compose restart "${ROLES[@]}"
  log "phase 2: TestPersist (what must survive a restart)"
  run_tester -test.run '^TestPersist$' || { docker compose logs --tail=80 "${ROLES[@]}"; fail "TestPersist"; }
  log "all end-to-end tests passed"
}

cmd="${1:-help}"
shift || true
case "$cmd" in
  build) build ;;
  up | start) up ;;
  test) test_all; echo; echo "The environment is still running; stop it with: $0 down" ;;
  all) trap down EXIT; test_all ;;
  stop) stop ;;
  down) down ;;
  ps) compose ps ;;
  logs) compose logs -f "$@" ;;
  *) sed -n '2,17p' "$0"; exit 2 ;;
esac
