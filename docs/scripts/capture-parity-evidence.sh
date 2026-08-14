#!/usr/bin/env bash
# Capture cross-runtime SPAN-SHAPE parity evidence for otel-nats.
#
# This is the companion to capture-live-evidence.sh, and it answers a different
# question. That script proves one ConfigMap flip governs both runtimes — it
# counts spans. This script proves the two libraries EMIT THE SAME SHAPE for the
# same messaging operation: same span name, same span kind, same attribute keys,
# same attribute values where they must agree.
#
# Counting spans cannot catch a naming or attribute drift between the Go and JS
# implementations, and a drift is exactly what happens when one side ships a
# semconv change the other has not picked up yet (Go 0.9.0 renamed `send
# {subject}` to `publish {subject}`; a count-only check stayed green through it).
#
# Writes docs/evidence/parity-*.{txt,json} and docs/evidence/parity-summary.json.
#
# What the demo path can and cannot cover
# ---------------------------------------
# The demo exercises core-NATS publish + push-subscribe on BOTH runtimes, so
# those are compared span-for-span here. It has no request/reply RPC, no
# wildcard subscription and no JetStream, so span names and attributes on those
# paths are NOT covered by this script — they are covered by each library's own
# test suite. See docs/parity.md for the full matrix and what backs each row.
set -euo pipefail
export PATH="/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin:$PATH"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
EVID="$ROOT/docs/evidence"
mkdir -p "$EVID"
NS="${NS:-demo}"
CH_POD="${CH_POD:-chi-clickhouse-cluster-default-0-0-0}"
CH_PASSWORD="${CH_PASSWORD:-demo-clickhouse-pw}"
SPAN_SETTLE="${SPAN_SETTLE:-6}"          # seconds between ClickHouse polls
SPAN_ATTEMPTS="${SPAN_ATTEMPTS:-20}"     # polls before giving up on a span landing

# A freshly started js-service resolves its first flag lookup against a local
# snapshot while the real evaluation is scheduled, so the FIRST request after a
# pod start can produce incomplete JS spans. That window is fail-safe (it can
# delay an enable, never introduce one) and is not a defect — but it does make a
# first-request capture unrepresentative, so warm up before measuring.
WARMUP_REQUESTS="${WARMUP_REQUESTS:-2}"
WARMUP_SETTLE="${WARMUP_SETTLE:-6}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "FATAL: $*"; exit 1; }

# chq_json runs a query and returns JSONEachRow, one JSON object per line.
chq_json() {
  kubectl -n "$NS" exec "$CH_POD" -- \
    clickhouse-client --password "$CH_PASSWORD" --format=JSONEachRow -q "$1"
}

# trigger fires one demo round trip from inside the cluster (no port-forward to
# race) and echoes the trace id the backend reports.
trigger() {
  kubectl -n "$NS" exec deploy/nats-box -- \
    sh -c 'curl -s -X POST http://backend:8080/api/demo-trace -H "Content-Type: application/json" -d "{}"'
}

# SPAN_COLUMNS is the projection every query below shares. Keep it one string so
# a column added for one runtime cannot silently be missing for the other.
#
# link_span_ids is what makes the consumer spans findable at all: both runtimes'
# consumer spans start a NEW trace (JS since otel-nats 0.3.0), so neither can be
# reached by trace id from the publisher's side.
SPAN_COLUMNS="
  ServiceName,
  SpanName,
  SpanKind,
  TraceId,
  SpanId,
  ParentSpanId,
  arraySort(mapKeys(SpanAttributes)) AS attr_keys,
  SpanAttributes['messaging.system']           AS m_system,
  SpanAttributes['messaging.operation.name']   AS m_op_name,
  SpanAttributes['messaging.operation.type']   AS m_op_type,
  SpanAttributes['messaging.destination.name'] AS m_dest,
  Links.SpanId  AS link_span_ids,
  Links.TraceId AS link_trace_ids
"

