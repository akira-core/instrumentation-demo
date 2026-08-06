// Package flagperf measures otel-nats per-operation latency under the
// feature-flag gate configurations the demo can be deployed in.
//
// Three things distinguish this harness from a plain benchmark loop, and all
// three exist because the earlier version of it got them wrong:
//
//  1. Every mode runs in its OWN PROCESS. otel-flags latches its provider
//     install (installDone, autoInstalled, explicitBind) for the lifetime of a
//     process and the OpenFeature SDK offers no way to unbind a domain, so two
//     modes measured in one process do not measure two configurations — the
//     second inherits the first's latches. See runWorker.
//
//  2. Both PROVIDER POSTURES are measured. Installing an in-memory provider with
//     openfeature.SetNamedProviderAndWait — what a unit test does — leaves
//     autoInstalled false, so every evaluation pays a context.WithTimeout and
//     three provider-registry metadata reads. The deployment sets
//     OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT instead, which auto-installs and
//     latches, skipping both. The two differ by more than the flag lookup does.
//
//  3. The MEASUREMENT INSTRUMENT is measured too. At the zero-gate baseline a
//     Publish costs less than a hundred nanoseconds, which is the same order as
//     the two time.Now calls used to time it and as the clock's own granularity.
//     Both are probed per run and recorded alongside the results, so a reader
//     can see how much of the baseline is the thing being measured.
//
// Focus: what a feature-flag gate costs per operation when a service is
// publishing at volume, against the two things it must be compared with — no
// gate at all, and tracing enabled without a gate.
package flagperf

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
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
	envPoll     = "OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL"

	subject      = "demo.perf.gate"
	rtRequest    = "demo.perf.request"
	rtReply      = "demo.perf.reply"
	rtCorrHeader = "X-Perf-Correlation-Id"

	// pollInterval matches deploy/base/backend.yaml so the auto-installed
	// provider's background poll runs at the deployed cadence during a
	// measurement rather than at the 60s default.
	pollInterval = "2s"
)

// Worker-protocol environment variables. The driver spawns the test binary
// again with these set; see runWorker.
const (
	envWorker  = "FLAGPERF_WORKER"
	envModeID  = "FLAGPERF_MODE"
	envKind    = "FLAGPERF_KIND"
	envOps     = "FLAGPERF_OPS"
	envWorkers = "FLAGPERF_WORKERS"

	// resultPrefix marks the one stdout line the driver parses. go test writes
	// its own output to the same stream, so the payload needs a sentinel.
	resultPrefix = "FLAGPERF_RESULT "
)

// Measurement kinds.
const (
	kindPublish  = "publish"
	kindParallel = "parallel"
	kindRoundTr  = "roundtrip"
)

// Posture is how (and whether) an OpenFeature provider reaches FlagDomain. It is
// the axis the previous version of this benchmark did not have.
const (
	postureNone = "none"        // NoopProvider on FlagDomain; no relay can exist
	postureMem  = "memprovider" // raw SetNamedProviderAndWait, as a unit test installs
	postureRlay = "relay"       // endpoint set; otel-flags auto-installs a GOFF provider
)

// Mode is one gate configuration under test.
type Mode struct {
	ID          string   `json:"id"`
	TitleZH     string   `json:"title_zh"`
	Description string   `json:"description_zh"`
	ConfigSteps []string `json:"config_steps"`

	// Posture selects how a provider reaches FlagDomain, which decides whether
	// the hot path pays the slow providerBound and the per-evaluation timeout
	// context. See the package comment.
	Posture string `json:"posture"`

	// FlagOn is the otel-nats-tracing value the provider serves (memprovider and
	// relay postures only).
	FlagOn bool `json:"flag_on"`

	ModuleEnv *string `json:"module_env"`
	MasterEnv *string `json:"master_env"`

	ExpectTracing       bool `json:"expect_tracing"`
	ExpectRelayPossible bool `json:"expect_relay_possible"`

	// EvalsPerPublish is how many OpenFeature evaluations one Publish performs:
	// gateState.tracing() resolves the master key and then this module's key,
	// and short-circuits at the master only when the master says no.
	EvalsPerPublish int `json:"evals_per_publish"`
}

