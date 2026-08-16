# Always use a kind CLI cluster of a fixed name so `kind load` can reach the
# node. Docker Desktop Kubernetes is also kind-backed, but its node is hidden
# from the host kind CLI — images cannot be loaded there, so it is never a target.
#
#   make deploy                       # create kind cluster demo-trace, or reuse it
#   make teardown                     # delete the demo namespace; keep the cluster
#   make kind-down                    # delete the kind CLI cluster itself
#   make deploy CLUSTER_NAME=parity   # same, but the cluster is named parity
#
# If the named cluster already exists it is left in place (no delete/recreate).
CLUSTER_NAME ?= demo-trace
KIND_CONTEXT := kind-$(CLUSTER_NAME)
NAMESPACE    := demo

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

.PHONY: bootstrap chart-test kind-up kind-down kube-context build-images kind-load helm-install k8s-apply \
	deploy wait-ready port-forward pf teardown load-test load-test-clean load-test-logs parity

# Initialize/update git submodules (instrumentation-js, instrumentation-go) to their
# tracked branch tip. Safe to re-run; required after a clone that skipped
# `--recurse-submodules`.
bootstrap:
	git submodule update --init --remote

# Run the upstream helm-unittest suites shipped with the vendored relay-proxy
# chart. No cluster needed. Local customization lives in deploy/values/, not
# in the chart, so these suites only guard upstream template regressions.
chart-test:
	@helm plugin list 2>/dev/null | grep -q '^unittest' || { \
		echo "helm-unittest plugin missing. Install it with:"; \
		echo "  helm plugin install https://github.com/helm-unittest/helm-unittest"; \
		exit 1; \
	}
	helm unittest charts/relay-proxy

# Create the named kind CLI cluster only if it does not already exist.
# Never deletes or recreates — re-run `make deploy` to upgrade in place.
kind-up:
	@if kind get clusters 2>/dev/null | grep -qx "$(CLUSTER_NAME)"; then \
		echo "kind cluster '$(CLUSTER_NAME)' already exists — reusing (no recreate)"; \
	else \
		echo "Creating kind cluster '$(CLUSTER_NAME)'..."; \
		kind create cluster --name "$(CLUSTER_NAME)" --config deploy/kind-cluster.yaml; \
	fi
	@$(MAKE) --no-print-directory kube-context

# Point kubectl at the named kind CLI cluster (context kind-<CLUSTER_NAME>).
kube-context:
	@kubectl config use-context "$(KIND_CONTEXT)" >/dev/null
	@echo "kubectl context: $$(kubectl config current-context)"
	@kubectl cluster-info >/dev/null

# Build the three in-house service images. Build contexts differ — see the
# comment header of each Dockerfile for why. (The feature-flag relay proxy is
# not built here: it is the upstream GO Feature Flag image, installed from its
# vendored chart by helm-install below.)
build-images:
	docker build -f backend/Dockerfile -t demo-backend:local .
	docker build -f js-service/Dockerfile -t demo-js-service:local .
	docker build -t demo-frontend:local frontend/

# Load the freshly-built images into the named kind CLI cluster (no registry).
kind-load: build-images
	@echo "Loading images into kind cluster '$(CLUSTER_NAME)'..."
	kind load docker-image demo-backend:local demo-js-service:local demo-frontend:local --name "$(CLUSTER_NAME)"

# Install/upgrade the vendored third-party charts (NATS, ClickHouse+rotel via
# Altinity operator, Grafana, GOFF relay proxy, VictoriaMetrics).
# --create-namespace makes this safe to run before or after k8s-apply.
# The clickhouse release also deploys rotel (Service name "rotel") and the
# ClickHouse client Service (name "clickhouse") — see deploy/values/clickhouse.yaml.
# The relay-proxy release name must stay "relay-proxy" so its Service resolves
# at the address otelnats is pointed at (OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT
# in deploy/base/backend.yaml).
#
# ClickHouse is installed in two steps on first apply so CRDs from the operator
# exist before the CHI/CHK custom resources are created (avoids a race where
# helm applies CRs before the crd-install Job finishes).
helm-install: kube-context
	helm upgrade --install nats charts/nats -f deploy/values/nats.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install clickhouse-operator charts/clickhouse \
		-f deploy/values/clickhouse.yaml \
		--set cluster.enabled=false \
		-n $(NAMESPACE) --create-namespace
	@echo "Waiting for ClickHouse CRDs..."
	@kubectl wait --for=condition=Established crd/clickhouseinstallations.clickhouse.altinity.com --timeout=120s
	@kubectl wait --for=condition=Established crd/clickhousekeeperinstallations.clickhouse-keeper.altinity.com --timeout=120s
	@# Demo credentials. The chart only references this Secret; it never
	@# creates it. Three keys, three different values:
	@#   password — `default` admin (rotel writes with it)
	@#   secret   — inter-replica clusterSecret
	@#   reporter — read-only `reporter` (Grafana; must match
	@#              deploy/values/grafana.yaml secureJsonData)
	@kubectl create secret generic clickhouse-default-user \
		--from-literal=password='demo-clickhouse-pw' \
		--from-literal=secret='demo-clickhouse-cluster-secret' \
		--from-literal=reporter='demo-clickhouse-reporter-pw' \
		-n $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@# --timeout 15m: the post-install DDL Job waits for the full CHI (2 CH
	@# replicas + 3 Keepers) to assemble before creating the otel schema, which
	@# exceeds helm's default 5m hook wait on first install (image pulls + PVCs).
	helm upgrade --install clickhouse charts/clickhouse \
		-f deploy/values/clickhouse.yaml \
		--set operator.enabled=false \
		--timeout 15m \
		-n $(NAMESPACE) --create-namespace
	helm upgrade --install grafana charts/grafana \
		-f deploy/values/grafana.yaml \
		-f deploy/values/grafana-otel-traces-explorer.yaml \
		-n $(NAMESPACE) --create-namespace
	helm upgrade --install relay-proxy charts/relay-proxy -f deploy/values/relay-proxy.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install victoria-metrics charts/victoria-metrics -f deploy/values/victoria-metrics.yaml -n $(NAMESPACE) --create-namespace