# wait_for_span polls until a query returns at least one row, then emits the
# first row. $1 = human label, $2 = WHERE clause.
wait_for_span() {
  local label="$1" where="$2" i out
  for ((i = 1; i <= SPAN_ATTEMPTS; i++)); do
    out="$(chq_json "SELECT $SPAN_COLUMNS FROM otel.otel_traces WHERE $where ORDER BY Timestamp LIMIT 1" || true)"
    if [[ -n "$out" ]]; then
      log "found $label (attempt $i)"
      printf '%s\n' "$out"
      return 0
    fi
    sleep "$SPAN_SETTLE"
  done
  die "no span found for '$label' after $SPAN_ATTEMPTS attempts — WHERE $where"
}

# ---------------------------------------------------------------------------
# Assertions
# ---------------------------------------------------------------------------
PASS=0
FAIL=0
RESULTS_JSON="[]"

# record appends one assertion result. $1 id, $2 dimension, $3 ok(true|false),
# $4 go value, $5 js value, $6 note.
record() {
  local id="$1" dim="$2" ok="$3" gov="$4" jsv="$5" note="$6"
  if [[ "$ok" == "true" ]]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); fi
  RESULTS_JSON="$(jq \
    --arg id "$id" --arg dim "$dim" --argjson ok "$ok" \
    --arg gov "$gov" --arg jsv "$jsv" --arg note "$note" \
    '. + [{id: $id, dimension: $dim, pass: $ok, go: $gov, js: $jsv, note: $note}]' \
    <<<"$RESULTS_JSON")"
  printf '%s  %-4s %-46s go=%-34s js=%s\n' \
    "$([[ "$ok" == "true" ]] && echo 'PASS' || echo 'FAIL')" "$id" "$dim" "$gov" "$jsv"
}

# assert_same records a pass when the two runtimes agree on one value.
assert_same() {
  local id="$1" dim="$2" gov="$3" jsv="$4" note="${5:-}"
  local ok=false
  [[ "$gov" == "$jsv" ]] && ok=true
  record "$id" "$dim" "$ok" "$gov" "$jsv" "$note"
}

# ---------------------------------------------------------------------------
log "warming up ($WARMUP_REQUESTS requests) — a just-started js-service resolves its first flag lookup locally"
for ((i = 0; i < WARMUP_REQUESTS; i++)); do trigger >/dev/null; sleep 1; done
sleep "$WARMUP_SETTLE"

log "triggering the measured round trip"
API="$(trigger)"
TID="$(jq -r '.traceId' <<<"$API")"
[[ -n "$TID" && "$TID" != "null" ]] || die "backend returned no traceId: $API"
log "traceId=$TID"

# Go side -------------------------------------------------------------------
GO_PUB="$(wait_for_span "go publish demo.trace.request" \
  "TraceId = '$TID' AND ServiceName = 'demo-backend' AND m_dest = 'demo.trace.request' AND SpanKind = 'Producer'")"
GO_PUB_SPANID="$(jq -r '.SpanId' <<<"$GO_PUB")"

# The Go consumer span is NOT on $TID — otelnats starts it at context.Background()
# and records the producer as a link. Reach it through that link.
GO_SUB="$(wait_for_span "go process demo.trace.request" \
  "ServiceName = 'demo-backend' AND SpanKind = 'Consumer' AND m_dest = 'demo.trace.request' AND has(Links.SpanId, '$GO_PUB_SPANID')")"

# JS side -------------------------------------------------------------------
# Since @akira-core/otel-nats 0.3.0 the JS consumer span has the SAME topology
# as Go: a new-trace root linked to the producer. Reach it through the link,
# exactly like the Go consumer above. The js-service self-loop publish
# (demo.trace.js) is a child of that consumer span, so it lives on the
# consumer's new trace, not on $TID.
JS_SUB="$(wait_for_span "js process demo.trace.request" \
  "ServiceName = 'demo-js-service' AND SpanKind = 'Consumer' AND m_dest = 'demo.trace.request' AND has(Links.SpanId, '$GO_PUB_SPANID')")"
