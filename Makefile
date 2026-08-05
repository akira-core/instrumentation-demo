# Kind cluster name is optional — do not hardcode a single cluster.
#
#   make port-forward                 # use current kubectl context (recommended)
#   make deploy CLUSTER_NAME=desktop  # pin a kind cluster by name
#   make deploy                       # auto-pick: current kind-* context, sole kind
#                                     # cluster, or create DEFAULT_CLUSTER_NAME
#
# Day-to-day targets (port-forward, helm, apply, load-test) use whatever
# kubectl currently points at unless CLUSTER_NAME is set.
CLUSTER_NAME         ?=
DEFAULT_CLUSTER_NAME := demo-trace
NAMESPACE            := demo

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

# Resolve which kind cluster name to use for kind create/load/delete.
# Order: explicit CLUSTER_NAME → kind-* current context → sole kind cluster → default.
# Exports RESOLVED_CLUSTER (empty only if no kind tooling path applies).
define resolve_kind_cluster
	if [ -n "$(CLUSTER_NAME)" ]; then \
		echo "$(CLUSTER_NAME)"; \
	else \
		ctx=$$(kubectl config current-context 2>/dev/null || true); \
		case "$$ctx" in \
			kind-*) echo "$${ctx#kind-}" ;; \
			*) \
				clusters=$$(kind get clusters 2>/dev/null || true); \
				n=$$(printf '%s\n' "$$clusters" | sed '/^$$/d' | wc -l | tr -d ' '); \
				if [ "$$n" -eq 1 ]; then \
					printf '%s\n' "$$clusters" | sed '/^$$/d'; \
				else \
					echo "$(DEFAULT_CLUSTER_NAME)"; \
				fi ;; \
		esac; \
	fi
endef

.PHONY: bootstrap kind-up kind-down kube-context build-images kind-load helm-install k8s-apply \
	deploy wait-ready port-forward pf teardown load-test load-test-clean load-test-logs

# Initialize/update git submodules (instrumentation-js, instrumentation-go) to their
# tracked branch tip. Safe to re-run; required after a clone that skipped
# `--recurse-submodules`.
bootstrap:
	git submodule update --init --remote

# Create a local kind cluster only if the resolved name does not already exist.
# Never deletes or recreates an existing cluster — re-run `make deploy` to upgrade in place.
# Skip kind create when CLUSTER_NAME is unset and current context is already a
# reachable non-kind cluster (e.g. Docker Desktop) that already has the demo.
kind-up:
	@name=$$($(resolve_kind_cluster)); \
	if kind get clusters 2>/dev/null | grep -qx "$$name"; then \
		echo "kind cluster '$$name' already exists — reusing (no recreate)"; \
	elif [ -z "$(CLUSTER_NAME)" ] && kubectl cluster-info >/dev/null 2>&1; then \
		ctx=$$(kubectl config current-context 2>/dev/null || true); \
		case "$$ctx" in \
			kind-*) \
				echo "Creating kind cluster '$$name'..."; \
				kind create cluster --name "$$name" --config deploy/kind-cluster.yaml ;; \
			*) \
				echo "kubectl already points at '$$ctx' — skipping kind create (pass CLUSTER_NAME=... to force a kind cluster)" ;; \
		esac; \
	else \
		echo "Creating kind cluster '$$name'..."; \
		kind create cluster --name "$$name" --config deploy/kind-cluster.yaml; \
	fi
	@$(MAKE) --no-print-directory kube-context

# Point kubectl at a cluster when CLUSTER_NAME is set; otherwise keep current context.
# Safe for Docker Desktop / any pre-existing kubeconfig — no hard-coded kind name.
kube-context:
	@if [ -n "$(CLUSTER_NAME)" ]; then \
		kubectl config use-context "kind-$(CLUSTER_NAME)" >/dev/null; \
	fi
	@echo "kubectl context: $$(kubectl config current-context)"
	@kubectl cluster-info >/dev/null

# Build the two in-house service images. Build contexts differ — see the
# comment header of each Dockerfile for why. (The feature-flag relay proxy is
# not built here: it is the upstream GO Feature Flag image, installed from its
# vendored chart by helm-install below.)
build-images:
	docker build -f backend/Dockerfile -t demo-backend:local .
	docker build -t demo-frontend:local frontend/

# Load the freshly-built images into a kind cluster (no registry needed).
# Skips when the active cluster is not kind (e.g. Docker Desktop can pull local tags).
kind-load: build-images
	@name=$$($(resolve_kind_cluster)); \
	if kind get clusters 2>/dev/null | grep -qx "$$name"; then \
		echo "Loading images into kind cluster '$$name'..."; \
		kind load docker-image demo-backend:local demo-frontend:local --name "$$name"; \
	else \
		echo "No kind cluster named '$$name' — skipping kind load (images must already be available to the cluster)"; \
	fi

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
	helm upgrade --install clickhouse charts/clickhouse \
		-f deploy/values/clickhouse.yaml \
		--set operator.enabled=false \
		-n $(NAMESPACE) --create-namespace
	helm upgrade --install grafana charts/grafana -f deploy/values/grafana.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install relay-proxy charts/relay-proxy -f deploy/values/relay-proxy.yaml -n $(NAMESPACE) --create-namespace
	helm upgrade --install victoria-metrics charts/victoria-metrics -f deploy/values/victoria-metrics.yaml -n $(NAMESPACE) --create-namespace

# Apply the in-house services (frontend, backend, feature-flags ConfigMap)
# via the Kustomize base.
k8s-apply: kube-context
	kubectl apply -k deploy/base

# Full local bring-up: cluster (reuse if present) -> images -> charts -> services.
# Safe to re-run: does not delete an existing kind cluster.
deploy: kind-up kind-load helm-install k8s-apply
	@echo ""
	@echo "Deployed (kubectl context: $$(kubectl config current-context))."
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
# Uses the current kubectl context (no hard-coded kind cluster name).
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

# Tear down — only deletes a kind cluster (never touches Docker Desktop / other contexts).
# Pass CLUSTER_NAME=... when more than one kind cluster exists or auto-detect is ambiguous.
teardown:
	@name=$$($(resolve_kind_cluster)); \
	if kind get clusters 2>/dev/null | grep -qx "$$name"; then \
		echo "Deleting kind cluster '$$name'..."; \
		kind delete cluster --name "$$name"; \
	else \
		echo "No kind cluster named '$$name' — refusing to tear down current context '$$(kubectl config current-context 2>/dev/null)'."; \
		echo "Hint: make teardown CLUSTER_NAME=<kind-cluster-name>"; \
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
