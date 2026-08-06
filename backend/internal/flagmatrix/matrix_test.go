// Package flagmatrix exercises every meaningful otel-nats feature-flag gate
// and fallback combination, and writes structured evidence to
// docs/evidence/matrix-unit.json for the zh-TW HTML report.
//
// Four kinds of case, and the last two exist because the first version of this
// matrix asserted only what a connection resolves ONCE, at construction:
//
//   - local   — the env > option > default rungs, with no relay in the process.
//   - relay   — the relay rung, authoritative in both directions.
//   - dynamic — the relay verdict changing on a LIVE connection. This is the
//     whole point of the 0.8.0 per-operation resolution, and a matrix that only
//     reads TracingEnabled() once after Connect cannot tell it apart from a
//     value pinned at construction.
//   - invalid — configuration that must fail construction, including the two
//     process-scoped relay variables (_ENDPOINT, _POLL_INTERVAL) that
//     ValidateAndInstall checks and that no earlier case covered.
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

	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
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
	envPoll     = "OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL"
)

// relayModes.
const (
	relayNone      = "none"           // NoopProvider on FlagDomain — no relay opinion
	relayNamed     = "named-domain"   // provider bound to FlagDomain, serving keys
	relayEmpty     = "empty-provider" // provider bound to FlagDomain, no keys defined
	relayDefaultSl = "default-slot"   // provider bound to the DEFAULT slot only
)

// Flip is a relay change applied to an ALREADY-CONNECTED Conn.
//
// Nothing reconnects between flips. That is the assertion: gateState.tracing()
// re-resolves on every operation, so a connection established while the relay
// said one thing must follow it when it says the other.
type Flip struct {
	TitleZH     string `json:"title_zh"`
	RelayModule *bool  `json:"relay_module"`
	RelayMaster *bool  `json:"relay_master"`
	ExpectOn    bool   `json:"expect_on"`
}

// Case is one matrix row.
type Case struct {
	ID          string   `json:"id"`
	TitleZH     string   `json:"title_zh"`
	Category    string   `json:"category"` // local | relay | dynamic | invalid
	ConfigSteps []string `json:"config_steps"`

	MasterEnv *string `json:"master_env"` // nil = unset
	ModuleEnv *string `json:"module_env"` // nil = unset

	// EndpointEnv and PollEnv are the process-scoped relay variables. They are
	// validated by otelflags.ValidateAndInstall inside every wrapper
	// constructor, so a bad one fails Connect even though this module never
	// names them.
	EndpointEnv *string `json:"endpoint_env"`
	PollEnv     *string `json:"poll_env"`

	Option *bool `json:"option"` // nil = no WithTracingEnabled

	RelayMaster *bool  `json:"relay_master"`
	RelayModule *bool  `json:"relay_module"`
	RelayMode   string `json:"relay_mode"`

	Flips []Flip `json:"flips,omitempty"`

	ExpectOn    bool   `json:"expect_on"`
	ExpectError bool   `json:"expect_error"`
	ExpectErrIs string `json:"expect_err_is,omitempty"`
}

