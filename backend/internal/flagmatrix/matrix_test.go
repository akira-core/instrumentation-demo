// Package flagmatrix exercises every meaningful otel-nats feature-flag gate
// and fallback combination, and writes structured evidence to
// docs/evidence/matrix-unit.json for the zh-TW HTML report.
package flagmatrix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	otelflags "github.com/akira-core/instrumentation-go/otel-flags"
	otelnats "github.com/akira-core/instrumentation-go/otel-nats/otelnats"
)

const (
	flagKeyNATS = "otel-nats-tracing"
	envMaster   = "OTEL_INSTRUMENTATION_GO_TRACING_ENABLED"
	envModule   = "OTEL_NATS_TRACING_ENABLED"
	envEndpoint = "OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT"
)

// Case is one matrix row.
type Case struct {
	ID           string `json:"id"`
	TitleZH      string `json:"title_zh"`
	Category     string `json:"category"` // local | relay | invalid | live-order
	ConfigSteps  []string `json:"config_steps"`
	MasterEnv    *string `json:"master_env"`    // nil = unset
	ModuleEnv    *string `json:"module_env"`    // nil = unset
	Option       *bool   `json:"option"`        // nil = no WithTracingEnabled
	RelayMaster  *bool   `json:"relay_master"`  // nil = key absent / no provider when RelayMode none
	RelayModule  *bool   `json:"relay_module"`
	RelayMode    string  `json:"relay_mode"` // none | named-domain | empty-provider
	ExpectOn     bool    `json:"expect_on"`
	ExpectError  bool    `json:"expect_error"`
	ExpectErrIs  string  `json:"expect_err_is,omitempty"`
}

