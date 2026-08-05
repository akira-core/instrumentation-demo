package httpapi

import "net/http"

// withCORS sets the headers browser callers need to invoke this handler
// cross-origin, including handling the OPTIONS preflight request. The
// allowed-headers list must include traceparent/tracestate so the frontend
// can propagate its trace context.
func withCORS(allowedOrigin string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "traceparent, tracestate, content-type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}
