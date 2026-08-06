// Package flagtargeting asserts that a relay rule keyed on the service name
// actually selects this process — the one link in the chain that neither the
// library's unit tests nor its integration tests close.
//
// otel-flags' unit tests prove the attribute is SENT: they read
// currentEvalCtx().Attributes()["serviceName"] and check it carries
// OTEL_SERVICE_NAME. otel-nats' integration tests prove a real relay is
// REACHABLE, but their flag fixture is a bare defaultRule with no targeting at
// all. Nothing joins the two, so "an operator writes a rule that enables
// instrumentation for one service" is untested at both levels.
//
// That gap has a documented trap sitting in it. otel-flags supplies the service
// name under BOTH spellings, `service.name` and `serviceName`, because the
// semconv name is what a reader expects to see while only the dot-free one can
// be targeted: both query languages read a dot as a nested-path separator, so
// `service.name eq "..."` resolves a path, finds nothing, and matches no
// process at all — silently, on every process, with the flag simply appearing
// not to work. T03 pins that, so deleting the dot-free spelling fails here
// rather than in a cluster.
//
// One case per process, because OTEL_SERVICE_NAME is read inside
// installProviderFromEnv and the install latches for the process lifetime: a
// second case in the same binary would inherit the first's evaluation context.
package flagtargeting

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"

	otelflags "github.com/akira-core/instrumentation-go/otel-flags"
	otelnats "github.com/akira-core/instrumentation-go/otel-nats/otelnats"

	"github.com/akira-core/instrumentation-demo/backend/internal/fakerelay"
)

const (
	flagKeyNATS = "otel-nats-tracing"

	envMaster   = "OTEL_INSTRUMENTATION_GO_TRACING_ENABLED"
	envModule   = "OTEL_NATS_TRACING_ENABLED"
	envEndpoint = "OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT"
	envPoll     = "OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL"
	envService  = "OTEL_SERVICE_NAME"

	// deployedServiceName is what deploy/base/backend.yaml sets, so the rules
	// under test are the rules an operator would really write for this demo.
	deployedServiceName = "demo-backend"

	// readyFlagKey is served true by the relay while its local default is false.
	//
	// otel-flags binds the auto-installed provider with the NON-blocking
	// SetNamedProvider, so Connect returns before the first configuration fetch
	// lands and every key still resolves locally. For a case that expects OFF,
	// "the rule did not match" and "the provider is not ready yet" produce the
	// same answer — polling a key whose relay and local answers differ is what
	// separates them.
	readyFlagKey = "otel-flagtargeting-ready"

	// Worker protocol.
	envWorker    = "FLAGTARGETING_WORKER"
	envCaseID    = "FLAGTARGETING_CASE"
	resultPrefix = "FLAGTARGETING_RESULT "
)

// Case is one targeting configuration.
type Case struct {
	ID      string `json:"id"`
	TitleZH string `json:"title_zh"`

	// Query is the targeting rule the relay serves for otel-nats-tracing.
	Query string `json:"query"`
	// ServiceName is OTEL_SERVICE_NAME in the process under test.
	ServiceName string `json:"service_name"`

	ExpectOn bool   `json:"expect_on"`
	WhyZH    string `json:"why_zh"`

	ConfigSteps []string `json:"config_steps"`
}

// Result is one worker's report.
type Result struct {
	Case     Case     `json:"case"`
	Pass     bool     `json:"pass"`
	GotOn    bool     `json:"got_on"`
	Evidence []string `json:"evidence"`
	GotError string   `json:"got_error,omitempty"`
}

func allCases() []Case {
	return []Case{
		{
			ID:          "T01",
			TitleZH:     "relay 規則 serviceName 命中 → 開啟",
			Query:       `serviceName eq "` + deployedServiceName + `"`,
			ServiceName: deployedServiceName,
			ExpectOn:    true,
			WhyZH: "option C 姿勢下本地是關的，只有 relay 能打開。開啟即證明 OTEL_SERVICE_NAME " +
				"真的以 serviceName 進到 evaluation context，而且 relay 規則吃得到它。",
			ConfigSteps: []string{
				"SET OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=1",
				"SET OTEL_NATS_TRACING_ENABLED=false（本地關閉，只有 relay 能開）",
				"SET OTEL_SERVICE_NAME=" + deployedServiceName,
				"SET OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT=<fake relay>（觸發 auto-install）",
				`relay otel-nats-tracing: targeting [{query: serviceName eq "` + deployedServiceName + `", variation: enabled}], defaultRule: disabled`,
			},
		},
		{
			ID:          "T02",
			TitleZH:     "同一條規則、服務名不符 → 關閉",
			Query:       `serviceName eq "` + deployedServiceName + `"`,
			ServiceName: "other-service",
			ExpectOn:    false,
			WhyZH: "與 T01 只差 OTEL_SERVICE_NAME。關閉即證明規則真的在「篩選」，" +
				"而不是無論如何都套用 enabled — 少了這個對照，T01 也可能只是碰巧全開。",
			ConfigSteps: []string{
				"同 T01，但 SET OTEL_SERVICE_NAME=other-service",
			},
		},
		{
			ID:          "T03",
			TitleZH:     "規則寫成 service.name（帶點）→ 關閉（已知陷阱）",
			Query:       `service.name eq "` + deployedServiceName + `"`,
			ServiceName: deployedServiceName,
			ExpectOn:    false,
			WhyZH: "服務名相符，規則卻不該命中：nikunjy 與 JSONLogic 都把點當成巢狀路徑分隔符，" +
				"所以 service.name 會被拆成 attribute「service」的子欄位「name」而找不到東西。" +
				"otel-flags 兩種拼法都送，就是為了讓 serviceName 這個沒有點的版本可以被 targeting。" +
				"這個案例把陷阱釘住：哪天有人把沒有點的那個拼法拿掉，會在這裡爆，而不是在叢集裡靜靜地誰都選不到。",
			ConfigSteps: []string{
				"同 T01，但 relay 規則寫成 service.name eq \"" + deployedServiceName + "\"",
			},
		},
	}
}