// Result is evidence for one case.
type Result struct {
	Case           Case      `json:"case"`
	Pass           bool      `json:"pass"`
	GotOn          *bool     `json:"got_on,omitempty"`
	GotError       string    `json:"got_error,omitempty"`
	ConnectOK      bool      `json:"connect_ok"`
	Evidence       []string  `json:"evidence"`
	RanAt          time.Time `json:"ran_at"`
	DurationMS     int64     `json:"duration_ms"`
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func allCases() []Case {
	return []Case{
		// --- Local ladder (no relay) ---
		{
			ID: "L01", TitleZH: "全未設定 → 關閉（模組預設 false）", Category: "local",
			ConfigSteps: []string{
				"清除 OTEL_INSTRUMENTATION_GO_TRACING_ENABLED",
				"清除 OTEL_NATS_TRACING_ENABLED",
				"不綁定 OpenFeature named provider",
				"不傳 WithTracingEnabled",
			},
			RelayMode: "none", ExpectOn: false,
		},
		{
			ID: "L02", TitleZH: "僅 master=true → 仍關閉（veto 不是啟用開關）", Category: "local",
			ConfigSteps: []string{
				"設定 OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=true",
				"清除 OTEL_NATS_TRACING_ENABLED",
				"無 relay / 無 option",
			},
			MasterEnv: strPtr("true"), RelayMode: "none", ExpectOn: false,
		},
		{
			ID: "L03", TitleZH: "僅 module env=true → 開啟（master 預設 true）", Category: "local",
			ConfigSteps: []string{
				"清除 master env",
				"設定 OTEL_NATS_TRACING_ENABLED=true",
			},
			ModuleEnv: strPtr("true"), RelayMode: "none", ExpectOn: true,
		},
		{
			ID: "L04", TitleZH: "WithTracingEnabled(true)、env 沉默 → 開啟", Category: "local",
			ConfigSteps: []string{
				"清除 module / master env",
				"ConnectWithOptions(..., WithTracingEnabled(true))",
			},
			Option: boolPtr(true), RelayMode: "none", ExpectOn: true,
		},
		{
			ID: "L05", TitleZH: "WithTracingEnabled(false)、env 沉默 → 關閉", Category: "local",
			ConfigSteps: []string{
				"清除 module / master env",
				"ConnectWithOptions(..., WithTracingEnabled(false))",
			},
			Option: boolPtr(false), RelayMode: "none", ExpectOn: false,
		},
		{
			ID: "L06", TitleZH: "env=false 勝過 option=true → 關閉", Category: "local",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=false",
				"WithTracingEnabled(true)",
			},
			ModuleEnv: strPtr("false"), Option: boolPtr(true), RelayMode: "none", ExpectOn: false,
		},
		{
			ID: "L07", TitleZH: "env=true 勝過 option=false → 開啟", Category: "local",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=true",
				"WithTracingEnabled(false)",
			},
			ModuleEnv: strPtr("true"), Option: boolPtr(false), RelayMode: "none", ExpectOn: true,
		},
		{
			ID: "L08", TitleZH: "master env 否決：module 與 option 皆 on → 關閉", Category: "local",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=false",
				"OTEL_NATS_TRACING_ENABLED=true",
				"WithTracingEnabled(true)",
			},
			MasterEnv: strPtr("false"), ModuleEnv: strPtr("true"), Option: boolPtr(true),
			RelayMode: "none", ExpectOn: false,
		},
		// truthy / falsy tokens
		{
			ID: "L09", TitleZH: "module env 真值 token「1」→ 開啟", Category: "local",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=1"},
			ModuleEnv: strPtr("1"), RelayMode: "none", ExpectOn: true,
		},
		{
			ID: "L10", TitleZH: "module env 假值 token「off」→ 關閉", Category: "local",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=off"},
			ModuleEnv: strPtr("off"), RelayMode: "none", ExpectOn: false,
		},
		// --- Invalid env (construction error) ---
		{
			ID: "E01", TitleZH: "module env 空字串 → 建構錯誤", Category: "invalid",
			ConfigSteps: []string{`export OTEL_NATS_TRACING_ENABLED=""`},
			ModuleEnv: strPtr(""), RelayMode: "none", ExpectError: true, ExpectErrIs: "otel-flags: invalid configuration value",
		},
		{
			ID: "E02", TitleZH: "module env 無法辨識「enabled」→ 建構錯誤", Category: "invalid",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=enabled"},
			ModuleEnv: strPtr("enabled"), RelayMode: "none", ExpectError: true, ExpectErrIs: "otel-flags: invalid configuration value",
		},
		// --- Relay ladder ---
		{
			ID: "R01", TitleZH: "relay 啟用 module、env 沉默 → 開啟（可補上部署未開的 tracing）", Category: "relay",
			ConfigSteps: []string{
				"清除 env",
				"SetNamedProviderAndWait(FlagDomain) 且 otel-nats-tracing=true",
				"先裝 provider 再 Connect",
			},
			RelayMode: "named-domain", RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R02", TitleZH: "relay 關閉 module、env=true → 關閉（relay 雙向權威）", Category: "relay",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=true",
				"relay otel-nats-tracing=false",
			},
			ModuleEnv: strPtr("true"), RelayMode: "named-domain", RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R03", TitleZH: "option C：env=false + relay=true → 開啟（本 demo 預設姿勢）", Category: "relay",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1",
				"OTEL_NATS_TRACING_ENABLED=false",
				"relay otel-nats-tracing=true",
			},
			MasterEnv: strPtr("1"), ModuleEnv: strPtr("false"),
			RelayMode: "named-domain", RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R04", TitleZH: "option C 翻轉：env=false + relay=false → 關閉", Category: "relay",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=false",
				"relay otel-nats-tracing=false",
			},
			ModuleEnv: strPtr("false"), RelayMode: "named-domain", RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R05", TitleZH: "relay master 否決：module relay on + option true → 關閉", Category: "relay",
			ConfigSteps: []string{
				"relay otel-instrumentation-go-tracing=false",
				"relay otel-nats-tracing=true",
				"WithTracingEnabled(true)",
			},
			Option: boolPtr(true), RelayMode: "named-domain",
			RelayMaster: boolPtr(false), RelayModule: boolPtr(true), ExpectOn: false,
		},
		{
			ID: "R06", TitleZH: "master env 否決勝過 enabling relay → 關閉", Category: "relay",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=false",
				"relay otel-nats-tracing=true",
			},
			MasterEnv: strPtr("false"), RelayMode: "named-domain", RelayModule: boolPtr(true), ExpectOn: false,
		},
		{
			ID: "R07", TitleZH: "option=true 仍服從 relay 關閉 → 關閉（不 pin）", Category: "relay",
			ConfigSteps: []string{
				"WithTracingEnabled(true)",
				"relay otel-nats-tracing=false",
			},
			Option: boolPtr(true), RelayMode: "named-domain", RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R08", TitleZH: "option=false 仍服從 relay 開啟 → 開啟", Category: "relay",
			ConfigSteps: []string{
				"WithTracingEnabled(false)",
				"relay otel-nats-tracing=true",
			},
			Option: boolPtr(false), RelayMode: "named-domain", RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R09", TitleZH: "provider 存在但無 key + env=false + option=true → env 勝 option", Category: "relay",
			ConfigSteps: []string{
				"綁定 named provider 但不定義任何 flag key",
				"OTEL_NATS_TRACING_ENABLED=false",
				"WithTracingEnabled(true)",
			},
			ModuleEnv: strPtr("false"), Option: boolPtr(true),
			RelayMode: "empty-provider", ExpectOn: false,
		},
		{
			ID: "R10", TitleZH: "無 provider + module env=true → 僅 env 路徑開啟", Category: "relay",
			ConfigSteps: []string{
				"FlagDomain 綁 NoopProvider",
				"OTEL_NATS_TRACING_ENABLED=1",
			},
			ModuleEnv: strPtr("1"), RelayMode: "none", ExpectOn: true,
		},
	}
}

