// Package config loads backend runtime configuration from environment
// variables, applying the defaults from the demo's integration contract.
package config

import "os"

// Config holds all environment-derived runtime settings for the backend.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	Port string
	// RelayProxyURL is the base URL of the GO Feature Flag relay proxy that
	// the OpenFeature provider evaluates flags against.
	RelayProxyURL string
	// NATSURL is the connection URL for the NATS server.
	NATSURL string
	// OTLPEndpoint is the OTLP/HTTP endpoint traces are exported to.
	OTLPEndpoint string
	// ServiceName is the OTel resource service.name value.
	ServiceName string
	// CORSAllowedOrigin is the value sent in Access-Control-Allow-Origin.
	CORSAllowedOrigin string
}

// Load reads Config from the environment, falling back to the demo's
// documented defaults for any unset variable.
func Load() Config {
	return Config{
		Port:              getEnv("PORT", "8080"),
		RelayProxyURL:     getEnv("RELAY_PROXY_URL", "http://relay-proxy:1031"),
		NATSURL:           getEnv("NATS_URL", "nats://nats:4222"),
		OTLPEndpoint:      getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://rotel:4318"),
		ServiceName:       getEnv("OTEL_SERVICE_NAME", "demo-backend"),
		CORSAllowedOrigin: getEnv("CORS_ALLOWED_ORIGIN", "*"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
