CLUSTER_NAME := demo-trace
NAMESPACE    := demo
# kind names the kubeconfig context kind-<cluster-name>
KIND_CONTEXT := kind-$(CLUSTER_NAME)

# Load-test knobs — override on the command line, e.g.
#   make load-test WORKERS=10 DURATION=2m SPANS_PER_RESOURCE=500
WORKERS            ?= 2
SPANS_PER_RESOURCE ?= 100
PUSH_INTERVAL      ?= 50ms
DURATION           ?= 60s

# Local ports for make port-forward (override if they collide on your machine)
PF_FRONTEND_PORT ?= 8081
PF_BACKEND_PORT  ?= 8080
PF_GRAFANA_PORT  ?= 3000
PF_ROTEL_PORT    ?= 4318

.PHONY: bootstrap kind-up kind-down kind-context build-images kind-load helm-install k8s-apply \
	deploy wait-ready port-forward pf teardown load-test load-test-clean load-test-logs

# Initialize/update git submodules (instrumentation-js, instrumentation-go) to their
# tracked branch tip. Safe to re-run; required after a clone that skipped
# `--recurse-submodules`.
bootstrap:
	git submodule update --init --remote

# Create the local kind cluster only if it does not already exist.
# Never deletes or recreates an existing cluster — re-run `make deploy` to upgrade in place.
kind-up:
	@if kind get clusters 2>/dev/null | grep -qx "$(CLUSTER_NAME)"; then \
		echo "kind cluster '$(CLUSTER_NAME)' already exists — reusing (no recreate)"; \
	else \
		echo "Creating kind cluster '$(CLUSTER_NAME)'..."; \
		kind create cluster --config deploy/kind-cluster.yaml; \
	fi
	@$(MAKE) --no-print-directory kind-context

# Point kubectl at the demo kind cluster (no-op if already selected).
kind-context:
	@kubectl config use-context "$(KIND_CONTEXT)" >/dev/null
	@echo "kubectl context: $$(kubectl config current-context)"

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
helm-install: kind-context
	helm upgrade --install nats charts/nats -f deploy/values/nats.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install clickhouse charts/clickhouse -f deploy/values/clickhouse.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install grafana charts/grafana -f deploy/values/grafana.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install relay-proxy charts/relay-proxy -f deploy/values/relay-proxy.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install victoria-metrics charts/victoria-metrics -f deploy/values/victoria-metrics.yaml -n $(NAMESPACE) --create-namespace

# Apply the in-house services (frontend, backend, feature-flags ConfigMap, rotel)
# via the Kustomize base.
k8s-apply: kind-context
	kubectl apply -k deploy/base

# Full local bring-up: cluster (reuse if present) -> images -> charts -> services.
# Safe to re-run: does not delete an existing kind cluster.
deploy: kind-up kind-load helm-install k8s-apply
	@echo ""
	@echo "Deployed to kind cluster '$(CLUSTER_NAME)' (context $(KIND_CONTEXT))."
	@echo "When pods are Ready, open access with:"
	@echo "  make port-forward"
	@echo ""
	@echo "  Frontend  http://localhost:$(PF_FRONTEND_PORT)"
	@echo "  Backend   http://localhost:$(PF_BACKEND_PORT)"
	@echo "  Grafana   http://localhost:$(PF_GRAFANA_PORT)  (admin / demo-grafana-admin)"
	@echo "  OTLP      http://localhost:$(PF_ROTEL_PORT)   (browser spans)"

# Wait until the services we port-forward are rollouts-complete.
wait-ready: kind-context
	@echo "Waiting for demo deployments in namespace '$(NAMESPACE)'..."
	kubectl -n $(NAMESPACE) rollout status deployment/frontend --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/backend --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/rotel --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/grafana --timeout=300s
	@echo "Core deployments ready."

# Port-forward frontend, backend, Grafana, and rotel in one process.
# Blocks until Ctrl-C; then stops all forwards.
# Usage after deploy:
#   make port-forward
#   # or: make pf
port-forward pf: kind-context wait-ready
	@echo ""
	@echo "Port-forwards running (Ctrl-C to stop):"
	@echo "  Frontend  http://localhost:$(PF_FRONTEND_PORT)"
	@echo "  Backend   http://localhost:$(PF_BACKEND_PORT)"
	@echo "  Grafana   http://localhost:$(PF_GRAFANA_PORT)  (admin / demo-grafana-admin)"
	@echo "  OTLP      http://localhost:$(PF_ROTEL_PORT)"
	@echo ""
	@trap 'echo ""; echo "Stopping port-forwards..."; kill 0' INT TERM EXIT; \
	kubectl -n $(NAMESPACE) port-forward svc/frontend $(PF_FRONTEND_PORT):8080 & \
	kubectl -n $(NAMESPACE) port-forward svc/backend  $(PF_BACKEND_PORT):8080 & \
	kubectl -n $(NAMESPACE) port-forward svc/grafana  $(PF_GRAFANA_PORT):3000 & \
	kubectl -n $(NAMESPACE) port-forward svc/rotel    $(PF_ROTEL_PORT):4318 & \
	wait

# Tear down the entire demo environment — removes the kind cluster and every
# workload in it. No persistent state survives outside the cluster.
# Only deletes the named demo cluster; does not touch other kind clusters.
teardown:
	kind delete cluster --name $(CLUSTER_NAME)

# Run a bounded OTLP trace load test against rotel. Not part of `deploy` — load
# only ever runs when explicitly asked for.
#
# The delete-first step is required, not defensive: a Job's pod template is
# immutable, so re-applying over a previous run fails outright. --ignore-not-found
# makes the first run work too.
load-test: kind-context
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
load-test-logs: kind-context
	kubectl logs -n $(NAMESPACE) job/otel-loadgen -f

# Remove the load-test Job.
load-test-clean: kind-context
	kubectl delete job otel-loadgen -n $(NAMESPACE) --ignore-not-found
