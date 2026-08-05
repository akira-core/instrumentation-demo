#!/usr/bin/env bash
# Capture live-cluster evidence for otel-nats feature-flag gates.
# Writes files under docs/evidence/live-*.txt and live-summary.json
set -euo pipefail
export PATH="/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EVID="$ROOT/docs/evidence"
mkdir -p "$EVID"
NS=demo
BACKEND_PF=18080
RELAY_PF=11031

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
  [[ -n "${PF_BACKEND_PID:-}" ]] && kill "$PF_BACKEND_PID" 2>/dev/null || true
  [[ -n "${PF_RELAY_PID:-}" ]] && kill "$PF_RELAY_PID" 2>/dev/null || true
}
trap cleanup EXIT

chq() {
  kubectl -n "$NS" exec chi-clickhouse-cluster-default-0-0-0 -- \
    clickhouse-client --password demo-clickhouse-pw -q "$1"
}

set_flags_cm() {
  local variation="$1"
  local out="$EVID/live-configmap-${variation}.yaml"
  cat >"$out" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-feature-flags
  namespace: ${NS}
data:
  flags.yaml: |
    otel-nats-tracing:
      variations:
        enabled: true
        disabled: false
      defaultRule:
        variation: ${variation}
EOF
  kubectl apply -f "$out" | tee "$EVID/live-configmap-apply-${variation}.txt"
}

wait_mount_and_poll() {
  # kubelet ConfigMap mount refresh is often ~60s; GOFF poll 1s; provider poll 2s
  local secs="${1:-75}"
  log "waiting ${secs}s for ConfigMap mount + GOFF + provider poll..."
  sleep "$secs"
}

relay_eval() {
  local label="$1"
  local f="$EVID/live-relay-eval-${label}.json"
  curl -sS -X POST "http://127.0.0.1:${RELAY_PF}/v1/feature/otel-nats-tracing/eval" \
    -H 'Content-Type: application/json' \
    -d '{"user":{"key":"demo-evidence"}}' | tee "$f"
  echo
}

demo_trace() {
  local label="$1"
  local f="$EVID/live-step-${label}-api.json"
  curl -sS -X POST "http://127.0.0.1:${BACKEND_PF}/api/demo-trace" | tee "$f"
  echo >&2
  python3 -c "import json; print(json.load(open('$f'))['traceId'])"
}

query_spans() {
  local label="$1"
  local tid="$2"
  local f="$EVID/live-clickhouse-${label}.txt"
  {
    echo "TraceId=$tid"
    echo "--- spans ---"
    chq "SELECT SpanName, SpanKind, ServiceName FROM otel.otel_traces WHERE TraceId = '$tid' ORDER BY Timestamp FORMAT PrettyCompactMonoBlock"
    echo "--- nats_count ---"
    chq "SELECT count() FROM otel.otel_traces WHERE TraceId = '$tid' AND SpanName LIKE '%demo.trace%'"
    echo "--- http_count ---"
    chq "SELECT count() FROM otel.otel_traces WHERE TraceId = '$tid' AND SpanName LIKE 'POST%'"
  } | tee "$f"
}

# --- start ---
log "context=$(kubectl config current-context)"
kubectl -n "$NS" get pods -o wide | tee "$EVID/live-pods.txt"
{
  echo "=== backend Deployment env ==="
  kubectl -n "$NS" get deploy backend -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}'
  echo
  echo "=== relay-proxy volumes ==="
  kubectl -n "$NS" get deploy relay-proxy -o jsonpath='{.spec.template.spec.volumes}' | python3 -m json.tool
  echo
  echo "=== current ConfigMap flags.yaml ==="
  kubectl -n "$NS" get cm demo-feature-flags -o jsonpath='{.data.flags\.yaml}'
  echo
} | tee "$EVID/live-baseline-config.txt"

# port-forwards
kubectl -n "$NS" port-forward svc/backend "$BACKEND_PF:8080" >"$EVID/pf-backend.log" 2>&1 &
PF_BACKEND_PID=$!
kubectl -n "$NS" port-forward svc/relay-proxy "$RELAY_PF:1031" >"$EVID/pf-relay.log" 2>&1 &
PF_RELAY_PID=$!
sleep 2

# health
curl -sS "http://127.0.0.1:${BACKEND_PF}/healthz" | tee "$EVID/live-healthz.json"
echo

declare -a SUMMARY=()