func TestOtelNatsFeatureFlagMatrix(t *testing.T) {
	// Shared NATS server for all cases.
	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)
	url := srv.ClientURL()

	// Global tracer so spans are real if any case publishes (we only need TracingEnabled()).
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(t.Context())
		otel.SetTracerProvider(otel.GetTracerProvider())
	})

	cases := allCases()
	results := make([]Result, 0, len(cases))

	for _, tc := range cases {
		tc := tc
		t.Run(tc.ID+"_"+sanitize(tc.TitleZH), func(t *testing.T) {
			start := time.Now()
			r := runCase(t, url, tc)
			r.DurationMS = time.Since(start).Milliseconds()
			r.RanAt = start.UTC()
			results = append(results, r)
			if !r.Pass {
				t.Errorf("%s FAILED: evidence=%v got_on=%v err=%q", tc.ID, r.Evidence, r.GotOn, r.GotError)
			}
		})
	}

	// Write evidence JSON after all subtests scheduled — use cleanup on root.
	t.Cleanup(func() {
		// Re-run order may be nondeterministic if parallel; we didn't parallelize.
		path := evidencePath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Errorf("mkdir evidence: %v", err)
			return
		}
		// Collect pass stats by re-reading results slice (filled by subtests on same process).
		payload := map[string]any{
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"library":      "otel-nats + otel-flags (workspace submodule)",
			"ladder":       "relay > env > option > default; tracing = master && module",
			"total":        len(results),
			"passed":       countPass(results),
			"failed":       len(results) - countPass(results),
			"results":      results,
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Errorf("write %s: %v", path, err)
			return
		}
		t.Logf("wrote evidence %s (%d cases)", path, len(results))
	})
}

func countPass(rs []Result) int {
	n := 0
	for _, r := range rs {
		if r.Pass {
			n++
		}
	}
	return n
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 128 && (r == '/' || r == ' ' || r == '→' || r == '(' || r == ')' || r == '，' || r == '、') {
			out = append(out, '_')
			continue
		}
		if r < 32 {
			continue
		}
		out = append(out, r)
	}
	if len(out) > 40 {
		return string(out[:40])
	}
	return string(out)
}

func evidencePath() string {
	_, file, _, _ := runtime.Caller(0)
	// backend/internal/flagmatrix → repo root docs/evidence
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "docs", "evidence", "matrix-unit.json")
}

