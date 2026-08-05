// Package config loads backend runtime configuration from environment
// variables, applying the defaults from the demo's integration contract.
package config

import "os"

// EnvFlagsEndpoint is the instrumentation libraries' own relay proxy URL
// variable. The backend never reads it to make a decision — otelnats reads it
// itself and installs the OpenFeature provider without any application code.
// It is loaded here only so the startup log can report whether relay control
// is wired, which is why it has no default: an empty value is the library's
// documented "no provider is installed" state, and inventing a fallback here
// would make the log disagree with the library.
const EnvFlagsEndpoint = "OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT"

// Config holds all environment-derived runtime settings for the backend.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	Port string
	// FlagsEndpoint is the GO Feature Flag relay proxy URL that otelnats
	// resolves its kill-switch flag through. Reported at startup, never acted
	// on — see EnvFlagsEndpoint.
	FlagsEndpoint string
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
		FlagsEndpoint:     os.Getenv(EnvFlagsEndpoint),
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
