# otel-nats runtime parity: Go vs JS

Whether `otel-nats` (Go) and `@akira-core/otel-nats` (JS) do the **same thing**
for the same messaging operation — and which parts of that claim this demo
verifies end-to-end, versus which parts rest on each library's own test suite.

Audited at:

| Submodule | Commit | Version |
|---|---|---|
| `third_party/instrumentation-go` | tag `otel-nats/v0.9.1` | `otelnats` `0.9.1` (`otelnats/conn.go:21`) |
| `third_party/instrumentation-js` | `feat/otel-nats-consumer-span-links` ([PR #3](https://github.com/akira-core/instrumentation-js/pull/3)) | `@akira-core/otel-nats` `0.3.0`, `@akira-core/otel-flags` `0.1.0` |

JS `0.2.0` aligned span names and inbox attributes with Go `0.9.0`/`0.9.1`;
JS `0.3.0` closed the one remaining behavioural gap (consumer-span topology).
This document is the check on the full claim.

---

## Summary

Twenty-one dimensions compared. **Nineteen agree** — including the consumer-span
topology that used to diverge (see history below). The two that differ are both
deliberate, documented consequences of the platforms, not drift:

- **#6 — flag resolution timing.** Go resolves per operation, synchronously; JS
  resolves against a background-refreshed snapshot. Same ladder, same verdict,
  different upper bound on how long a flip takes to land (3s vs 5s). A
  consequence of OpenFeature JS having no synchronous evaluation path.
- **#21 — context threading.** Go takes an explicit `ctx`; JS reads ambient
  `AsyncLocalStorage`. Deliberate and documented in the JS README.

---

## The matrix

Legend — **demo**: verified end-to-end by `docs/scripts/capture-parity-evidence.sh`
(`make parity`) against a live cluster. **suite**: verified by the libraries' own
tests, not reachable from the demo path. **read**: established by reading both
implementations.

| # | Dimension | Go 0.9.1 | JS 0.3.0 | Parity | Backed by |
|---|---|---|---|---|---|
| 1 | Flag key | `otel-nats-tracing` | `otel-nats-tracing` | same | demo |
| 2 | Module env var | `OTEL_NATS_TRACING_ENABLED` | `OTEL_NATS_TRACING_ENABLED` | same | demo |
| 3 | Master veto env | `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `OTEL_INSTRUMENTATION_JS_TRACING_ENABLED` | same shape, per-runtime by design | demo |
| 4 | Module default | `false` | `false` | same | read |
| 5 | Ladder | relay > env > option > default | same | same | demo |
| 6 | Resolution timing | per-operation, synchronous | background-refreshed snapshot | **differs** (3s vs 5s bound) | demo |
| 7 | Publish span name | `publish {subject}` | `publish {subject}` | same | demo |
| 8 | Request span name | `request {subject}` | `request {subject}` | same | suite |
| 9 | Inbox destination omitted from span name | all three halves | all three halves | same | suite |
| 10 | `messaging.destination.template` on wildcard | yes | yes | same | suite |
| 11 | Inbox attrs (`temporary`/`anonymous`/`conversation_id`) | yes | yes | same | suite |
| 12 | `_INBOX.>` treated as bounded | yes | yes | same | suite |
| 13 | `InboxPrefixes()` / `inboxPrefixes()` | yes | yes | same | read |
| 14 | Span kinds | semconv messaging mapping | same mapping | same | demo |
| 15 | Span attribute set and emit conditions | `conn.go:397-457` | `attributes.ts:49-101` | same | demo |
| 16 | Reply-receive span carries a span link | yes | yes | same | suite |
| 17 | Late `conversation_id` suppressed for inbox-addressed request | yes | yes | same | suite |
| 18 | Core subscribe: consumer span parent | new trace root | new trace root (0.3.0+) | same | demo |
| 19 | Core subscribe: link back to producer | yes, exactly one | yes, exactly one (0.3.0+) | same | demo |
| 20 | JetStream consumer: parent + link | new root + link | new root + link (0.3.0+) | same | suite |
| 21 | Context threading | explicit `ctx` param | ambient `AsyncLocalStorage` | differs by design | read |

### On row 15

Both build the same attribute set under the same conditions: `messaging.system`,
`messaging.destination.name`, `messaging.operation.type`,
`messaging.operation.name` always; `messaging.message.body.size` when the body is
non-empty; `messaging.message.conversation_id` when `reply` is set;
`messaging.consumer.group.name` when a queue group is set; server address/port
appended last. Both exclude `conversation_id` from JetStream spans (`$JS.ACK…` is
protocol plumbing, not a conversation identifier).

### On rows 18–20 (history)

Before JS `0.3.0`, `@akira-core/otel-nats` parented every consumer span on the
context extracted from the message headers, so JS consumers landed inline on the
producer's trace while Go consumers opened their own linked-root traces — a real,
systematic divergence this demo's parity capture exposed (its `D01`/`D02` rows
were written as plain assertions so the fix would flip them green without a
demo-side edit, which is exactly what happened). The root cause was spec wording
in `instrumentation-js` ("a context whose active trace matches the publisher's
trace") that the implementation faithfully delivered; the OpenSpec change
`js-otel-nats-consumer-span-links` (archived 2026-08-15) rewrote the contract to
the explicit root-plus-link topology, the implementation followed, and the
topology is now pinned by tests on both sides.

Both runtimes now emit, for every consume path: a new-trace root span carrying
exactly one span link to the producer, with the handler/message context carrying
the consumer span. One message consumed by both runtimes yields one trace shape.

---

## Known residual difference worth tracking

The **reply-receive span** of `request` carries a span link in both runtimes, but
the details differ: Go parents it on the request context (or the extracted reply
context when the responder propagated) and links the extracted responder context
when present; JS parents it on the ambient context and always links the request
span, never extracting the reply's own headers. Go's own spec text for this span
has also drifted (it says CONSUMER; both implementations emit CLIENT). Deferred
to its own change so both repos can align on one written contract — recorded in
the archived change's `design.md`.

---

## What the demo path cannot cover

The demo exercises **core-NATS publish and push-subscribe** on both runtimes. It
has no request/reply RPC, no wildcard subscription, no inbox subject and no
JetStream, so rows 8–12, 16, 17 and 20 are marked **suite**: they are backed by
`otel-nats/otelnats/conn_test.go`, `oteljetstream`'s suite, and
`packages/otel-nats/test/{unit,integration}/`.

Both suites ran green at the audited commits:

```
$ (cd third_party/instrumentation-go/otel-nats && go test ./otelnats/... ./oteljetstream/...)
ok  github.com/akira-core/instrumentation-go/otel-nats/otelnats        1.154s
ok  github.com/akira-core/instrumentation-go/otel-nats/oteljetstream  10.966s

$ (cd third_party/instrumentation-js/packages/otel-nats && pnpm test)
Test Files  8 passed (8)      Tests  183 passed (183)     # unit
Test Files  5 passed (5)      Tests   29 passed  (29)     # integration
```

Since `0.3.0` the JS suite pins consumer-span topology too
(`test/unit/consumer-topology.test.ts` and the linked-root assertions in its
integration suites), so a green suite on each side now covers the dimension that
previously could only be caught by this demo's cross-runtime comparison.

---

## Reproducing

```sh
make deploy CLUSTER_NAME=parity      # builds images from the current submodules
make parity CLUSTER_NAME=parity      # docs/scripts/capture-parity-evidence.sh
```

The cluster must be built from the **current** submodules — the capture reports
on whatever the running images were compiled against, not on what is checked out
on disk. Docker Desktop's Kubernetes will not see a locally built image (its
containerd has a separate image store from the Docker daemon), which is why the
reproduction above uses `kind`, where `make deploy` loads images explicitly.

Writes `docs/evidence/parity-summary.json` (machine-readable assertion results),
`docs/evidence/parity-spans.txt` (the span tables as captured) and one
`docs/evidence/parity-span-*.json` per compared span. All fourteen assertions —
span shape (`P01`–`P12`) and topology (`D01`/`D02`) — must pass; the script exits
non-zero otherwise.
