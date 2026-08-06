#!/usr/bin/env bash
# Capture live-cluster evidence for otel-nats feature-flag gates.
#
# Writes docs/evidence/live-*.{txt,json,yaml} and docs/evidence/live-summary.json.
# live-summary.json is the file the zh-TW HTML report reads, and THIS SCRIPT is
# what produces it — an earlier version emitted a thinner shape than the file
# that was committed, so the published evidence could not be reproduced from the
# repository.
#
# Three cases, all against a running cluster, changing nothing but the ConfigMap:
#   C1  option C baseline, flag enabled  → NATS spans present
#   C2  flag flipped to disabled         → business call still succeeds, NATS spans gone
#   C3  flag back to enabled             → NATS spans return
set -euo pipefail
export PATH="/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EVID="$ROOT/docs/evidence"
mkdir -p "$EVID"
NS="${NS:-demo}"
BACKEND_PF="${BACKEND_PF:-18080}"
RELAY_PF="${RELAY_PF:-11031}"
CH_POD="${CH_POD:-chi-clickhouse-cluster-default-0-0-0}"
CH_PASSWORD="${CH_PASSWORD:-demo-clickhouse-pw}"

# Convergence budget. The chain a flag change has to walk is kubelet ConfigMap
# mount refresh (often ~60s, occasionally longer) → relay proxy reload (1s poll)
# → backend provider poll (2s). The previous script slept a flat 75s and hoped;
# that is flaky when the kubelet is slow and wasteful when it is not, so every
# wait here polls for the condition it actually needs.
RELAY_TIMEOUT="${RELAY_TIMEOUT:-240}"   # seconds to wait for the relay to serve the new variation
CONVERGE_ATTEMPTS="${CONVERGE_ATTEMPTS:-30}"
CONVERGE_SLEEP="${CONVERGE_SLEEP:-6}"   # seconds between backend probes
SPAN_SETTLE="${SPAN_SETTLE:-6}"         # seconds for a trace to land in ClickHouse

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

cleanup() {
  [[ -n "${PF_BACKEND_PID:-}" ]] && kill "$PF_BACKEND_PID" 2>/dev/null || true
  [[ -n "${PF_RELAY_PID:-}" ]] && kill "$PF_RELAY_PID" 2>/dev/null || true
}
trap cleanup EXIT

# chq runs a query and returns its pretty-printed output, for the human-readable
# evidence files.
chq() {
  kubectl -n "$NS" exec "$CH_POD" -- \
    clickhouse-client --password "$CH_PASSWORD" -q "$1"
}

# chq_num runs a scalar query and returns just the number.
#
# TabSeparated, not a grep over a box-drawing table. The old script parsed
# `grep -A1 nats_count | tail -1 | tr -d '[:space:]'` out of PrettyCompact
# output, which silently yields an empty string — and therefore a default — the
# moment the formatting changes.
chq_num() {
  kubectl -n "$NS" exec "$CH_POD" -- \
    clickhouse-client --password "$CH_PASSWORD" --format=TabSeparated -q "$1" |
    tr -d '[:space:]'
}

nats_count_for_trace() {
  chq_num "SELECT count() FROM otel.otel_traces WHERE TraceId = '$1' AND SpanName LIKE '%demo.trace%'"
}

http_count_for_trace() {
  chq_num "SELECT count() FROM otel.otel_traces WHERE TraceId = '$1' AND SpanName LIKE 'POST%'"
}

# nats_count_recent counts NATS spans across ALL traces in the last N minutes.
#
# This is what makes C2 an assertion rather than an ambiguity. "Zero NATS spans
# on this trace ID" is equally consistent with tracing being disabled and with
# trace propagation being broken so the spans landed under a different trace.
# Counting them cluster-wide over a window tells the two apart.
nats_count_recent() {
  chq_num "SELECT count() FROM otel.otel_traces WHERE SpanName LIKE '%demo.trace%' AND Timestamp > now() - INTERVAL $1 MINUTE"
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
  kubectl apply -f "$out" >"$EVID/live-configmap-apply-${variation}.txt" 2>&1
  cat "$EVID/live-configmap-apply-${variation}.txt" >&2
}