# Apply the in-house services and the library flags ConfigMap (demo-feature-flags)
# via the Kustomize base. The relay proxy reads that ConfigMap through GOFF's
# configmap retriever (see deploy/values/relay-proxy.yaml); startWithRetrieverError
# keeps it up if helm-install raced ahead of this apply.
k8s-apply: kube-context
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
# Also waits for the Altinity CHI (ClickHouse) to finish reconciling so rotel's
# exporter and Grafana's datasource have a ready HTTP endpoint.
wait-ready: kube-context
	@echo "Waiting for demo deployments in namespace '$(NAMESPACE)'..."
	kubectl -n $(NAMESPACE) rollout status deployment/frontend --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/backend --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/js-service --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/rotel --timeout=300s
	kubectl -n $(NAMESPACE) rollout status deployment/grafana --timeout=300s
	@echo "Waiting for ClickHouseInstallation clickhouse-cluster..."
	@for i in $$(seq 1 60); do \
		st=$$(kubectl -n $(NAMESPACE) get chi clickhouse-cluster -o jsonpath='{.status.status}' 2>/dev/null || true); \
		if [ "$$st" = "Completed" ]; then echo "CHI status=Completed"; break; fi; \
		if [ "$$i" -eq 60 ]; then echo "timed out waiting for CHI (last status=$$st)"; exit 1; fi; \
		sleep 5; \
	done
	@echo "Core deployments ready."

# Port-forward frontend, backend, Grafana, and rotel in one process.
# Blocks until Ctrl-C; then stops all forwards.
# Usage after deploy:
#   make port-forward
#   # or: make pf
port-forward pf: kube-context wait-ready
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

# Tear down the demo workloads only. The kind CLI cluster is kept so the next
# `make deploy` can reuse it (and the images already loaded into the node).
teardown:
	@if ! kind get clusters 2>/dev/null | grep -qx "$(CLUSTER_NAME)"; then \
		echo "No kind cluster named '$(CLUSTER_NAME)'."; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory kube-context
	@echo "Removing namespace '$(NAMESPACE)' from kind cluster '$(CLUSTER_NAME)' (cluster kept)..."
	kubectl delete namespace $(NAMESPACE) --ignore-not-found --wait
	@echo "Kind cluster '$(CLUSTER_NAME)' still running. Delete it with: make kind-down"

# Delete the named kind CLI cluster (never Docker Desktop).
kind-down:
	@if kind get clusters 2>/dev/null | grep -qx "$(CLUSTER_NAME)"; then \
		echo "Deleting kind cluster '$(CLUSTER_NAME)'..."; \
		kind delete cluster --name "$(CLUSTER_NAME)"; \
	else \
		echo "No kind cluster named '$(CLUSTER_NAME)'."; \
		exit 1; \
	fi

# Run a bounded OTLP trace load test against rotel. Not part of `deploy` — load
# only ever runs when explicitly asked for.
#
# The delete-first step is required, not defensive: a Job's pod template is
# immutable, so re-applying over a previous run fails outright. --ignore-not-found
# makes the first run work too.
load-test: kube-context
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
load-test-logs: kube-context
	kubectl logs -n $(NAMESPACE) job/otel-loadgen -f

# Remove the load-test Job.
load-test-clean: kube-context
	kubectl delete job otel-loadgen -n $(NAMESPACE) --ignore-not-found

# Compare the span SHAPE the Go and JS otel-nats emit for the same operation.
#
# Distinct from capture-live-evidence.sh, which counts spans to prove one flag
# flip governs both runtimes. Counting cannot catch a naming or attribute drift
# between the two implementations — Go 0.9.0 renamed `send {subject}` to
# `publish {subject}` and a count-only check stayed green through it.
#
# Requires a cluster built from the CURRENT submodules (`make deploy`), since it
# reports on whatever the running images were compiled against.
parity: kube-context
	docs/scripts/capture-parity-evidence.sh