// Result is evidence for one case.
type Result struct {
	Case       Case      `json:"case"`
	Pass       bool      `json:"pass"`
	GotOn      *bool     `json:"got_on,omitempty"`
	GotError   string    `json:"got_error,omitempty"`
	ConnectOK  bool      `json:"connect_ok"`
	Evidence   []string  `json:"evidence"`
	RanAt      time.Time `json:"ran_at"`
	DurationMS int64     `json:"duration_ms"`
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
			RelayMode: relayNone, ExpectOn: false,
		},
		{
			ID: "L02", TitleZH: "僅 master=true → 仍關閉（veto 不是啟用開關）", Category: "local",
			ConfigSteps: []string{
				"設定 OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=true",
				"清除 OTEL_NATS_TRACING_ENABLED",
				"無 relay / 無 option",
			},
			MasterEnv: strPtr("true"), RelayMode: relayNone, ExpectOn: false,
		},
		{
			ID: "L03", TitleZH: "僅 module env=true → 開啟（master 預設 true）", Category: "local",
			ConfigSteps: []string{
				"清除 master env",
				"設定 OTEL_NATS_TRACING_ENABLED=true",
			},
			ModuleEnv: strPtr("true"), RelayMode: relayNone, ExpectOn: true,
		},
		{
			ID: "L04", TitleZH: "WithTracingEnabled(true)、env 沉默 → 開啟", Category: "local",
			ConfigSteps: []string{
				"清除 module / master env",
				"ConnectWithOptions(..., WithTracingEnabled(true))",
			},
			Option: boolPtr(true), RelayMode: relayNone, ExpectOn: true,
		},
		{
			ID: "L05", TitleZH: "WithTracingEnabled(false)、env 沉默 → 關閉", Category: "local",
			ConfigSteps: []string{
				"清除 module / master env",
				"ConnectWithOptions(..., WithTracingEnabled(false))",
			},
			Option: boolPtr(false), RelayMode: relayNone, ExpectOn: false,
		},
		{
			ID: "L06", TitleZH: "env=false 勝過 option=true → 關閉", Category: "local",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=false",
				"WithTracingEnabled(true)",
			},
			ModuleEnv: strPtr("false"), Option: boolPtr(true), RelayMode: relayNone, ExpectOn: false,
		},
		{
			ID: "L07", TitleZH: "env=true 勝過 option=false → 開啟", Category: "local",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=true",
				"WithTracingEnabled(false)",
			},
			ModuleEnv: strPtr("true"), Option: boolPtr(false), RelayMode: relayNone, ExpectOn: true,
		},
		{
			ID: "L08", TitleZH: "master env 否決：module 與 option 皆 on → 關閉", Category: "local",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=false",
				"OTEL_NATS_TRACING_ENABLED=true",
				"WithTracingEnabled(true)",
			},
			MasterEnv: strPtr("false"), ModuleEnv: strPtr("true"), Option: boolPtr(true),
			RelayMode: relayNone, ExpectOn: false,
		},
		{
			ID: "L09", TitleZH: "module env 真值 token「1」→ 開啟", Category: "local",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=1"},
			ModuleEnv:   strPtr("1"), RelayMode: relayNone, ExpectOn: true,
		},
		{
			ID: "L10", TitleZH: "module env 假值 token「off」→ 關閉", Category: "local",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=off"},
			ModuleEnv:   strPtr("off"), RelayMode: relayNone, ExpectOn: false,
		},

		// --- Invalid configuration (construction error) ---
		{
			ID: "E01", TitleZH: "module env 空字串 → 建構錯誤", Category: "invalid",
			ConfigSteps: []string{`export OTEL_NATS_TRACING_ENABLED=""`},
			ModuleEnv:   strPtr(""), RelayMode: relayNone,
			ExpectError: true, ExpectErrIs: "otelflags.ErrInvalidFlagValue",
		},
		{
			ID: "E02", TitleZH: "module env 無法辨識「enabled」→ 建構錯誤", Category: "invalid",
			ConfigSteps: []string{"OTEL_NATS_TRACING_ENABLED=enabled"},
			ModuleEnv:   strPtr("enabled"), RelayMode: relayNone,
			ExpectError: true, ExpectErrIs: "otelflags.ErrInvalidFlagValue",
		},
		{
			ID: "E03", TitleZH: "master env 無法辨識「yes-please」→ 建構錯誤", Category: "invalid",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=yes-please",
				"master 也是嚴格三態，不會猜方向",
			},
			MasterEnv: strPtr("yes-please"), RelayMode: relayNone,
			ExpectError: true, ExpectErrIs: "otelflags.ErrInvalidFlagValue",
		},
		{
			ID: "V01", TitleZH: "_ENDPOINT 缺 scheme/host（relay:1031）→ 建構錯誤", Category: "invalid",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT=relay:1031",
				"「relay:1031」能被 url.Parse 接受（scheme=relay、opaque=1031），但永遠連不到任何東西",
				"所以 ValidateAndInstall 同時檢查 scheme 與 host",
			},
			EndpointEnv: strPtr("relay:1031"), RelayMode: relayNone,
			ExpectError: true, ExpectErrIs: "otelflags.ErrInvalidFlagValue",
		},
		{
			ID: "V02", TitleZH: "_POLL_INTERVAL 是裸整數（60）→ 建構錯誤", Category: "invalid",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL=60",
				"只接受 Go duration 字串（60s / 2m），裸整數會被拒絕而不是當成秒",
			},
			PollEnv: strPtr("60"), RelayMode: relayNone,
			ExpectError: true, ExpectErrIs: "otelflags.ErrInvalidFlagValue",
		},

		// --- Relay ladder ---
		{
			ID: "R01", TitleZH: "relay 啟用 module、env 沉默 → 開啟（可補上部署未開的 tracing）", Category: "relay",
			ConfigSteps: []string{
				"清除 env",
				"SetNamedProviderAndWait(FlagDomain) 且 otel-nats-tracing=true",
				"先裝 provider 再 Connect",
			},
			RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R02", TitleZH: "relay 關閉 module、env=true → 關閉（relay 雙向權威）", Category: "relay",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=true",
				"relay otel-nats-tracing=false",
			},
			ModuleEnv: strPtr("true"), RelayMode: relayNamed, RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R03", TitleZH: "option C：env=false + relay=true → 開啟（本 demo 預設姿勢）", Category: "relay",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1",
				"OTEL_NATS_TRACING_ENABLED=false",
				"relay otel-nats-tracing=true",
			},
			MasterEnv: strPtr("1"), ModuleEnv: strPtr("false"),
			RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R04", TitleZH: "option C 翻轉：env=false + relay=false → 關閉", Category: "relay",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=false",
				"relay otel-nats-tracing=false",
			},
			ModuleEnv: strPtr("false"), RelayMode: relayNamed, RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R05", TitleZH: "relay master 否決：module relay on + option true → 關閉", Category: "relay",
			ConfigSteps: []string{
				"relay otel-instrumentation-go-tracing=false",
				"relay otel-nats-tracing=true",
				"WithTracingEnabled(true)",
			},
			Option: boolPtr(true), RelayMode: relayNamed,
			RelayMaster: boolPtr(false), RelayModule: boolPtr(true), ExpectOn: false,
		},
		{
			ID: "R06", TitleZH: "master env 否決勝過 enabling relay → 關閉", Category: "relay",
			ConfigSteps: []string{
				"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=false",
				"relay otel-nats-tracing=true",
			},
			MasterEnv: strPtr("false"), RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: false,
		},
		{
			ID: "R07", TitleZH: "option=true 仍服從 relay 關閉 → 關閉（不 pin）", Category: "relay",
			ConfigSteps: []string{
				"WithTracingEnabled(true)",
				"relay otel-nats-tracing=false",
			},
			Option: boolPtr(true), RelayMode: relayNamed, RelayModule: boolPtr(false), ExpectOn: false,
		},
		{
			ID: "R08", TitleZH: "option=false 仍服從 relay 開啟 → 開啟", Category: "relay",
			ConfigSteps: []string{
				"WithTracingEnabled(false)",
				"relay otel-nats-tracing=true",
			},
			Option: boolPtr(false), RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: true,
		},
		{
			ID: "R09", TitleZH: "provider 存在但無 key + env=false + option=true → env 勝 option", Category: "relay",
			ConfigSteps: []string{
				"綁定 named provider 但不定義任何 flag key",
				"OTEL_NATS_TRACING_ENABLED=false",
				"WithTracingEnabled(true)",
			},
			ModuleEnv: strPtr("false"), Option: boolPtr(true),
			RelayMode: relayEmpty, ExpectOn: false,
		},
		{
			ID: "R10", TitleZH: "無 provider + module env=true → 僅 env 路徑開啟", Category: "relay",
			ConfigSteps: []string{
				"FlagDomain 綁 NoopProvider",
				"OTEL_NATS_TRACING_ENABLED=1",
			},
			ModuleEnv: strPtr("1"), RelayMode: relayNone, ExpectOn: true,
		},
		{
			ID: "R11", TitleZH: "只裝 default slot 的 provider 不算 relay → 關閉", Category: "relay",
			ConfigSteps: []string{
				"openfeature.SetProviderAndWait(DEFAULT slot) 且 otel-nats-tracing=true",
				"FlagDomain 本身不綁（NoopProvider）",
				"清除所有 env",
				"預期：模組預設 false 勝出 — 應用自己的商業 provider 不該被當成 instrumentation 的 relay",
			},
			RelayMode: relayDefaultSl, RelayModule: boolPtr(true), ExpectOn: false,
		},

		// --- Dynamic re-resolution on a live connection ---
		{
			ID: "D01", TitleZH: "連線後翻 relay：on → off → on（不重連）", Category: "dynamic",
			ConfigSteps: []string{
				"OTEL_NATS_TRACING_ENABLED=false（option C 姿勢）",
				"relay otel-nats-tracing=true，先裝 provider 再 Connect",
				"連線建立後直接改綁 provider，不呼叫 Connect、不關連線",
				"每次改綁後重新讀 conn.TracingEnabled()",
			},
			ModuleEnv: strPtr("false"), MasterEnv: strPtr("1"),
			RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: true,
			Flips: []Flip{
				{TitleZH: "relay 改為 false → 同一條連線應立刻關閉", RelayModule: boolPtr(false), ExpectOn: false},
				{TitleZH: "relay 改回 true → 同一條連線應立刻恢復", RelayModule: boolPtr(true), ExpectOn: true},
			},
		},
		{
			ID: "D02", TitleZH: "連線後 relay master 否決 → 關閉（module key 不變）", Category: "dynamic",
			ConfigSteps: []string{
				"relay otel-nats-tracing=true，Connect 後為開啟",
				"改綁 provider：加上 otel-instrumentation-go-tracing=false，module key 維持 true",
				"預期：master 否決在 module key 之上，短路掉模組自己的 key",
			},
			ModuleEnv: strPtr("false"),
			RelayMode: relayNamed, RelayModule: boolPtr(true), ExpectOn: true,
			Flips: []Flip{
				{
					TitleZH:     "relay 加上 master=false → 關閉",
					RelayMaster: boolPtr(false), RelayModule: boolPtr(true), ExpectOn: false,
				},
				{
					TitleZH:     "relay 拿掉 master 否決 → 恢復",
					RelayMaster: boolPtr(true), RelayModule: boolPtr(true), ExpectOn: true,
				},
			},
		},
		{
			ID: "D03", TitleZH: "無 relay 的連線不受後來綁上的 provider 影響（建構時已定案）", Category: "dynamic",
			ConfigSteps: []string{
				"FlagDomain 綁 NoopProvider、env 全清 → RelayPossible=false",
				"Connect（此時 traced 實作根本沒被建立）",
				"連線之後才綁上 otel-nats-tracing=true 的 provider",
				"預期：仍為關閉 — RelayPossible 在建構時解析，這是文件寫明的順序要求",
			},
			RelayMode: relayNone, ExpectOn: false,
			Flips: []Flip{
				{TitleZH: "事後才綁 provider（otel-nats-tracing=true）→ 仍關閉", RelayModule: boolPtr(true), ExpectOn: false},
			},
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
		path := evidencePath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Errorf("mkdir evidence: %v", err)
			return
		}
		payload := map[string]any{
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"library":      "otel-nats + otel-flags (workspace submodule)",
			"ladder":       "relay > env > option > default; tracing = master && module",
			"total":        len(results),
			"passed":       countPass(results),
			"failed":       len(results) - countPass(results),
			"by_category":  countByCategory(results),
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

func countByCategory(rs []Result) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, r := range rs {
		c := r.Case.Category
		if out[c] == nil {
			out[c] = map[string]int{"total": 0, "passed": 0}
		}
		out[c]["total"]++
		if r.Pass {
			out[c]["passed"]++
		}
	}
	return out
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

// isolateEnv clears every OTEL_* variable the ladder reads and registers
// restoration of whatever the process started with.
//
// t.Setenv is what registers the restore; os.Unsetenv is what actually clears
// the variable, because "" and unset are DIFFERENT states here — an empty module
// variable is a construction error by design (case E01), so clearing by setting
// empty would change the case under test.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{envMaster, envModule, envEndpoint, envPoll} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

func runCase(t *testing.T, natsURL string, tc Case) Result {
	t.Helper()
	r := Result{Case: tc, Evidence: []string{}}

	isolateEnv(t)
	for _, e := range []struct {
		name string
		val  *string
	}{
		{envMaster, tc.MasterEnv},
		{envModule, tc.ModuleEnv},
		{envEndpoint, tc.EndpointEnv},
		{envPoll, tc.PollEnv},
	} {
		if e.val == nil {
			r.Evidence = append(r.Evidence, "UNSET "+e.name)
			continue
		}
		t.Setenv(e.name, *e.val)
		r.Evidence = append(r.Evidence, fmt.Sprintf("SET %s=%q", e.name, *e.val))
	}

	// Provider setup BEFORE connect. RelayPossible() is resolved at construction,
	// so a provider bound afterwards is invisible to the connection — which is
	// itself what case D03 asserts.
	if !installProvider(t, &r, tc.RelayMode, tc.RelayMaster, tc.RelayModule) {
		r.Pass = false
		return r
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
		if !tc.ExpectError {
			r.Pass = false
			r.Evidence = append(r.Evidence, "RESULT: FAIL — unexpected construction error")
			return r
		}
		// One assertion, no substring fallback. Every construction error the
		// ladder can produce — a bad module value, a bad master value, a bad
		// endpoint, a bad poll interval — wraps the same sentinel, and matching
		// on the word "invalid" instead would pass for errors that are not this
		// one at all.
		r.Pass = errors.Is(err, otelflags.ErrInvalidFlagValue)
		if r.Pass {
			r.Evidence = append(r.Evidence, "errors.Is(err, otelflags.ErrInvalidFlagValue) = true", "RESULT: PASS")
		} else {
			r.Evidence = append(r.Evidence, "errors.Is(err, otelflags.ErrInvalidFlagValue) = false", "RESULT: FAIL")
		}
		return r
	}
	defer conn.Close()
	r.ConnectOK = true

	if tc.ExpectError {
		r.Pass = false
		r.Evidence = append(r.Evidence, "RESULT: FAIL — expected construction error but Connect succeeded")
		return r
	}

	on := conn.TracingEnabled()
	r.GotOn = &on
	r.Evidence = append(r.Evidence, fmt.Sprintf("conn.TracingEnabled() = %v (expect %v)", on, tc.ExpectOn))
	if on != tc.ExpectOn {
		r.Pass = false
		r.Evidence = append(r.Evidence, "RESULT: FAIL")
		return r
	}

	// Flips run on the SAME connection. No reconnect, no Close, no new options —
	// only the provider binding changes.
	for i, f := range tc.Flips {
		if !installProvider(t, &r, flipRelayMode(tc.RelayMode), f.RelayMaster, f.RelayModule) {
			r.Pass = false
			return r
		}
		got := conn.TracingEnabled()
		r.Evidence = append(r.Evidence, fmt.Sprintf("flip[%d] %s → conn.TracingEnabled() = %v (expect %v)",
			i, f.TitleZH, got, f.ExpectOn))
		if got != f.ExpectOn {
			r.Pass = false
			r.Evidence = append(r.Evidence, "RESULT: FAIL — 同一條連線未跟上 relay 變更")
			return r
		}
	}

	r.Pass = true
	r.Evidence = append(r.Evidence, "RESULT: PASS")
	return r
}

// flipRelayMode is what a flip binds. A case that started with no relay (D03)
// still binds a real provider on its flip — the assertion there is that the
// already-constructed connection ignores it.
func flipRelayMode(original string) string {
	if original == relayNone {
		return relayNamed
	}
	return original
}

// installProvider binds the provider a case (or a flip) calls for, and reports
// whether it succeeded.
func installProvider(t *testing.T, r *Result, mode string, master, module *bool) bool {
	t.Helper()
	switch mode {
	case relayNone:
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → NoopProvider (no relay opinion)")
	case relayEmpty:
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{}))
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → empty InMemoryProvider (keys absent)")
	case relayDefaultSl:
		// The DEFAULT slot, not FlagDomain. This is an application's own business
		// provider, and otel-flags must not read it as a relay: NamedProviderMetadata
		// falls back to the default provider's metadata when the domain is unbound,
		// so a naive check would see one here.
		flags := map[string]memprovider.InMemoryFlag{}
		if module != nil {
			flags[flagKeyNATS] = memBool(*module)
		}
		if master != nil {
			flags[otelflags.FlagKeyGlobalTracing] = memBool(*master)
		}
		_ = openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, openfeature.NoopProvider{})
		if err := openfeature.SetProviderAndWait(memprovider.NewInMemoryProvider(flags)); err != nil {
			r.GotError = err.Error()
			r.Evidence = append(r.Evidence, "default-slot provider install error: "+err.Error())
			return false
		}
		t.Cleanup(func() { _ = openfeature.SetProviderAndWait(openfeature.NoopProvider{}) })
		r.Evidence = append(r.Evidence,
			"openfeature.SetProviderAndWait(DEFAULT slot) 綁了含 otel-nats-tracing 的 provider",
			"FlagDomain 本身仍是 NoopProvider")
	case relayNamed:
		flags := map[string]memprovider.InMemoryFlag{}
		if master != nil {
			flags[otelflags.FlagKeyGlobalTracing] = memBool(*master)
			r.Evidence = append(r.Evidence, fmt.Sprintf("relay %s=%v", otelflags.FlagKeyGlobalTracing, *master))
		}
		if module != nil {
			flags[flagKeyNATS] = memBool(*module)
			r.Evidence = append(r.Evidence, fmt.Sprintf("relay %s=%v", flagKeyNATS, *module))
		}
		if err := openfeature.SetNamedProviderAndWait(otelflags.FlagDomain, memprovider.NewInMemoryProvider(flags)); err != nil {
			r.GotError = err.Error()
			r.Evidence = append(r.Evidence, "provider install error: "+err.Error())
			return false
		}
		r.Evidence = append(r.Evidence, "OpenFeature FlagDomain → InMemoryProvider installed")
	default:
		t.Fatalf("unknown relay mode %q", mode)
	}
	return true
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