# relay_eval asks the relay proxy what it would answer for the module key.
#
# The targeting key is derived from the backend pod name because otel-flags
# supplies "<hostname>-<pid>" and a container's hostname IS its pod name. It is
# still only an approximation of what the backend sends, so with a percentage or
# progressiveRollout rule this answer would NOT stand in for the backend's. The
# authoritative per-process evidence is the span count below; this call records
# what the control plane believes.
relay_eval() {
  local label="$1" key="$2"
  local f="$EVID/live-relay-eval-${label}.json"
  curl -sS -X POST "http://127.0.0.1:${RELAY_PF}/v1/feature/otel-nats-tracing/eval" \
    -H 'Content-Type: application/json' \
    -d "{\"user\":{\"key\":\"${key}\"}}" >"$f"
  cat "$f"
}

# wait_for_relay polls until the relay proxy serves the variation just applied,
# so the backend-side convergence loop does not start against a stale ConfigMap.
wait_for_relay() {
  local want="$1" key="$2" deadline
  deadline=$(( $(date +%s) + RELAY_TIMEOUT ))
  log "waiting for relay to serve variation=${want} (timeout ${RELAY_TIMEOUT}s)"
  while (( $(date +%s) < deadline )); do
    local got
    got=$(curl -sS -X POST "http://127.0.0.1:${RELAY_PF}/v1/feature/otel-nats-tracing/eval" \
      -H 'Content-Type: application/json' -d "{\"user\":{\"key\":\"${key}\"}}" 2>/dev/null |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("variationType",""))' 2>/dev/null || true)
    if [[ "$got" == "$want" ]]; then
      log "relay now serving variation=${got}"
      return 0
    fi
    sleep 5
  done
  log "WARNING: relay did not serve variation=${want} within ${RELAY_TIMEOUT}s; continuing so the failure is recorded"
  return 1
}

demo_trace() {
  local label="$1"
  local f="$EVID/live-step-${label}-api.json"
  curl -sS -X POST "http://127.0.0.1:${BACKEND_PF}/api/demo-trace" >"$f"
  python3 -c "import json; print(json.load(open('$f'))['traceId'])"
}

# query_spans writes the human-readable per-case ClickHouse evidence.
query_spans() {
  local label="$1" tid="$2" nats="$3" http="$4" recent="$5"
  local f="$EVID/live-clickhouse-${label}.txt"
  {
    echo "TraceId=$tid"
    chq "SELECT SpanName, SpanKind, ServiceName FROM otel.otel_traces WHERE TraceId = '$tid' ORDER BY Timestamp FORMAT PrettyCompactMonoBlock"
    echo "nats_count=$nats"
    echo "http_count=$http"
    echo "nats_spans_cluster_wide_last_2min=$recent"
    echo "--- recent 15 ---"
    chq "SELECT substring(TraceId,1,12) AS t, SpanName, SpanKind FROM otel.otel_traces WHERE Timestamp > now() - INTERVAL 10 MINUTE ORDER BY Timestamp DESC LIMIT 15 FORMAT PrettyCompactMonoBlock"
  } >"$f" 2>&1
  cat "$f" >&2
}