func caseByID(id string) (Case, bool) {
	for _, c := range allCases() {
		if c.ID == id {
			return c, true
		}
	}
	return Case{}, false
}

// TestServiceNameTargeting drives one subprocess per case and writes evidence.
func TestServiceNameTargeting(t *testing.T) {
	if os.Getenv(envWorker) == "1" {
		t.Skip("driver does not run inside a worker process")
	}

	cases := allCases()
	results := make([]Result, 0, len(cases))

	for _, tc := range cases {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			res, err := spawnWorker(t, tc.ID)
			if err != nil {
				t.Fatalf("%s: %v", tc.ID, err)
			}
			results = append(results, res)
			if !res.Pass {
				t.Errorf("%s FAILED: got_on=%v want %v err=%q evidence=%v",
					tc.ID, res.GotOn, tc.ExpectOn, res.GotError, res.Evidence)
			}
		})
	}

	writeEvidence(t, results)
}

func spawnWorker(t *testing.T, id string) (Result, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceNameTargetingWorker$", "-test.v")
	cmd.Env = append(os.Environ(), envWorker+"=1", envCaseID+"="+id)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return Result{}, fmt.Errorf("worker failed: %w\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, resultPrefix) {
			continue
		}
		var res Result
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, resultPrefix)), &res); err != nil {
			return Result{}, fmt.Errorf("decode worker result: %w", err)
		}
		return res, nil
	}
	return Result{}, fmt.Errorf("worker produced no result line\nstdout:\n%s", out)
}

// TestServiceNameTargetingWorker runs ONE case. Spawned by the driver.
func TestServiceNameTargetingWorker(t *testing.T) {
	if os.Getenv(envWorker) != "1" {
		t.Skip("worker process only; driven by TestServiceNameTargeting")
	}

	tc, ok := caseByID(os.Getenv(envCaseID))
	if !ok {
		t.Fatalf("unknown case %q", os.Getenv(envCaseID))
	}
	res := Result{Case: tc, Evidence: []string{}}

	srv := natstest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	relay, err := fakerelay.Start(map[string]fakerelay.Flag{
		// The rule is the ONLY thing that can enable tracing: the local answer
		// is off and the flag's own defaultRule is disabled.
		flagKeyNATS:  fakerelay.Targeted(tc.Query, true, false),
		readyFlagKey: fakerelay.Bool(true),
	})
	if err != nil {
		t.Fatalf("start fake relay: %v", err)
	}
	t.Cleanup(relay.Close)

	// Every variable set before Connect: the auto-install reads OTEL_SERVICE_NAME
	// once, inside the constructor, and latches.
	for _, kv := range [][2]string{
		{envMaster, "1"},
		{envModule, "false"},
		{envService, tc.ServiceName},
		{envEndpoint, relay.URL},
		{envPoll, "2s"},
	} {
		t.Setenv(kv[0], kv[1])
		res.Evidence = append(res.Evidence, fmt.Sprintf("SET %s=%q", kv[0], kv[1]))
	}
	res.Evidence = append(res.Evidence,
		fmt.Sprintf("relay %s: targeting [{query: %s, variation: enabled}], defaultRule: disabled",
			flagKeyNATS, tc.Query))

	conn, err := otelnats.Connect(srv.ClientURL())
	if err != nil {
		res.GotError = err.Error()
		res.Evidence = append(res.Evidence, "Connect error: "+err.Error())
		emit(t, res)
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	if err := waitForRelay(); err != nil {
		res.GotError = err.Error()
		res.Evidence = append(res.Evidence, err.Error())
		emit(t, res)
		t.Fatal(err)
	}
	res.Evidence = append(res.Evidence,
		"provider ready（哨兵旗標 "+readyFlagKey+" 已由 relay 回覆 true，排除「還沒 fetch 完」）")

	res.GotOn = conn.TracingEnabled()
	res.Pass = res.GotOn == tc.ExpectOn
	res.Evidence = append(res.Evidence,
		fmt.Sprintf("conn.TracingEnabled() = %v (expect %v)", res.GotOn, tc.ExpectOn),
		map[bool]string{true: "RESULT: PASS", false: "RESULT: FAIL"}[res.Pass])

	emit(t, res)
}

// waitForRelay blocks until the auto-installed provider has completed its first
// configuration fetch.
func waitForRelay() error {
	resolver := otelflags.NewResolver()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resolver.Value(readyFlagKey, false) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("relay provider never became ready within 30s")
}

func emit(t *testing.T, res Result) {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	fmt.Println(resultPrefix + string(b))
}

func writeEvidence(t *testing.T, results []Result) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	path := filepath.Join(root, "docs", "evidence", "targeting-unit.json")

	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
	}
	payload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"library":      "otel-nats + otel-flags (workspace submodule)",
		"subject": "relay targeting by service name — OTEL_SERVICE_NAME 是否真的能被 relay 規則選中，" +
			"以及 service.name（帶點）為何選不到",
		"provider": "otel-flags 自動安裝的 GO Feature Flag in-process provider，" +
			"設定由行程內的假 relay 供應（真 rule 引擎，不是 in-memory stub）",
		"isolation": "一個 case 一個子行程：OTEL_SERVICE_NAME 在 installProviderFromEnv 內被讀取，" +
			"且安裝在 process 內是不可逆的 latch",
		"total":   len(results),
		"passed":  passed,
		"failed":  len(results) - passed,
		"results": results,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Errorf("mkdir evidence: %v", err)
		return
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
}