func strPtr(s string) *string { return &s }

func allModes() []Mode {
	return []Mode{
		{
			ID:      "no_flag_no_env",
			TitleZH: "完全不使用 feature flag / env（零閘門）",
			Description: "不綁 named provider、不設 OTEL_* 旗標變數。RelayPossible=false，模組預設 off，" +
				"熱路徑不做 OpenFeature 評估，也不發 span。這是下限基準，不是任何部署姿勢。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"UNSET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED",
				"UNSET OTEL_NATS_TRACING_ENABLED",
				"FlagDomain → NoopProvider",
				"Connect 不加 WithTracingEnabled",
			},
			Posture:             postureNone,
			ExpectTracing:       false,
			ExpectRelayPossible: false,
			EvalsPerPublish:     0,
		},
		{
			ID:      "env_on_no_flag",
			TitleZH: "僅 env 開啟 tracing（無 feature flag / 無 OpenFeature）",
			Description: "OTEL_NATS_TRACING_ENABLED=true，無 named provider、無 endpoint。會發 span，" +
				"但熱路徑不做 OpenFeature 評估（本地 master∧module 布林）。「要送 trace 但不要 feature flag」的基準。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"UNSET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED（master 預設 true）",
				"SET OTEL_NATS_TRACING_ENABLED=true",
				"FlagDomain → NoopProvider",
			},
			Posture:             postureNone,
			ModuleEnv:           strPtr("true"),
			ExpectTracing:       true,
			ExpectRelayPossible: false,
			EvalsPerPublish:     0,
		},
		{
			ID:      "flag_on_memprovider",
			TitleZH: "flag=enabled，provider 由測試直接綁（in-memory）",
			Description: "openfeature.SetNamedProviderAndWait(FlagDomain, memprovider)，未設 endpoint。" +
				"explicitBind 與 autoInstalled 都是 false，所以每次評估都走 providerBound 慢路徑" +
				"（3 次 provider registry metadata 讀取）並配一個 250ms 的 timeout context。" +
				"這是舊版報告量到的路徑，不是本 demo 部署的路徑。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"openfeature.SetNamedProviderAndWait(FlagDomain, memprovider{otel-nats-tracing=true})",
				"先裝 provider 再 Connect",
			},
			Posture:             postureMem,
			FlagOn:              true,
			ModuleEnv:           strPtr("false"),
			ExpectTracing:       true,
			ExpectRelayPossible: true,
			EvalsPerPublish:     2,
		},
		{
			ID:      "flag_off_memprovider",
			TitleZH: "flag=disabled，provider 由測試直接綁（in-memory）",
			Description: "同上，但 otel-nats-tracing=false。仍每次評估閘門，只是選 direct 實作、不發 span，" +
				"所以與 no_flag_no_env 的差額就是純閘門成本（慢路徑版本）。",
			ConfigSteps: []string{
				"UNSET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT",
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"openfeature.SetNamedProviderAndWait(FlagDomain, memprovider{otel-nats-tracing=false})",
			},
			Posture:             postureMem,
			FlagOn:              false,
			ModuleEnv:           strPtr("false"),
			ExpectTracing:       false,
			ExpectRelayPossible: true,
			EvalsPerPublish:     2,
		},
		{
			ID:      "flag_on_relay",
			TitleZH: "部署姿勢 option C：endpoint 自動安裝 GOFF provider，flag=enabled",
			Description: "設定 OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT 指向 relay，由 otel-flags 自行建立並綁定 " +
				"GO Feature Flag in-process provider（autoInstalled=true）。providerBound 走兩個 atomic load 的快路徑、" +
				"evaluationContext 直接回 context.Background()，但 rule 評估是真的 GOFF。" +
				"這才是 deploy/base/backend.yaml 跑的路徑。",
			ConfigSteps: []string{
				"SET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1",
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"SET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT=<fake relay>",
				"SET OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL=2s",
				"relay 提供 otel-nats-tracing=enabled（master key 刻意不提供，與 demo ConfigMap 一致）",
			},
			Posture:             postureRlay,
			FlagOn:              true,
			MasterEnv:           strPtr("1"),
			ModuleEnv:           strPtr("false"),
			ExpectTracing:       true,
			ExpectRelayPossible: true,
			EvalsPerPublish:     2,
		},
		{
			ID:      "flag_off_relay",
			TitleZH: "部署姿勢 option C，flag=disabled（kill switch 已按下）",
			Description: "同上但 relay 提供 otel-nats-tracing=disabled。仍每次評估閘門、不發 span — " +
				"這是「用 relay 關掉 instrumentation 之後，還留下多少成本」的答案。",
			ConfigSteps: []string{
				"SET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1",
				"SET OTEL_NATS_TRACING_ENABLED=false",
				"SET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT=<fake relay>",
				"relay 提供 otel-nats-tracing=disabled",
			},
			Posture:             postureRlay,
			FlagOn:              false,
			MasterEnv:           strPtr("1"),
			ModuleEnv:           strPtr("false"),
			ExpectTracing:       false,
			ExpectRelayPossible: true,
			EvalsPerPublish:     2,
		},
	}
}

