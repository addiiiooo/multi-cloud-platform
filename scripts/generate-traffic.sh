#!/usr/bin/env bash
#
# generate-traffic.sh
#
# Sends a steady mix of requests to a locally running pulse-api so the
# Grafana dashboard (observability/README.md) has something to show:
# reads, writes, client errors (400) and unknown paths (404, recorded under
# the bounded "unmatched" label). pulse-api has no route that returns 5xx on
# demand, so the 5xx panels stay at zero; alerts_test.yml covers that path.
#
#   scripts/generate-traffic.sh                # 60 s against localhost:8080
#   DURATION=300 scripts/generate-traffic.sh
#   BASE_URL=http://localhost:9000 scripts/generate-traffic.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
DURATION="${DURATION:-60}"

log() { printf '[generate-traffic] %s\n' "$1"; }

if ! curl -fsS -o /dev/null "$BASE_URL/healthz"; then
  log "pulse-api is not answering at $BASE_URL/healthz; start it first with: docker compose --profile observability up --build"
  exit 1
fi

log "sending traffic to $BASE_URL for ${DURATION}s (Ctrl-C to stop)"
end=$((SECONDS + DURATION))
sent=0
while ((SECONDS < end)); do
  # Roughly 10 requests/s: 5 reads, 3 writes, 1 bad write, 1 unknown path.
  for _ in 1 2 3 4 5; do
    curl -s -o /dev/null "$BASE_URL/api/v1/pulses"
  done
  for _ in 1 2 3; do
    curl -s -o /dev/null -X POST -H 'Content-Type: application/json' \
      -d "{\"message\":\"load-test $sent\"}" "$BASE_URL/api/v1/pulses"
  done
  curl -s -o /dev/null -X POST -H 'Content-Type: application/json' \
    -d '{"not_message":true}' "$BASE_URL/api/v1/pulses"
  curl -s -o /dev/null "$BASE_URL/does-not-exist"
  sent=$((sent + 10))
  sleep 1
done
log "done: $sent requests sent"