# Case C1: option C happy path — env false, ConfigMap enabled
log "CASE C1: option C baseline (ConfigMap enabled)"
set_flags_cm enabled
wait_mount_and_poll 75
relay_eval c1-enabled
TID=$(demo_trace c1-enabled)
sleep 6
query_spans c1-enabled "$TID"
NATS=$(grep -A1 nats_count "$EVID/live-clickhouse-c1-enabled.txt" | tail -1 | tr -d '[:space:]')
HTTP=$(grep -A1 http_count "$EVID/live-clickhouse-c1-enabled.txt" | tail -1 | tr -d '[:space:]')
if [[ "${NATS:-0}" -gt 0 && "${HTTP:-0}" -gt 0 ]]; then
  SUMMARY+=('{"id":"C1","title":"option C 預設：env=false + ConfigMap enabled","pass":true,"nats":'"$NATS"',"http":'"$HTTP"',"traceId":"'"$TID"'"}')
else
  SUMMARY+=('{"id":"C1","title":"option C 預設：env=false + ConfigMap enabled","pass":false,"nats":'"${NATS:-0}"',"http":'"${HTTP:-0}"',"traceId":"'"$TID"'"}')
fi

# Case C2: disable via ConfigMap
log "CASE C2: ConfigMap disabled"
set_flags_cm disabled
wait_mount_and_poll 75
relay_eval c2-disabled
TID=$(demo_trace c2-disabled)
sleep 6
query_spans c2-disabled "$TID"
NATS=$(grep -A1 nats_count "$EVID/live-clickhouse-c2-disabled.txt" | tail -1 | tr -d '[:space:]')
HTTP=$(grep -A1 http_count "$EVID/live-clickhouse-c2-disabled.txt" | tail -1 | tr -d '[:space:]')
if [[ "${NATS:-1}" -eq 0 && "${HTTP:-0}" -gt 0 ]]; then
  SUMMARY+=('{"id":"C2","title":"ConfigMap flip → disabled：業務仍成功、無 NATS span","pass":true,"nats":'"$NATS"',"http":'"$HTTP"',"traceId":"'"$TID"'"}')
else
  SUMMARY+=('{"id":"C2","title":"ConfigMap flip → disabled：業務仍成功、無 NATS span","pass":false,"nats":'"${NATS:-x}"',"http":'"${HTTP:-x}"',"traceId":"'"$TID"'"}')
fi

# Case C3: re-enable
log "CASE C3: ConfigMap re-enabled"
set_flags_cm enabled
wait_mount_and_poll 75
relay_eval c3-enabled
TID=$(demo_trace c3-enabled)
sleep 6
query_spans c3-enabled "$TID"
NATS=$(grep -A1 nats_count "$EVID/live-clickhouse-c3-enabled.txt" | tail -1 | tr -d '[:space:]')
HTTP=$(grep -A1 http_count "$EVID/live-clickhouse-c3-enabled.txt" | tail -1 | tr -d '[:space:]')
if [[ "${NATS:-0}" -gt 0 && "${HTTP:-0}" -gt 0 ]]; then
  SUMMARY+=('{"id":"C3","title":"ConfigMap 恢復 enabled：NATS span 回來","pass":true,"nats":'"$NATS"',"http":'"$HTTP"',"traceId":"'"$TID"'"}')
else
  SUMMARY+=('{"id":"C3","title":"ConfigMap 恢復 enabled：NATS span 回來","pass":false,"nats":'"${NATS:-0}"',"http":'"${HTTP:-0}"',"traceId":"'"$TID"'"}')
fi

# Write summary JSON
{
  echo '{'
  echo "  \"generated_at\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
  echo '  "cluster": "'"$(kubectl config current-context)"'",'
  echo '  "namespace": "'"$NS"'",'
  echo '  "backend_env": {'
  kubectl -n "$NS" get deploy backend -o jsonpath='{range .spec.template.spec.containers[0].env[*]}    "{.name}": "{.value}",{"\n"}{end}' | sed '$ s/,$//'
  echo '  },'
  echo '  "cases": ['
  printf '    %s\n' "${SUMMARY[0]}"
  for ((i=1; i<${#SUMMARY[@]}; i++)); do
    printf '    ,%s\n' "${SUMMARY[$i]}"
  done
  echo '  ]'
  echo '}'
} | tee "$EVID/live-summary.json"

log "done — evidence in $EVID"