# run_case drives one flag state to convergence and records the evidence.
#
# expect is "gt0" (NATS spans must be present) or "eq0" (they must be gone). The
# loop retries the whole probe — request, then count — instead of sleeping a
# fixed interval and accepting whatever it finds, so a slow ConfigMap mount
# costs time rather than a false negative. A case that never converges records
# its last attempt with pass=false.
run_case() {
  local id="$1" title="$2" variation="$3" expect="$4" key="$5"
  local label
  label="$(echo "$id" | tr '[:upper:]' '[:lower:]')-${variation}"

  log "CASE ${id}: ConfigMap → ${variation} (expect nats ${expect})"
  set_flags_cm "$variation"
  wait_for_relay "$variation" "$key" || true
  relay_eval "$label" "$key" >/dev/null

  local attempt tid nats http recent pass=false
  for (( attempt = 1; attempt <= CONVERGE_ATTEMPTS; attempt++ )); do
    tid="$(demo_trace "$label")"
    sleep "$SPAN_SETTLE"
    nats="$(nats_count_for_trace "$tid")"
    http="$(http_count_for_trace "$tid")"
    recent="$(nats_count_recent 2)"
    log "  attempt ${attempt}/${CONVERGE_ATTEMPTS}: trace=${tid} nats=${nats} http=${http} cluster_recent=${recent}"

    if [[ "$expect" == "gt0" && "${nats:-0}" -gt 0 && "${http:-0}" -gt 0 ]]; then
      pass=true; break
    fi
    # For the disabled case, "gone" means gone everywhere, not merely absent from
    # this trace ID — see nats_count_recent.
    if [[ "$expect" == "eq0" && "${nats:-1}" -eq 0 && "${http:-0}" -gt 0 && "${recent:-1}" -eq 0 ]]; then
      pass=true; break
    fi
    sleep "$CONVERGE_SLEEP"
  done

  query_spans "$label" "$tid" "$nats" "$http" "$recent"

  ID="$id" TITLE="$title" LABEL="$label" PASS="$pass" TID="$tid" \
  NATS="${nats:-0}" HTTP="${http:-0}" RECENT="${recent:-0}" ATTEMPTS="$attempt" \
  EVID="$EVID" python3 - <<'PY'
import json, os

evid = os.environ["EVID"]
label = os.environ["LABEL"]

def read_json(path, default=None):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except Exception:
        return default

def read_text(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return fh.read()
    except Exception:
        return ""

case = {
    "id": os.environ["ID"],
    "title": os.environ["TITLE"],
    "pass": os.environ["PASS"] == "true",
    "traceId": os.environ["TID"],
    "attempts": int(os.environ["ATTEMPTS"]),
    "api": read_json(f"{evid}/live-step-{label}-api.json", {}),
    "relay_eval": read_json(f"{evid}/live-relay-eval-{label}.json", {}),
    "relay_eval_note": (
        "targeting key 由 backend pod 名稱推導（otel-flags 送的是 <hostname>-<pid>）。"
        "目前 defaultRule 是 STATIC，兩者結果相同；若改用 percentage / progressiveRollout 規則，"
        "此回應就不能代表 backend 實際看到的值 — 以 ClickHouse span 數為準。"
    ),
    "nats_count": int(os.environ["NATS"] or 0),
    "http_count": int(os.environ["HTTP"] or 0),
    "nats_spans_cluster_wide_last_2min": int(os.environ["RECENT"] or 0),
    "clickhouse": read_text(f"{evid}/live-clickhouse-{label}.txt"),
    "files": {
        "apply": f"live-configmap-apply-{label.split('-', 1)[1]}.txt",
        "relay": f"live-relay-eval-{label}.json",
        "api": f"live-step-{label}-api.json",
        "clickhouse": f"live-clickhouse-{label}.txt",
    },
}
with open(f"{evid}/live-case-{case['id']}.json", "w", encoding="utf-8") as fh:
    json.dump(case, fh, ensure_ascii=False, indent=2)
PY
}

# --- start ---
log "context=$(kubectl config current-context)"
kubectl -n "$NS" get pods -o wide >"$EVID/live-pods.txt" 2>&1
cat "$EVID/live-pods.txt" >&2

BACKEND_POD="$(kubectl -n "$NS" get pods -l app=backend -o jsonpath='{.items[0].metadata.name}')"
# otel-flags builds its targeting key as "<hostname>-<pid>", and in a container
# the hostname is the pod name. PID 1 is the usual single-process container.
TARGET_KEY="${TARGET_KEY:-${BACKEND_POD}-1}"
log "backend pod=${BACKEND_POD} derived targeting key=${TARGET_KEY}"

# live-baseline-env.txt is read by render-flag-matrix-html.py. It used to be a
# committed file that no script produced, so the report's "cluster baseline"
# block could not be regenerated from the repository — the same gap as
# live-summary.json.
kubectl -n "$NS" get deploy backend \
  -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
  >"$EVID/live-baseline-env.txt"

{
  echo "=== backend Deployment env ==="
  cat "$EVID/live-baseline-env.txt"
  echo
  echo "=== relay-proxy volumes ==="
  kubectl -n "$NS" get deploy relay-proxy -o jsonpath='{.spec.template.spec.volumes}' | python3 -m json.tool
  echo
  echo "=== current ConfigMap flags.yaml ==="
  kubectl -n "$NS" get cm demo-feature-flags -o jsonpath='{.data.flags\.yaml}'
  echo
  echo "=== derived targeting key ==="
  echo "$TARGET_KEY"
} >"$EVID/live-baseline-config.txt" 2>&1

kubectl -n "$NS" port-forward svc/backend "$BACKEND_PF:8080" >"$EVID/pf-backend.log" 2>&1 &
PF_BACKEND_PID=$!
kubectl -n "$NS" port-forward svc/relay-proxy "$RELAY_PF:1031" >"$EVID/pf-relay.log" 2>&1 &
PF_RELAY_PID=$!
sleep 3

curl -sS "http://127.0.0.1:${BACKEND_PF}/healthz" >"$EVID/live-healthz.json"
cat "$EVID/live-healthz.json" >&2
echo >&2

run_case C1 "option C 預設：env=false + ConfigMap enabled → 有 NATS span" enabled  gt0 "$TARGET_KEY"
run_case C2 "ConfigMap → disabled：HTTP 成功、NATS span=0（全叢集時間窗亦為 0）" disabled eq0 "$TARGET_KEY"
run_case C3 "ConfigMap → enabled：NATS span 恢復" enabled gt0 "$TARGET_KEY"

# Assemble the summary the report reads. Built in Python from the per-case files
# rather than echoed as bash string fragments, so the committed artifact and the
# script that produces it cannot drift apart.
NS="$NS" EVID="$EVID" CLUSTER="$(kubectl config current-context)" \
BACKEND_ENV="$(kubectl -n "$NS" get deploy backend -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}')" \
python3 - <<'PY'
import json, os, datetime

evid = os.environ["EVID"]

backend_env = {}
for line in os.environ.get("BACKEND_ENV", "").splitlines():
    if "=" in line:
        k, v = line.split("=", 1)
        backend_env[k] = v

cases = []
for cid in ("C1", "C2", "C3"):
    path = f"{evid}/live-case-{cid}.json"
    if os.path.exists(path):
        with open(path, encoding="utf-8") as fh:
            cases.append(json.load(fh))

summary = {
    "generated_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "cluster": os.environ.get("CLUSTER", ""),
    "namespace": os.environ["NS"],
    "backend_env": backend_env,
    "note": (
        "每個 case 都是輪詢到條件成立（或用盡重試）才記錄，不是固定 sleep。"
        "C2 額外查全叢集最近 2 分鐘的 demo.trace span 數，用來區分「tracing 被關掉」與「propagation 壞掉、span 跑到別條 trace」。"
    ),
    "cases": cases,
    "passed": sum(1 for c in cases if c["pass"]),
    "total": len(cases),
}

with open(f"{evid}/live-summary.json", "w", encoding="utf-8") as fh:
    json.dump(summary, fh, ensure_ascii=False, indent=2)
print(json.dumps({k: v for k, v in summary.items() if k != "cases"}, ensure_ascii=False, indent=2))
PY

log "done — evidence in $EVID"