JS_SUB_TRACEID="$(jq -r '.TraceId' <<<"$JS_SUB")"
JS_PUB="$(wait_for_span "js publish demo.trace.js" \
  "TraceId = '$JS_SUB_TRACEID' AND ServiceName = 'demo-js-service' AND SpanKind = 'Producer' AND m_dest = 'demo.trace.js'")"

g() { jq -r "$2" <<<"$1"; }

echo
echo "=== Span shape parity: core NATS publish ==="
assert_same P01 "publish span name is operation-first"  \
  "$(g "$GO_PUB" '.SpanName' | sed 's/demo\.trace\.request/{subject}/')" \
  "$(g "$JS_PUB" '.SpanName' | sed 's/demo\.trace\.js/{subject}/')" \
  "semconv v1.39.0 '{messaging.operation.name} {destination}'; Go <0.9.0 emitted 'send {subject}'"
assert_same P02 "publish span kind"                     "$(g "$GO_PUB" '.SpanKind')"   "$(g "$JS_PUB" '.SpanKind')"   ""
assert_same P03 "publish messaging.system"              "$(g "$GO_PUB" '.m_system')"   "$(g "$JS_PUB" '.m_system')"   ""
assert_same P04 "publish messaging.operation.name"      "$(g "$GO_PUB" '.m_op_name')"  "$(g "$JS_PUB" '.m_op_name')"  ""
assert_same P05 "publish messaging.operation.type"      "$(g "$GO_PUB" '.m_op_type')"  "$(g "$JS_PUB" '.m_op_type')"  ""
assert_same P06 "publish attribute key set"             "$(g "$GO_PUB" '.attr_keys|join(",")')" "$(g "$JS_PUB" '.attr_keys|join(",")')" ""

echo
echo "=== Span shape parity: core NATS push-subscribe ==="
assert_same P07 "process span name is operation-first"  \
  "$(g "$GO_SUB" '.SpanName' | sed 's/demo\.trace\.request/{subject}/')" \
  "$(g "$JS_SUB" '.SpanName' | sed 's/demo\.trace\.request/{subject}/')" ""
assert_same P08 "process span kind"                     "$(g "$GO_SUB" '.SpanKind')"   "$(g "$JS_SUB" '.SpanKind')"   ""
assert_same P09 "process messaging.system"              "$(g "$GO_SUB" '.m_system')"   "$(g "$JS_SUB" '.m_system')"   ""
assert_same P10 "process messaging.operation.name"      "$(g "$GO_SUB" '.m_op_name')"  "$(g "$JS_SUB" '.m_op_name')"  ""
assert_same P11 "process messaging.operation.type"      "$(g "$GO_SUB" '.m_op_type')"  "$(g "$JS_SUB" '.m_op_type')"  ""
assert_same P12 "process attribute key set"             "$(g "$GO_SUB" '.attr_keys|join(",")')" "$(g "$JS_SUB" '.attr_keys|join(",")')" ""

echo
echo "=== Consumer-span topology ==="
# Historically the JS consumer span was a CHILD on the producer's trace with no
# link (the divergence these rows were written to expose). JS otel-nats 0.3.0
# adopted the Go topology — new-trace root plus a producer link — so these rows
# now assert convergence and are expected to PASS.
GO_SUB_ROOTED=$([[ -z "$(g "$GO_SUB" '.ParentSpanId')" ]] && echo "new-trace-root" || echo "child-of-producer")
JS_SUB_ROOTED=$([[ -z "$(g "$JS_SUB" '.ParentSpanId')" ]] && echo "new-trace-root" || echo "child-of-producer")
assert_same D01 "consumer span parenting"  "$GO_SUB_ROOTED" "$JS_SUB_ROOTED" \
  "both runtimes start the consumer span from a background/ROOT context (Go: otelnats wrapMsgHandler; JS: internal/consumer-context.ts, 0.3.0+)"