func runCase(t *testing.T, natsURL string, tc Case) Result {
	t.Helper()
	r := Result{Case: tc, Evidence: []string{}}

	// Isolate env.
	t.Setenv(envEndpoint, "") // never auto-install real GOFF during unit matrix
	for _, k := range []string{envMaster, envModule, envEndpoint, "OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL"} {
		_ = os.Unsetenv(k)
	}
	if tc.MasterEnv != nil {
		t.Setenv(envMaster, *tc.MasterEnv)
		r.Evidence = append(r.Evidence, fmt.Sprintf("SET %s=%q", envMaster, *tc.MasterEnv))
	} else {
		r.Evidence = append(r.Evidence, fmt.Sprintf("UNSET %s", envMaster))
	}
	if tc.ModuleEnv != nil {
		t.Setenv(envModule, *tc.ModuleEnv)
		r.Evidence = append(r.Evidence, fmt.Sprintf("SET %s=%q", envModule, *tc.ModuleEnv))
	} else {
		r.Evidence = append(r.Evidence, fmt.Sprintf("UNSET %s", envModule))
	}

	// Provider setup BEFORE connect.
	switch tc.RelayMode {
	case "none":
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → NoopProvider (no relay opinion)")
	case "empty-provider":
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{}))
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → empty InMemoryProvider (keys absent)")
	case "named-domain":
		flags := map[string]memprovider.InMemoryFlag{}
		if tc.RelayMaster != nil {
			flags[otelflags.FlagKeyGlobalTracing] = memBool(*tc.RelayMaster)
			r.Evidence = append(r.Evidence, fmt.Sprintf("relay %s=%v", otelflags.FlagKeyGlobalTracing, *tc.RelayMaster))
		}
		if tc.RelayModule != nil {
			flags[flagKeyNATS] = memBool(*tc.RelayModule)
			r.Evidence = append(r.Evidence, fmt.Sprintf("relay %s=%v", flagKeyNATS, *tc.RelayModule))
		}
		if err := openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(flags)); err != nil {
			r.GotError = err.Error()
			r.Evidence = append(r.Evidence, "provider install error: "+err.Error())
			r.Pass = false
			return r
		}
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → InMemoryProvider installed BEFORE Connect")
	}
	t.Cleanup(func() {
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
	})

	var opts []otelnats.Option
	if tc.Option != nil {
		opts = append(opts, otelnats.WithTracingEnabled(*tc.Option))
		r.Evidence = append(r.Evidence, fmt.Sprintf("option WithTracingEnabled(%v)", *tc.Option))
	} else {
		r.Evidence = append(r.Evidence, "option: (none)")
	}

	conn, err := otelnats.ConnectWithOptions(natsURL, []nats.Option{}, opts...)
	if err != nil {
		r.GotError = err.Error()
		r.Evidence = append(r.Evidence, "ConnectWithOptions error: "+err.Error())
		if tc.ExpectError {
			ok := true
			if tc.ExpectErrIs != "" && !errors.Is(err, otelflags.ErrInvalidFlagValue) && !contains(err.Error(), "invalid configuration value") {
				// still accept substring match
				if !contains(err.Error(), "invalid") {
					ok = false
					r.Evidence = append(r.Evidence, "error did not look like ErrInvalidFlagValue")
				}
			}
			r.Pass = ok
			r.Evidence = append(r.Evidence, fmt.Sprintf("expect_error=true → pass=%v", ok))
			return r
		}
		r.Pass = false
		r.Evidence = append(r.Evidence, "unexpected construction error")
		return r
	}
	defer conn.Close()
	r.ConnectOK = true

	if tc.ExpectError {
		r.Pass = false
		r.Evidence = append(r.Evidence, "expected construction error but Connect succeeded")
		return r
	}

	on := conn.TracingEnabled()
	r.GotOn = &on
	r.Evidence = append(r.Evidence, fmt.Sprintf("conn.TracingEnabled() = %v (expect %v)", on, tc.ExpectOn))
	r.Pass = on == tc.ExpectOn
	if r.Pass {
		r.Evidence = append(r.Evidence, "RESULT: PASS")
	} else {
		r.Evidence = append(r.Evidence, "RESULT: FAIL")
	}
	return r
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

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()))
}
