// Package flagperf measures otel-nats per-Publish average latency under
// different feature-flag gate configurations, and writes structured evidence
// for the zh-TW HTML report.
//
// Focus: gate evaluation cost when many requests must send traces — compare
// paths that evaluate OpenFeature each call vs paths that never touch OpenFeature.
package flagperf

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	otelflags "github.com/akira-core/instrumentation-go/otel-flags"
	otelnats "github.com/akira-core/instrumentation-go/otel-nats/otelnats"
)

const (
	flagKeyNATS = "otel-nats-tracing"
	envMaster   = "OTEL_INSTRUMENTATION_GO_TRACING_ENABLED"
	envModule   = "OTEL_NATS_TRACING_ENABLED"
	envEndpoint = "OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT"
	subject     = "demo.perf.gate"
)

// Mode is one gate configuration under test.
type Mode struct {
	ID          string   `json:"id"`
	TitleZH     string   `json:"title_zh"`
	Description string   `json:"description_zh"`
	// ConfigSteps documents what we set for the report.
	ConfigSteps []string `json:"config_steps"`
	// UseNamedProvider installs an in-memory provider on FlagDomain (relay possible).
	UseNamedProvider bool `json:"use_named_provider"`
	// FlagOn is the otel-nats-tracing value when UseNamedProvider is true.
	FlagOn bool `json:"flag_on"`
	// ModuleEnv if non-nil is set as OTEL_NATS_TRACING_ENABLED.
	ModuleEnv *string `json:"module_env"`
	// MasterEnv if non-nil is set as OTEL_INSTRUMENTATION_GO_TRACING_ENABLED.
	MasterEnv *string `json:"master_env"`
	// ExpectTracing is the expected conn.TracingEnabled() after setup.
	ExpectTracing bool `json:"expect_tracing"`
	// ExpectRelayPossible documents whether gate evaluation should hit OpenFeature.
	ExpectRelayPossible bool `json:"expect_relay_possible"`
}

// RunResult is timing for one mode × N.
type RunResult struct {
	ModeID         string  `json:"mode_id"`
	N              int     `json:"n"`
	Warmup         int     `json:"warmup"`
	AvgNS          float64 `json:"avg_ns"`
	AvgUS          float64 `json:"avg_us"`
	P50NS          float64 `json:"p50_ns"`
	P95NS          float64 `json:"p95_ns"`
	P99NS          float64 `json:"p99_ns"`
	MinNS          float64 `json:"min_ns"`
	MaxNS          float64 `json:"max_ns"`
	TotalMS        float64 `json:"total_ms"`
	OpsPerSec      float64 `json:"ops_per_sec"`
	TracingEnabled bool    `json:"tracing_enabled"`
	Errors         int     `json:"errors"`
}

// Report is the full evidence payload.
type Report struct {
	GeneratedAt string      `json:"generated_at"`
	GoVersion   string      `json:"go_version"`
	GOOS        string      `json:"goos"`
	GOARCH      string      `json:"goarch"`
	NumCPU      int         `json:"num_cpu"`
	Library     string      `json:"library"`
	Method      string      `json:"method_zh"`
	PayloadB    int         `json:"payload_bytes"`
	Volumes     []int       `json:"volumes"`
	Modes       []Mode      `json:"modes"`
	Results     []RunResult `json:"results"`
	// Comparisons: flag_on vs no_flag_no_env (and vs env_on) at each N.
	Comparisons []Comparison `json:"comparisons"`
}

// Comparison is a side-by-side delta at one N.
type Comparison struct {
	N                    int     `json:"n"`
	BaseMode             string  `json:"base_mode"` // no_flag_no_env
	BaseAvgUS            float64 `json:"base_avg_us"`
	EnvOnMode            string  `json:"env_on_mode"`
	EnvOnAvgUS           float64 `json:"env_on_avg_us"`
	FlagOnMode           string  `json:"flag_on_mode"`
	FlagOnAvgUS          float64 `json:"flag_on_avg_us"`
	FlagOffMode          string  `json:"flag_off_mode"`
	FlagOffAvgUS         float64 `json:"flag_off_avg_us"`
	// Deltas relative to base (no OF, no env).
	FlagOnMinusBaseUS    float64 `json:"flag_on_minus_base_us"`
	FlagOnOverBaseRatio  float64 `json:"flag_on_over_base_ratio"`
	// Gate-only overhead when traces are actually emitted: flag_on vs env_on.
	FlagOnMinusEnvOnUS   float64 `json:"flag_on_minus_env_on_us"`
	FlagOnOverEnvOnRatio float64 `json:"flag_on_over_env_on_ratio"`
	// Cost of evaluating OF when flag is off vs no OF at all.
	FlagOffMinusBaseUS   float64 `json:"flag_off_minus_base_us"`
	FlagOffOverBaseRatio float64 `json:"flag_off_over_base_ratio"`
}

func strPtr(s string) *string { return &s }