GO_SUB_LINKS="$(g "$GO_SUB" '.link_span_ids|length')"
JS_SUB_LINKS="$(g "$JS_SUB" '.link_span_ids|length')"
assert_same D02 "consumer span link count"  "$GO_SUB_LINKS" "$JS_SUB_LINKS" \
  "both runtimes record the producer as exactly one span link"

# ---------------------------------------------------------------------------
{
  echo "traceId=$TID"
  echo
  echo "--- every span on the publisher's trace ---"
  kubectl -n "$NS" exec "$CH_POD" -- clickhouse-client --password "$CH_PASSWORD" -q "
    SELECT ServiceName, SpanName, SpanKind,
           substring(SpanId,1,8) AS span, substring(ParentSpanId,1,8) AS parent,
           length(Links.SpanId) AS links
    FROM otel.otel_traces WHERE TraceId = '$TID'
    ORDER BY Timestamp FORMAT PrettyCompactMonoBlock"
  echo
  echo "--- both consumer spans, reached through their producer link (each on its own trace) ---"
  kubectl -n "$NS" exec "$CH_POD" -- clickhouse-client --password "$CH_PASSWORD" -q "
    SELECT ServiceName, SpanName, SpanKind,
           substring(TraceId,1,10) AS trace, substring(SpanId,1,8) AS span,
           substring(ParentSpanId,1,8) AS parent,
           arrayMap(x -> substring(x,1,8), Links.SpanId) AS link_spans
    FROM otel.otel_traces
    WHERE SpanKind = 'Consumer' AND has(Links.SpanId, '$GO_PUB_SPANID')
    ORDER BY ServiceName FORMAT PrettyCompactMonoBlock"
  echo
  echo "--- the js-service self-loop, on the JS consumer's new trace ---"
  kubectl -n "$NS" exec "$CH_POD" -- clickhouse-client --password "$CH_PASSWORD" -q "
    SELECT ServiceName, SpanName, SpanKind,
           substring(TraceId,1,10) AS trace, substring(SpanId,1,8) AS span,
           substring(ParentSpanId,1,8) AS parent,
           length(Links.SpanId) AS links
    FROM otel.otel_traces
    WHERE TraceId = '$JS_SUB_TRACEID'
    ORDER BY Timestamp FORMAT PrettyCompactMonoBlock"
} >"$EVID/parity-spans.txt" 2>&1
log "wrote $EVID/parity-spans.txt"

for name in GO_PUB GO_SUB JS_PUB JS_SUB; do
  printf '%s\n' "${!name}" | jq . >"$EVID/parity-span-$(tr '[:upper:]' '[:lower:]' <<<"${name/_/-}").json"
done

jq -n \
  --arg tid "$TID" \
  --arg cluster "$(kubectl config current-context)" \
  --arg ns "$NS" \
  --arg go_ver "$(sed -n 's/.*instrumentationVersion = "\(.*\)".*/\1/p' "$ROOT/third_party/instrumentation-go/otel-nats/otelnats/conn.go" | head -1)" \
  --arg js_ver "$(jq -r .version "$ROOT/third_party/instrumentation-js/packages/otel-nats/package.json")" \
  --argjson pass "$PASS" --argjson fail "$FAIL" \
  --argjson results "$RESULTS_JSON" \
  '{
     generated_for_trace: $tid,
     cluster: $cluster,
     namespace: $ns,
     versions: { go_otel_nats: $go_ver, js_otel_nats: $js_ver },
     totals: { pass: $pass, fail: $fail },
     scope: "core NATS publish + push-subscribe only — the demo path has no request/reply RPC, no wildcard subscription and no JetStream; those rows of the matrix are backed by each library'"'"'s own test suite, not by this capture",
     results: $results
   }' >"$EVID/parity-summary.json"
log "wrote $EVID/parity-summary.json"

echo
echo "parity assertions: $PASS passed, $FAIL failed"
# Since JS otel-nats 0.3.0 the topology rows converge too, so every row —
# P (span shape) and D (topology) — must pass.
[[ "$FAIL" == "0" ]] || die "$FAIL parity assertion(s) failed"
log "full parity holds on every dimension the demo path covers"