func modeByID(id string) (Mode, bool) {
	for _, m := range allModes() {
		if m.ID == id {
			return m, true
		}
	}
	return Mode{}, false
}

// Stats is the latency distribution of one run of one mode.
//
// The headline is Median, not Avg. A 500,000-sample run on a machine that also
// runs a NATS server and a GC picks up outliers three orders of magnitude above
// the mode, and a single one of those moves an average by more than the effect
// being measured.
type Stats struct {
	Ops          int     `json:"ops"`
	AvgNS        float64 `json:"avg_ns"`
	TrimmedAvgNS float64 `json:"trimmed_avg_ns"`
	MedianNS     float64 `json:"median_ns"`
	P90NS        float64 `json:"p90_ns"`
	P95NS        float64 `json:"p95_ns"`
	P99NS        float64 `json:"p99_ns"`
	MinNS        float64 `json:"min_ns"`
	MaxNS        float64 `json:"max_ns"`
	StddevNS     float64 `json:"stddev_ns"`

	// WallMS excludes the trailing Flush; FlushMS reports it separately. The
	// previous version folded the flush into the wall clock and then derived an
	// ops/sec from it that disagreed with its own average.
	WallMS    float64 `json:"wall_ms"`
	FlushMS   float64 `json:"flush_ms"`
	OpsPerSec float64 `json:"ops_per_sec"`

	MallocsPerOp float64 `json:"mallocs_per_op"`
	BytesPerOp   float64 `json:"bytes_per_op"`

	Errors   int `json:"errors"`
	Timeouts int `json:"timeouts"`
}

// RunResult is one worker process's report.
type RunResult struct {
	ModeID         string `json:"mode_id"`
	Kind           string `json:"kind"`
	Repeat         int    `json:"repeat"`
	Workers        int    `json:"workers"`
	Warmup         int    `json:"warmup"`
	TracingEnabled bool   `json:"tracing_enabled"`
	Stats          Stats  `json:"stats"`

	// ClockGranularityNS and TimingOverheadNS are probed inside the same process
	// as the measurement, because they are the floor under every number in it.
	ClockGranularityNS float64 `json:"clock_granularity_ns"`
	TimingOverheadNS   float64 `json:"timing_overhead_ns"`
	GOMAXPROCS         int     `json:"gomaxprocs"`
}

// ---------------------------------------------------------------------------
// Statistics
// ---------------------------------------------------------------------------

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

// trimmedMean drops the lowest and highest `frac` of the distribution before
// averaging, so one scheduler stall or GC pause cannot set the headline.
func trimmedMean(sorted []float64, frac float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	cut := int(float64(n) * frac)
	if 2*cut >= n {
		return percentile(sorted, 0.5)
	}
	body := sorted[cut : n-cut]
	sum := 0.0
	for _, v := range body {
		sum += v
	}
	return sum / float64(len(body))
}

