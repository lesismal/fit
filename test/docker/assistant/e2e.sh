#!/usr/bin/env bash
# Start / stop the docker test of the assistant's model client, and run it.
#
#   test/docker/assistant/e2e.sh up      start Ollama and pull the model (kept in a volume)
#   test/docker/assistant/e2e.sh test    up, then run the test; Ollama keeps running
#   test/docker/assistant/e2e.sh all     like test, then remove the container (the model volume too)
#   test/docker/assistant/e2e.sh down    stop and remove the container and the model volume
#
# The test calls Ollama's /api/chat as kind ollama and its /v1/chat/completions as kind
# openai_compatible, each with a tool the model must call, then the tool's result handed back,
# and lists the models of both. Environment: OLLAMA_TAG (the image's tag, default 0.12.6),
# OLLAMA_PORT (the host port, default 21434), FIT_ASSISTANT_MODEL (a model that calls tools,
# default qwen2.5:1.5b). This is an ordinary interop test, not a load test: it makes a handful of
# calls.
set -euo pipefail

cd "$(dirname "$0")"
export OLLAMA_PORT="${OLLAMA_PORT:-21434}"
MODEL="${FIT_ASSISTANT_MODEL:-qwen2.5:1.5b}"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }

up() {
  log "starting Ollama on 127.0.0.1:${OLLAMA_PORT}"
  docker compose up -d --wait ollama
  log "pulling ${MODEL} (kept in the models volume)"
  docker compose exec -T ollama ollama pull "${MODEL}"
}

run() {
  log "running the test"
  (cd ../../.. && FIT_ASSISTANT_OLLAMA="http://127.0.0.1:${OLLAMA_PORT}" FIT_ASSISTANT_MODEL="${MODEL}" \
    go test -tags e2e -race -count=1 -v ./test/docker/assistant/e2e)
}

down() {
  log "removing the container and the model volume"
  docker compose down -v --remove-orphans
}

cmd="${1:-help}"
case "$cmd" in
  up) up ;;
  test) up; run ;;
  all) trap down EXIT; up; run ;;
  down) down ;;
  *) sed -n '2,15p' "$0"; exit 2 ;;
esac
