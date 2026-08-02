CLUSTER_NAME := demo-trace
NAMESPACE    := demo

# Load-test knobs — override on the command line, e.g.
#   make load-test WORKERS=10 DURATION=2m SPANS_PER_RESOURCE=500
WORKERS            ?= 2
SPANS_PER_RESOURCE ?= 100
PUSH_INTERVAL      ?= 50ms
DURATION           ?= 60s

.PHONY: bootstrap kind-up kind-down build-images kind-load helm-install k8s-apply deploy teardown load-test load-test-clean load-test-logs

# Initialize/update git submodules (instrumentation-js, instrumentation-go) to their
# tracked branch tip. Safe to re-run; required after a clone that skipped
# `--recurse-submodules`.
bootstrap:
	git submodule update --init --remote

# Create the local kind cluster. No-ops if it already exists.
kind-up:
	kind get clusters | grep -qx "$(CLUSTER_NAME)" || kind create cluster --config deploy/kind-cluster.yaml

# Build the two in-house service images. Build contexts differ — see the
# comment header of each Dockerfile for why. (The feature-flag relay proxy is
# not built here: it is the upstream GO Feature Flag image, installed from its
# vendored chart by helm-install below.)
build-images:
	docker build -f backend/Dockerfile -t demo-backend:local .
	docker build -t demo-frontend:local frontend/

# Load the freshly-built images into the kind cluster (no registry needed).
kind-load: build-images
	kind load docker-image demo-backend:local demo-frontend:local --name $(CLUSTER_NAME)

# Install/upgrade the vendored third-party charts (NATS, ClickHouse, Grafana,
# GOFF relay proxy). --create-namespace makes this safe to run before or after
# k8s-apply. The relay-proxy release name must stay "relay-proxy" so its Service
# resolves at the address the backend is configured with.
helm-install:
	helm upgrade --install nats charts/nats -f deploy/values/nats.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install clickhouse charts/clickhouse -f deploy/values/clickhouse.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install grafana charts/grafana -f deploy/values/grafana.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install relay-proxy charts/relay-proxy -f deploy/values/relay-proxy.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install victoria-metrics charts/victoria-metrics -f deploy/values/victoria-metrics.yaml -n $(NAMESPACE) --create-namespace

# Apply the in-house services (frontend, backend, relay-proxy + RBAC,
# feature-flags ConfigMap, rotel) via the Kustomize base.
k8s-apply:
	kubectl apply -k deploy/base

# Full local bring-up: cluster -> images -> vendored charts -> in-house
# services. Safe to re-run (kind-up and helm-install are both idempotent;
# k8s-apply is a plain `kubectl apply`).
deploy: kind-up kind-load helm-install k8s-apply
	@echo ""
	@echo "Deployed. Access instructions: see README.md ('Accessing the demo')."

# Tear down the entire demo environment — removes the kind cluster and every
# workload in it. No persistent state survives outside the cluster.
teardown:
	kind delete cluster --name $(CLUSTER_NAME)

# Run a bounded OTLP trace load test against rotel. Not part of `deploy` — load
# only ever runs when explicitly asked for.
#
# The delete-first step is required, not defensive: a Job's pod template is
# immutable, so re-applying over a previous run fails outright. --ignore-not-found
# makes the first run work too.
load-test:
	kubectl delete job otel-loadgen -n $(NAMESPACE) --ignore-not-found --wait
	sed -e 's|__WORKERS__|$(WORKERS)|' \
	    -e 's|__SPANS_PER_RESOURCE__|$(SPANS_PER_RESOURCE)|' \
	    -e 's|__PUSH_INTERVAL__|$(PUSH_INTERVAL)|' \
	    -e 's|__DURATION__|$(DURATION)|' \
	    deploy/loadgen/loadgen-job.yaml | kubectl apply -f -
	@echo ""
	@echo "Load test started: workers=$(WORKERS) spans-per-resource=$(SPANS_PER_RESOURCE) push-interval=$(PUSH_INTERVAL) duration=$(DURATION)"
	@echo "Follow it with:  make load-test-logs"

# Follow the running/most-recent load test's output.
load-test-logs:
	kubectl logs -n $(NAMESPACE) job/otel-loadgen -f

# Remove the load-test Job.
load-test-clean:
	kubectl delete job otel-loadgen -n $(NAMESPACE) --ignore-not-found