func summarize(samples []float64, wall, flush time.Duration, errs, timeouts int, mem memDelta) Stats {
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	n := float64(len(sorted))
	avg := 0.0
	if n > 0 {
		avg = sum / n
	}
	variance := 0.0
	for _, v := range sorted {
		d := v - avg
		variance += d * d
	}
	if n > 1 {
		variance /= n - 1
	}

	st := Stats{
		Ops:          len(sorted),
		AvgNS:        avg,
		TrimmedAvgNS: trimmedMean(sorted, 0.05),
		MedianNS:     percentile(sorted, 0.50),
		P90NS:        percentile(sorted, 0.90),
		P95NS:        percentile(sorted, 0.95),
		P99NS:        percentile(sorted, 0.99),
		StddevNS:     math.Sqrt(variance),
		WallMS:       float64(wall.Microseconds()) / 1e3,
		FlushMS:      float64(flush.Microseconds()) / 1e3,
		Errors:       errs,
		Timeouts:     timeouts,
	}
	if len(sorted) > 0 {
		st.MinNS = sorted[0]
		st.MaxNS = sorted[len(sorted)-1]
	}
	if wall > 0 {
		st.OpsPerSec = n / wall.Seconds()
	}
	if n > 0 {
		st.MallocsPerOp = float64(mem.mallocs) / n
		st.BytesPerOp = float64(mem.bytes) / n
	}
	return st
}

// memDelta is the allocation cost of the measured window. otel-flags documents
// "roughly 2 µs and 7 allocations per call" for one evaluation; recording this
// is what turns that from a claim into something the evidence can check.
type memDelta struct {
	mallocs uint64
	bytes   uint64
}

func readMem() runtime.MemStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms
}

func diffMem(before, after runtime.MemStats) memDelta {
	return memDelta{
		mallocs: after.Mallocs - before.Mallocs,
		bytes:   after.TotalAlloc - before.TotalAlloc,
	}
}

// probeClockGranularity returns the smallest non-zero step the monotonic clock
// reports. On a platform where it is tens of nanoseconds, every sample near the
// zero-gate baseline is only a couple of ticks wide and its percentiles are
// quantisation artefacts rather than measurements.
func probeClockGranularity() float64 {
	best := math.MaxFloat64
	for i := 0; i < 1000; i++ {
		t0 := time.Now()
		var d time.Duration
		for d == 0 {
			d = time.Since(t0)
		}
		if v := float64(d.Nanoseconds()); v > 0 && v < best {
			best = v
		}
	}
	if best == math.MaxFloat64 {
		return 0
	}
	return best
}

// probeTimingOverhead returns the cost of the time.Now/time.Since pair that
// brackets every sample, measured the same way the samples are.
func probeTimingOverhead() float64 {
	const n = 200_000
	samples := make([]float64, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		samples[i] = float64(time.Since(t0).Nanoseconds())
	}
	sort.Float64s(samples)
	return trimmedMean(samples, 0.05)
}

// ---------------------------------------------------------------------------
// Process environment
// ---------------------------------------------------------------------------