func allModes() []Mode {
	return []Mode{
		{
			ID:      "no_flag_no_env",
			TitleZH: "完全不使用 feature flag / env（零閘門）",
			Description: "不綁 named provider、不設 OTEL_* 旗標變數。" +
				"RelayPossible=false，模組預設 off，熱路徑不做 OpenFeature 評估，也不發 span。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"UNSET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED",
				"UNSET OTEL_NATS_TRACING_ENABLED",
				"FlagDomain → NoopProvider",
				"Connect 不加 WithTracingEnabled",
			},
			UseNamedProvider:    false,
			ExpectTracing:       false,
			ExpectRelayPossible: false,
		},
		{
			ID:      "env_on_no_flag",
			TitleZH: "僅 env 開啟 tracing（無 feature flag / 無 OpenFeature）",
			Description: "OTEL_NATS_TRACING_ENABLED=true，無 named provider。" +
				"會發 span，但熱路徑不做 OpenFeature 評估（本地 master∧module 布林）。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"UNSET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED（master 預設 true）",
				"SET OTEL_NATS_TRACING_ENABLED=true",
				"FlagDomain → NoopProvider",
			},
			ModuleEnv:           strPtr("true"),
			UseNamedProvider:    false,
			ExpectTracing:       true,
			ExpectRelayPossible: false,
		},
		{
			ID:      "flag_on",
			TitleZH: "有 feature flag 且為 enabled（每 call 評估閘門 + 發 span）",
			Description: "named provider 服務 otel-nats-tracing=true；module env=false（option C 姿勢）。" +
				"每操作 2 次 OpenFeature Boolean 評估後走 traced 實作。",
			ConfigSteps: []string{
				"UNSET endpoint（避免真 GOFF）",
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"SetNamedProviderAndWait(FlagDomain) otel-nats-tracing=true",
				"先裝 provider 再 Connect",
			},
			ModuleEnv:           strPtr("false"),
			UseNamedProvider:    true,
			FlagOn:              true,
			ExpectTracing:       true,
			ExpectRelayPossible: true,
		},
		{
			ID:      "flag_off",
			TitleZH: "有 feature flag 且為 disabled（每 call 仍評估閘門，不發 span）",
			Description: "named provider 服務 otel-nats-tracing=false。" +
				"仍做 OpenFeature 評估，但選 direct 實作、不發 span。",
			ConfigSteps: []string{
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"SetNamedProviderAndWait(FlagDomain) otel-nats-tracing=false",
			},
			ModuleEnv:           strPtr("false"),
			UseNamedProvider:    true,
			FlagOn:              false,
			ExpectTracing:       false,
			ExpectRelayPossible: true,
		},
	}
}

// volumes: orders of magnitude of Publish calls after warmup.
func volumes() []int {
	return []int{100, 1_000, 10_000, 100_000}
}

func TestGateLatencyByRequestVolume(t *testing.T) {
	// Shared NATS + tracer for all modes.
	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)
	url := srv.ClientURL()

	// Always sample so flag_on / env_on actually create spans (the "need to
	// send traces" scenario). Export to a no-op exporter so 1e5 runs do not
	// retain every span in memory.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(discardExporter{})),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	payload := []byte(`{"bench":"gate-latency","n":0}`)
	modes := allModes()
	vols := volumes()
	const warmup = 200

	results := make([]RunResult, 0, len(modes)*len(vols))

	for _, mode := range modes {
		mode := mode
		t.Run(mode.ID, func(t *testing.T) {
			conn := setupConn(t, url, mode)
			t.Cleanup(conn.Close)

			if got := conn.TracingEnabled(); got != mode.ExpectTracing {
				t.Fatalf("TracingEnabled()=%v want %v for mode %s", got, mode.ExpectTracing, mode.ID)
			}
			t.Logf("mode=%s TracingEnabled=%v", mode.ID, conn.TracingEnabled())

			// Warmup (not timed).
			ctx := context.Background()
			for i := 0; i < warmup; i++ {
				_ = conn.Publish(ctx, subject, payload)
			}
			// Flush NATS buffers so warmup does not pollute first timed publishes.
			_ = conn.NatsConn().Flush()

			for _, n := range vols {
				n := n
				rr := measurePublish(t, conn, subject, payload, n, warmup)
				rr.ModeID = mode.ID
				rr.TracingEnabled = conn.TracingEnabled()
				results = append(results, rr)
				t.Logf("%s N=%d avg=%.1fns (%.3fµs) p50=%.0f p95=%.0f ops/s=%.0f tracing=%v",
					mode.ID, n, rr.AvgNS, rr.AvgUS, rr.P50NS, rr.P95NS, rr.OpsPerSec, rr.TracingEnabled)
			}
		})
	}

	t.Cleanup(func() {
		rep := Report{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			GoVersion:   runtime.Version(),
			GOOS:        runtime.GOOS,
			GOARCH:      runtime.GOARCH,
			NumCPU:      runtime.NumCPU(),
			Library:     "otel-nats + otel-flags (workspace submodule)",
			Method: "每模式：warmup 200 次 Publish 後，對 N∈{100,1e3,1e4,1e5} 逐次計時 " +
				"conn.Publish(ctx, subject, payload)。payload 固定 ~32B。TracerProvider 使用 AlwaysSample + 記憶體 SpanRecorder（不走網路 OTLP）。" +
				"比較重點：flag_on（每 call OpenFeature）vs env_on_no_flag（有 span 無 OF）vs no_flag_no_env（零閘門）。",
			PayloadB: len(payload),
			Volumes:  vols,
			Modes:    modes,
			Results:  results,
		}
		rep.Comparisons = buildComparisons(results)
		path := evidencePath("gate-latency.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Errorf("mkdir: %v", err)
			return
		}
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Errorf("write %s: %v", path, err)
			return
		}
		t.Logf("wrote %s (%d results)", path, len(results))
	})
}

