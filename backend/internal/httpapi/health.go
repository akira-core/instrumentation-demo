package httpapi

import (
	"net/http"

	"github.com/akira-core/instrumentation-demo/backend/internal/natsflow"
)

// NewHealthzHandler always returns 200 once the HTTP server is serving
// requests — an in-progress NATS connection retry is a normal, non-fatal
// state (task 2.7), so readiness never blocks on it. natsConnected is
// informational only.
func NewHealthzHandler(nm *natsflow.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":        "ok",
			"natsConnected": nm.Connected(),
		})
	}
}