// EnvInfo records everything about the host that changes the numbers. The
// previous evidence was captured on a laptop and applied to a container; the
// cgroup quota is here so that mistake is visible rather than implicit.
type EnvInfo struct {
	GoVersion    string `json:"go_version"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	NumCPU       int    `json:"num_cpu"`
	GOMAXPROCS   int    `json:"gomaxprocs"`
	CgroupCPUMax string `json:"cgroup_cpu_max"`
	GOGC         string `json:"gogc"`
	NatsServer   string `json:"nats_server_version"`
}

func collectEnvInfo() EnvInfo {
	gogc := os.Getenv("GOGC")
	if gogc == "" {
		gogc = "100 (default)"
	}
	return EnvInfo{
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		GOMAXPROCS:   runtime.GOMAXPROCS(0),
		CgroupCPUMax: readCgroupCPUMax(),
		GOGC:         gogc,
		NatsServer:   natsserver.VERSION,
	}
}

// readCgroupCPUMax reports the container CPU quota, cgroup v2 first and v1 as a
// fallback, or "" when the process is not limited.
func readCgroupCPUMax() string {
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "" {
			return "cpu.max=" + v
		}
	}
	quota, qErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, pErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if qErr == nil && pErr == nil {
		return fmt.Sprintf("cfs_quota_us=%s cfs_period_us=%s",
			strings.TrimSpace(string(quota)), strings.TrimSpace(string(period)))
	}
	return ""
}

// ---------------------------------------------------------------------------
// Worker setup
// ---------------------------------------------------------------------------

// discardExporter drops exported spans, so the traced modes pay span creation,
// attribute building and W3C injection but not OTLP serialisation or network.
// That is deliberate — the comparison here is between gates, and holding the
// span cost constant and minimal is what keeps the gate delta readable — but it
// means the traced modes UNDER-state a production process that exports.
type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }

// isolateEnv clears every variable otel-flags reads, so a worker inherits
// nothing from the driver's environment. Empty is not the same as unset here:
// an empty module variable is a construction error by design.
func isolateEnv(t testing.TB) {
	t.Helper()
	for _, k := range []string{envMaster, envModule, envEndpoint, envPoll} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// setupConn applies one mode's configuration and returns a connected Conn whose
// gate has been verified to resolve the way the mode says it should.
func setupConn(t testing.TB, natsURL string, mode Mode) *otelnats.Conn {
	t.Helper()
	isolateEnv(t)

	if mode.MasterEnv != nil {
		t.Setenv(envMaster, *mode.MasterEnv)
	}
	if mode.ModuleEnv != nil {
		t.Setenv(envModule, *mode.ModuleEnv)
	}

	switch mode.Posture {
	case postureNone:
		// A NoopProvider on the domain reads as "unbound" to otel-flags, which is
		// what keeps RelayPossible false and the gate arithmetic static.
		if err := openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{}); err != nil {
			t.Fatalf("install NoopProvider: %v", err)
		}
	case postureMem:
		flags := map[string]memprovider.InMemoryFlag{
			flagKeyNATS:  memBool(mode.FlagOn),
			readyFlagKey: memBool(true),
		}
		if err := openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(flags)); err != nil {
			t.Fatalf("install memprovider: %v", err)
		}
	case postureRlay:
		relay := startFakeRelay(t, mode.FlagOn)
		t.Setenv(envEndpoint, relay.URL)
		t.Setenv(envPoll, pollInterval)
	default:
		t.Fatalf("unknown posture %q", mode.Posture)
	}

	conn, err := otelnats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	if mode.Posture == postureRlay {
		waitForRelay(t)
	}
	if got := conn.TracingEnabled(); got != mode.ExpectTracing {
		t.Fatalf("mode %s: TracingEnabled()=%v want %v", mode.ID, got, mode.ExpectTracing)
	}
	return conn
}

// waitForRelay blocks until the auto-installed provider has completed its first
// configuration fetch.
//
// otel-flags binds it with the NON-blocking openfeature.SetNamedProvider, so
// Connect returns while every key still resolves to its local value. Measuring
// there would time the local fallback and label it the relay. The sentinel key
// is served true by the relay and defaults false locally, so it answers "ready"
// unambiguously — which conn.TracingEnabled() cannot do for a mode whose relay
// value and local value are both false.
func waitForRelay(t testing.TB) {
	t.Helper()
	resolver := otelflags.NewResolver()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resolver.Value(readyFlagKey, false) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("relay provider never became ready within 30s")
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

func newTracerProvider() *sdktrace.TracerProvider {
	// AlwaysSample so the traced modes really build spans: a sampler that drops
	// them would measure the cheapest possible traced path and call it tracing.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(discardExporter{})),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp
}

// ---------------------------------------------------------------------------
// The three measurements
// ---------------------------------------------------------------------------

const warmupOps = 5_000

var payload = []byte(`{"bench":"gate-latency","n":0}`)

// measurePublish times conn.Publish serially. One Publish resolves the gate
// once — two OpenFeature evaluations when a relay is possible.
func measurePublish(conn *otelnats.Conn, ops int) Stats {
	ctx := context.Background()
	samples := make([]float64, ops)
	var errs int

	runtime.GC()
	before := readMem()
	wall0 := time.Now()
	for i := 0; i < ops; i++ {
		t0 := time.Now()
		err := conn.Publish(ctx, subject, payload)
		samples[i] = float64(time.Since(t0).Nanoseconds())
		if err != nil {
			errs++
		}
	}
	wall := time.Since(wall0)
	after := readMem()

	flush0 := time.Now()
	_ = conn.NatsConn().Flush()
	flush := time.Since(flush0)

	return summarize(samples, wall, flush, errs, 0, diffMem(before, after))
}

// measureParallel runs the same Publish across `workers` goroutines.
//
// This is the measurement the previous version did not have, and the one most
// likely to change a decision: an OpenFeature evaluation takes the SDK's
// provider-registry lock and walks its hook chain, so a cost that looks flat
// when one goroutine pays it need not stay flat when every request-handling
// goroutine does.
func measureParallel(conn *otelnats.Conn, ops, workers int) Stats {
	per := ops / workers
	buckets := make([][]float64, workers)
	errCounts := make([]int, workers)
	for i := range buckets {
		buckets[i] = make([]float64, per)
	}

	var wg sync.WaitGroup
	runtime.GC()
	before := readMem()
	wall0 := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < per; i++ {
				t0 := time.Now()
				err := conn.Publish(ctx, subject, payload)
				buckets[w][i] = float64(time.Since(t0).Nanoseconds())
				if err != nil {
					errCounts[w]++
				}
			}
		}(w)
	}
	wg.Wait()
	wall := time.Since(wall0)
	after := readMem()

	flush0 := time.Now()
	_ = conn.NatsConn().Flush()
	flush := time.Since(flush0)

	samples := make([]float64, 0, per*workers)
	errs := 0
	for w := range buckets {
		samples = append(samples, buckets[w]...)
		errs += errCounts[w]
	}
	return summarize(samples, wall, flush, errs, 0, diffMem(before, after))
}

// roundTrip is the demo's own request/reply shape: publish a request, an
// in-process subscriber replies, a second subscriber resolves the correlation
// ID back to the waiting caller. It mirrors natsflow.Manager minus its
// simulated 5ms of work.
//
// It matters because one round trip resolves the gate FOUR times, not once:
// the request publish, the request handler, the reply publish, and the reply
// handler. At two OpenFeature evaluations each that is eight per demo request —
// the number a capacity estimate needs and a single-Publish benchmark hides.
type roundTrip struct {
	conn    *otelnats.Conn
	pending sync.Map // correlation ID -> chan struct{}
}

func newRoundTrip(t testing.TB, conn *otelnats.Conn) *roundTrip {
	t.Helper()
	rt := &roundTrip{conn: conn}

	if _, err := conn.Subscribe(rtRequest, func(m otelnats.Msg) {
		ctx := m.Context()
		msg := &nats.Msg{Subject: rtReply, Data: []byte("ok"), Header: nats.Header{}}
		if corr := m.Msg.Header.Get(rtCorrHeader); corr != "" {
			msg.Header.Set(rtCorrHeader, corr)
		}
		_ = rt.conn.PublishMsg(ctx, msg)
	}); err != nil {
		t.Fatalf("subscribe %s: %v", rtRequest, err)
	}

	if _, err := conn.Subscribe(rtReply, func(m otelnats.Msg) {
		corr := m.Msg.Header.Get(rtCorrHeader)
		if corr == "" {
			return
		}
		if ch, ok := rt.pending.LoadAndDelete(corr); ok {
			close(ch.(chan struct{}))
		}
	}); err != nil {
		t.Fatalf("subscribe %s: %v", rtReply, err)
	}
	return rt
}

const rtTimeout = 10 * time.Second

// do performs one round trip and reports whether the reply arrived in time.
func (rt *roundTrip) do(ctx context.Context, corr string) (bool, error) {
	ch := make(chan struct{})
	rt.pending.Store(corr, ch)
	defer rt.pending.Delete(corr)

	msg := &nats.Msg{Subject: rtRequest, Data: []byte("demo-perf-request"), Header: nats.Header{}}
	msg.Header.Set(rtCorrHeader, corr)
	if err := rt.conn.PublishMsg(ctx, msg); err != nil {
		return false, err
	}

	timer := time.NewTimer(rtTimeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true, nil
	case <-timer.C:
		return false, nil
	}
}

// measureRoundTrip runs `ops` round trips spread over `workers` in-flight
// callers. Serial round trips would be bounded by the reply latency of a single
// outstanding message and would take far longer than the publish measurements
// for a result nobody deploys.
func measureRoundTrip(rt *roundTrip, ops, workers int) Stats {
	per := ops / workers
	buckets := make([][]float64, workers)
	errCounts := make([]int, workers)
	toCounts := make([]int, workers)
	for i := range buckets {
		buckets[i] = make([]float64, 0, per)
	}

	var wg sync.WaitGroup
	runtime.GC()
	before := readMem()
	wall0 := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ctx := context.Background()
			base := w * per
			for i := 0; i < per; i++ {
				// strconv rather than Sprintf: the correlation ID is built
				// inside the timed window, and Sprintf's reflection would show
				// up in this measurement's allocs/op as if the round trip had
				// caused it.
				corr := strconv.Itoa(base + i)
				t0 := time.Now()
				ok, err := rt.do(ctx, corr)
				elapsed := float64(time.Since(t0).Nanoseconds())
				switch {
				case err != nil:
					errCounts[w]++
				case !ok:
					toCounts[w]++
				default:
					buckets[w] = append(buckets[w], elapsed)
				}
			}
		}(w)
	}
	wg.Wait()
	wall := time.Since(wall0)
	after := readMem()

	samples := make([]float64, 0, per*workers)
	errs, timeouts := 0, 0
	for w := range buckets {
		samples = append(samples, buckets[w]...)
		errs += errCounts[w]
		timeouts += toCounts[w]
	}
	return summarize(samples, wall, 0, errs, timeouts, diffMem(before, after))
}

// ---------------------------------------------------------------------------
// Worker entry point
// ---------------------------------------------------------------------------

// TestFlagPerfWorker measures ONE mode and prints one JSON line. It is spawned
// by the driver, never run directly: a process may hold only one otel-flags
// provider-install decision, so one process may measure only one mode.
func TestFlagPerfWorker(t *testing.T) {
	if os.Getenv(envWorker) != "1" {
		t.Skip("worker process only; driven by TestGateLatencyEvidence")
	}
	runWorker(t)
}

func runWorker(t testing.TB) {
	t.Helper()

	modeID := os.Getenv(envModeID)
	mode, ok := modeByID(modeID)
	if !ok {
		t.Fatalf("unknown mode %q", modeID)
	}
	kind := os.Getenv(envKind)
	ops := mustAtoi(t, os.Getenv(envOps))
	workers := mustAtoi(t, os.Getenv(envWorkers))
	if workers < 1 {
		workers = 1
	}

	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	tp := newTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	conn := setupConn(t, srv.ClientURL(), mode)

	res := RunResult{
		ModeID:             mode.ID,
		Kind:               kind,
		Workers:            workers,
		Warmup:             warmupOps,
		TracingEnabled:     conn.TracingEnabled(),
		ClockGranularityNS: probeClockGranularity(),
		TimingOverheadNS:   probeTimingOverhead(),
		GOMAXPROCS:         runtime.GOMAXPROCS(0),
	}

	switch kind {
	case kindPublish:
		warmPublish(conn, warmupOps)
		res.Stats = measurePublish(conn, ops)
	case kindParallel:
		warmPublish(conn, warmupOps)
		res.Stats = measureParallel(conn, ops, workers)
	case kindRoundTr:
		rt := newRoundTrip(t, conn)
		warmRoundTrip(t, rt, 500, workers)
		res.Stats = measureRoundTrip(rt, ops, workers)
	default:
		t.Fatalf("unknown kind %q", kind)
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	fmt.Println(resultPrefix + string(b))
}

func warmPublish(conn *otelnats.Conn, n int) {
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_ = conn.Publish(ctx, subject, payload)
	}
	_ = conn.NatsConn().Flush()
	// Return the warmup's garbage before the timed window opens, so the first
	// timed operations do not pay for it.
	runtime.GC()
	debug.FreeOSMemory()
}

func warmRoundTrip(t testing.TB, rt *roundTrip, n, workers int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := rt.do(ctx, fmt.Sprintf("warm-%d", i)); err != nil {
			t.Fatalf("warmup round trip: %v", err)
		}
	}
	runtime.GC()
	debug.FreeOSMemory()
	_ = workers
}

func mustAtoi(t testing.TB, s string) int {
	t.Helper()
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("parse int %q: %v", s, err)
	}
	return v
}