// discardExporter drops exported spans (measures create/end cost without storage).
type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }

func setupConn(t *testing.T, natsURL string, mode Mode) *otelnats.Conn {
	t.Helper()
	// Isolate env: must Unset (not set empty — empty is ErrInvalidFlagValue).
	for _, k := range []string{envMaster, envModule, envEndpoint, "OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL"} {
		prev, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}

	if mode.MasterEnv != nil {
		t.Setenv(envMaster, *mode.MasterEnv)
	}
	if mode.ModuleEnv != nil {
		t.Setenv(envModule, *mode.ModuleEnv)
	}

	if mode.UseNamedProvider {
		flags := map[string]memprovider.InMemoryFlag{
			flagKeyNATS: memBool(mode.FlagOn),
		}
		if err := openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(flags)); err != nil {
			t.Fatalf("install provider: %v", err)
		}
	} else {
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
	}
	t.Cleanup(func() {
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
	})

	conn, err := otelnats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

func measurePublish(t *testing.T, conn *otelnats.Conn, subj string, payload []byte, n, warmup int) RunResult {
	t.Helper()
	samples := make([]float64, n)
	ctx := context.Background()
	var errs int

	// Wall clock for throughput.
	wall0 := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		if err := conn.Publish(ctx, subj, payload); err != nil {
			errs++
		}
		samples[i] = float64(time.Since(t0).Nanoseconds())
	}
	// Ensure publishes reached the server (amortized flush once).
	_ = conn.NatsConn().Flush()
	total := time.Since(wall0)

	sort.Float64s(samples)
	sum := 0.0
	for _, v := range samples {
		sum += v
	}
	avg := sum / float64(n)
	return RunResult{
		N:         n,
		Warmup:    warmup,
		AvgNS:     avg,
		AvgUS:     avg / 1e3,
		P50NS:     percentile(samples, 0.50),
		P95NS:     percentile(samples, 0.95),
		P99NS:     percentile(samples, 0.99),
		MinNS:     samples[0],
		MaxNS:     samples[n-1],
		TotalMS:   float64(total.Microseconds()) / 1e3,
		OpsPerSec: float64(n) / total.Seconds(),
		Errors:    errs,
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func buildComparisons(results []RunResult) []Comparison {
	by := map[string]map[int]RunResult{}
	for _, r := range results {
		if by[r.ModeID] == nil {
			by[r.ModeID] = map[int]RunResult{}
		}
		by[r.ModeID][r.N] = r
	}
	var out []Comparison
	for _, n := range volumes() {
		base, ok0 := by["no_flag_no_env"][n]
		env, ok1 := by["env_on_no_flag"][n]
		fon, ok2 := by["flag_on"][n]
		foff, ok3 := by["flag_off"][n]
		if !ok0 || !ok1 || !ok2 || !ok3 {
			continue
		}
		c := Comparison{
			N:            n,
			BaseMode:     "no_flag_no_env",
			BaseAvgUS:    base.AvgUS,
			EnvOnMode:    "env_on_no_flag",
			EnvOnAvgUS:   env.AvgUS,
			FlagOnMode:   "flag_on",
			FlagOnAvgUS:  fon.AvgUS,
			FlagOffMode:  "flag_off",
			FlagOffAvgUS: foff.AvgUS,
		}
		c.FlagOnMinusBaseUS = fon.AvgUS - base.AvgUS
		c.FlagOffMinusBaseUS = foff.AvgUS - base.AvgUS
		c.FlagOnMinusEnvOnUS = fon.AvgUS - env.AvgUS
		if base.AvgUS > 0 {
			c.FlagOnOverBaseRatio = fon.AvgUS / base.AvgUS
			c.FlagOffOverBaseRatio = foff.AvgUS / base.AvgUS
		}
		if env.AvgUS > 0 {
			c.FlagOnOverEnvOnRatio = fon.AvgUS / env.AvgUS
		}
		out = append(out, c)
	}
	return out
}

func memBool(v bool) memprovider.InMemoryFlag {
	variant := "off"
	if v {
		variant = "on"
	}
	return memprovider.InMemoryFlag{
		State:          memprovider.Enabled,
		DefaultVariant: variant,
		Variants:       map[string]any{"on": true, "off": false},
	}
}

func evidencePath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "docs", "evidence", "perf", name)
}

