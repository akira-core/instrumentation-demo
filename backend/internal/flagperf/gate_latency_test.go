package flagperf

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// defaultOps is the request volume every mode is measured at.
//
// One volume, deliberately. The previous report swept N ∈ {100, 1e3, 1e4, 1e5}
// and published a ratio column off the smallest of them, where a single 148µs
// outlier in 100 samples moves the average by 1.5µs and the zero-gate baseline
// is only a couple of clock ticks wide. Those rows measured warm-up and
// quantisation, not the gate, and presenting them beside the steady-state rows
// implied a volume trend that was not there. Only the volume a busy service
// actually reaches is kept.
const defaultOps = 500_000

// defaultRepeats independent processes per mode. The spread between them is
// reported, because a single run of anything on a shared machine is an anecdote.
const defaultRepeats = 5

// roundTripRepeats is lower because a round trip costs two orders of magnitude
// more than a Publish, so the same number of repeats would multiply the run into
// hours for a spread figure that three samples already show.
const roundTripRepeats = 3

// Driver-side overrides, so a smoke run does not need a code change.
const (
	envTotalOps  = "FLAGPERF_TOTAL_OPS"
	envRTOps     = "FLAGPERF_RT_OPS"
	envRepeats   = "FLAGPERF_REPEATS"
	envRTRepeats = "FLAGPERF_RT_REPEATS"
	envSkipKinds = "FLAGPERF_SKIP_KINDS"
)

// roundTripModes is the subset measured end-to-end. The two memprovider modes
// are omitted: their publish numbers already show what that posture costs, and
// repeating it through a round trip buys a longer run rather than a new fact.
var roundTripModes = map[string]bool{
	"no_flag_no_env": true,
	"env_on_no_flag": true,
	"flag_on_relay":  true,
	"flag_off_relay": true,
}

// Aggregate collapses the repeats of one mode × kind.
//
// The headline is the median of the per-run medians. Averaging averages would
// let one bad run set the number, which is the failure this whole structure
// exists to prevent.
type Aggregate struct {
	ModeID  string `json:"mode_id"`
	Kind    string `json:"kind"`
	Repeats int    `json:"repeats"`
	Workers int    `json:"workers"`

	MedianNS        float64 `json:"median_ns"`
	MedianUS        float64 `json:"median_us"`
	TrimmedAvgNS    float64 `json:"trimmed_avg_ns"`
	AvgNS           float64 `json:"avg_ns"`
	P95NS           float64 `json:"p95_ns"`
	P99NS           float64 `json:"p99_ns"`
	MinRunMedianNS  float64 `json:"min_run_median_ns"`
	MaxRunMedianNS  float64 `json:"max_run_median_ns"`
	RunSpreadPct    float64 `json:"run_spread_pct"`
	OpsPerSec       float64 `json:"ops_per_sec"`
	MallocsPerOp    float64 `json:"mallocs_per_op"`
	BytesPerOp      float64 `json:"bytes_per_op"`
	TracingEnabled  bool    `json:"tracing_enabled"`
	Errors          int     `json:"errors"`
	Timeouts        int     `json:"timeouts"`
	EvalsPerOp      int     `json:"evals_per_op"`
	ClockGranNS     float64 `json:"clock_granularity_ns"`
	TimingOverhdNS  float64 `json:"timing_overhead_ns"`
	MedianMinusOvhd float64 `json:"median_minus_timing_overhead_ns"`
}

// Comparison is one derived statement the report makes, kept as data so the
// HTML cannot drift from the numbers.
type Comparison struct {
	Kind        string  `json:"kind"`
	Label       string  `json:"label_zh"`
	LeftMode    string  `json:"left_mode"`
	RightMode   string  `json:"right_mode"`
	LeftUS      float64 `json:"left_us"`
	RightUS     float64 `json:"right_us"`
	DeltaUS     float64 `json:"delta_us"`
	Ratio       float64 `json:"ratio"`
	Explanation string  `json:"explanation_zh"`
}

// Report is the evidence payload.
type Report struct {
	GeneratedAt string  `json:"generated_at"`
	Library     string  `json:"library"`
	Env         EnvInfo `json:"env"`

	OpsPerRun        int `json:"ops_per_run"`
	RoundTripOps     int `json:"round_trip_ops"`
	Repeats          int `json:"repeats"`
	RoundTripRepeats int `json:"round_trip_repeats"`
	ConcurrentWorker int `json:"concurrent_workers"`
	WarmupOps        int `json:"warmup_ops"`
	PayloadBytes     int `json:"payload_bytes"`

	Method   string   `json:"method_zh"`
	Caveats  []string `json:"caveats_zh"`
	Isolated string   `json:"process_isolation_zh"`

	Modes       []Mode       `json:"modes"`
	Runs        []RunResult  `json:"runs"`
	Aggregates  []Aggregate  `json:"aggregates"`
	Comparisons []Comparison `json:"comparisons"`
}

// TestGateLatencyEvidence drives the whole measurement and writes the evidence
// JSON. Each mode × kind × repeat is a fresh process; see the package comment
// for why that is not optional.
func TestGateLatencyEvidence(t *testing.T) {
	if os.Getenv(envWorker) == "1" {
		t.Skip("driver does not run inside a worker process")
	}
	if testing.Short() {
		t.Skip("-short: 500,000-operation measurement skipped")
	}

	ops := intFromEnv(envTotalOps, defaultOps)
	rtOps := intFromEnv(envRTOps, ops)
	repeats := intFromEnv(envRepeats, defaultRepeats)
	rtRepeats := intFromEnv(envRTRepeats, roundTripRepeats)
	skip := skipKinds()

	// Oversubscribed on purpose: a request-handling pool has more goroutines
	// than cores, and lock contention inside the OpenFeature evaluation pipeline
	// is exactly what an at-parallelism number is for.
	//
	// Not oversubscribed FURTHER for the round trip, though. Each in-flight
	// round trip parks waiting for a reply that a subscriber goroutine on the
	// same two cores has to produce, so a deep in-flight window stops measuring
	// service time and starts measuring the queue in front of it.
	workers := 4 * runtime.GOMAXPROCS(0)
	if workers < 4 {
		workers = 4
	}

	modes := allModes()
	var runs []RunResult

	for _, kind := range []string{kindPublish, kindParallel, kindRoundTr} {
		if skip[kind] {
			t.Logf("skipping kind %s (%s)", kind, envSkipKinds)
			continue
		}
		for _, mode := range modes {
			if kind == kindRoundTr && !roundTripModes[mode.ID] {
				continue
			}
			n, w, reps := ops, 1, repeats
			switch kind {
			case kindParallel:
				w = workers
			case kindRoundTr:
				n, w, reps = rtOps, workers, rtRepeats
			}
			for rep := 1; rep <= reps; rep++ {
				res, err := spawnWorker(t, mode.ID, kind, n, w)
				if err != nil {
					t.Fatalf("mode=%s kind=%s repeat=%d: %v", mode.ID, kind, rep, err)
				}
				res.Repeat = rep
				runs = append(runs, res)
				t.Logf("%-22s %-9s rep=%d median=%8.0fns p99=%9.0fns allocs/op=%5.2f ops/s=%10.0f tracing=%v",
					mode.ID, kind, rep, res.Stats.MedianNS, res.Stats.P99NS,
					res.Stats.MallocsPerOp, res.Stats.OpsPerSec, res.TracingEnabled)
			}
		}
	}

	if len(runs) == 0 {
		t.Fatal("no runs completed")
	}

	rep := Report{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		Library:          "otel-nats + otel-flags (workspace submodule)",
		Env:              collectEnvInfo(),
		OpsPerRun:        ops,
		RoundTripOps:     rtOps,
		Repeats:          repeats,
		RoundTripRepeats: rtRepeats,
		ConcurrentWorker: workers,
		WarmupOps:        warmupOps,
		PayloadBytes:     len(payload),
		Method:           methodZH,
		Caveats:          caveatsZH,
		Isolated:         isolationZH,
		Modes:            modes,
		Runs:             runs,
	}
	rep.Aggregates = aggregate(runs, modes)
	rep.Comparisons = compare(rep.Aggregates)

	// A reduced run must never overwrite the published evidence. The knobs above
	// exist so the harness can be exercised cheaply — by a smoke run, or by a
	// plain `go test ./...` someone shortened — and a file called evidence that
	// silently came from 5,000 operations and one repeat is worse than no file.
	if reduced(ops, rtOps, repeats, rtRepeats) || len(skip) > 0 {
		t.Logf("reduced run (ops=%d rt_ops=%d repeats=%d rt_repeats=%d skipped=%v): "+
			"evidence NOT written; a full run is ops=%d repeats=%d rt_repeats=%d",
			ops, rtOps, repeats, rtRepeats, skip, defaultOps, defaultRepeats, roundTripRepeats)
		return
	}
	writeEvidence(t, rep)
}

// reduced reports whether this run measured less than the published evidence is
// defined to contain.
func reduced(ops, rtOps, repeats, rtRepeats int) bool {
	return ops < defaultOps ||
		rtOps < defaultOps ||
		repeats < defaultRepeats ||
		rtRepeats < roundTripRepeats
}

// methodZH describes what the code does. It is a constant next to the code it
// describes because the previous version's method text claimed an in-memory
// SpanRecorder while the code used a discarding exporter, and nothing caught it.
const methodZH = "每個 mode × kind × repeat 都是一個獨立子行程（otel-flags 的 provider 安裝在 process 內是不可逆的 latch，" +
	"同一行程量兩個 mode 會讓第二個繼承第一個的 latch）。每個子行程：啟動 in-process NATS server、" +
	"安裝 TracerProvider（AlwaysSample + SimpleSpanProcessor + 丟棄用 exporter，不做 OTLP 序列化也不走網路）、" +
	"套用該 mode 的 env 與 provider 姿勢、Connect、驗證 conn.TracingEnabled() 與預期相符、" +
	"warmup 5,000 次後 runtime.GC()，再對每次操作以 time.Now/time.Since 逐次計時。" +
	"publish=serial conn.Publish；parallel=同一 Conn 上 4×GOMAXPROCS 條 goroutine 併發 Publish；" +
	"roundtrip=publish→subscriber 回覆→第二個 subscriber 以 correlation id 喚醒呼叫端（等同 natsflow 的 demo 往返，扣掉 5ms 模擬工作）。" +
	"主指標是各 repeat 中位數的中位數；平均值一併保留但不當標題。" +
	"allocs/op 由計時區間前後的 runtime.ReadMemStats 差值求得。"

var caveatsZH = []string{
	"span 端刻意用丟棄用 exporter，所以 traced 模式不含 OTLP 序列化與網路匯出成本 — 對照生產是低估的。",
	"flag_on_relay / flag_off_relay 連的是行程內的假 relay（httptest），HTTP 與網路延遲不在熱路徑上：" +
		"GOFF in-process provider 只在啟動與輪詢時取設定，評估本身不連網。所以量到的是評估成本，不是 relay 的網路成本。",
	"relay 只提供 otel-nats-tracing，不提供 master key，與 demo ConfigMap 一致；master 每次評估都是 FLAG_NOT_FOUND 後回退本地值，" +
		"這條路徑一樣要走完整個 SDK 評估流程，成本照算。",
	"no_flag_no_env 的單次成本與計時器本身同量級，clock_granularity_ns 與 timing_overhead_ns 已一併記錄，" +
		"median_minus_timing_overhead_ns 是扣掉計時開銷後的值。",
	"parallel 與 roundtrip 的 goroutine 數是 4×GOMAXPROCS，超過核心數，因此併發數字同時包含排程延遲與鎖競爭，兩者未分離。" +
		"這是刻意的（真實的請求處理池也超訂），但不要把 parallel 的單次延遲當成單執行緒延遲。",
	"roundtrip 的單次延遲包含在飛行中的排隊時間：回覆是由同一批核心上的 subscriber goroutine 產生的。" +
		"要看閘門本身請用 publish 的差額，roundtrip 是用來回答「閘門佔一次真實業務往返的多少」。",
	"每次往返解析四次閘門（request publish、request handler、reply publish、reply handler），" +
		"以每次兩個 OpenFeature 評估計，一次 demo 請求共 8 次評估。",
	"roundtrip 請以 ops/s 解讀，不要用單次延遲：在飛行中的請求數固定時，延遲會隨吞吐反向放大" +
		"（Little's law），所以延遲差額看起來遠大於閘門本身的成本。",
	"「要送 trace 時加閘門的增量」會略高於「純閘門成本」，即使兩者都是同樣的兩次評估。" +
		"差異來自 GC：已經在高配置速率下的模式，每次操作要分攤的 GC 工作也比較多。" +
		"allocs/op 的差額在兩種算法下相同，可以據此對照。",
}

const isolationZH = "每個 mode 一個子行程。原因：otel-flags 的 installDone / autoInstalled / explicitBind 是 process 生命週期的 latch，" +
	"且 OpenFeature SDK 沒有解除 domain 綁定的方法。舊版在同一個行程內依序量四個 mode，" +
	"後面的 mode 會沿用前面留下的安裝狀態，量到的並非它宣稱的設定。"

// spawnWorker runs one measurement in a fresh process and parses its result.
func spawnWorker(t *testing.T, modeID, kind string, ops, workers int) (RunResult, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestFlagPerfWorker$", "-test.v", "-test.timeout=30m")
	cmd.Env = append(os.Environ(),
		envWorker+"=1",
		envModeID+"="+modeID,
		envKind+"="+kind,
		envOps+"="+strconv.Itoa(ops),
		envWorkers+"="+strconv.Itoa(workers),
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return RunResult{}, fmt.Errorf("worker failed: %w\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, resultPrefix) {
			continue
		}
		var res RunResult
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, resultPrefix)), &res); err != nil {
			return RunResult{}, fmt.Errorf("decode worker result: %w", err)
		}
		return res, nil
	}
	return RunResult{}, fmt.Errorf("worker produced no result line\nstdout:\n%s", out)
}

func aggregate(runs []RunResult, modes []Mode) []Aggregate {
	evals := map[string]int{}
	for _, m := range modes {
		evals[m.ID] = m.EvalsPerPublish
	}

	type key struct{ mode, kind string }
	grouped := map[key][]RunResult{}
	var order []key
	for _, r := range runs {
		k := key{r.ModeID, r.Kind}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], r)
	}

	out := make([]Aggregate, 0, len(order))
	for _, k := range order {
		group := grouped[k]
		medians := make([]float64, 0, len(group))
		trimmed := make([]float64, 0, len(group))
		avgs := make([]float64, 0, len(group))
		p95s := make([]float64, 0, len(group))
		p99s := make([]float64, 0, len(group))
		rates := make([]float64, 0, len(group))
		mallocs := make([]float64, 0, len(group))
		bytesPer := make([]float64, 0, len(group))
		var errs, timeouts int
		for _, r := range group {
			medians = append(medians, r.Stats.MedianNS)
			trimmed = append(trimmed, r.Stats.TrimmedAvgNS)
			avgs = append(avgs, r.Stats.AvgNS)
			p95s = append(p95s, r.Stats.P95NS)
			p99s = append(p99s, r.Stats.P99NS)
			rates = append(rates, r.Stats.OpsPerSec)
			mallocs = append(mallocs, r.Stats.MallocsPerOp)
			bytesPer = append(bytesPer, r.Stats.BytesPerOp)
			errs += r.Stats.Errors
			timeouts += r.Stats.Timeouts
		}
		sort.Float64s(medians)
		med := percentile(medians, 0.5)
		lo, hi := medians[0], medians[len(medians)-1]
		spread := 0.0
		if med > 0 {
			spread = 100 * (hi - lo) / med
		}
		evalCount := evals[k.mode]
		if k.kind == kindRoundTr && evalCount > 0 {
			// One round trip resolves the gate four times: request publish,
			// request handler, reply publish, reply handler.
			evalCount *= 4
		}
		a := Aggregate{
			ModeID:          k.mode,
			Kind:            k.kind,
			Repeats:         len(group),
			Workers:         group[0].Workers,
			MedianNS:        med,
			MedianUS:        med / 1e3,
			TrimmedAvgNS:    medianOf(trimmed),
			AvgNS:           medianOf(avgs),
			P95NS:           medianOf(p95s),
			P99NS:           medianOf(p99s),
			MinRunMedianNS:  lo,
			MaxRunMedianNS:  hi,
			RunSpreadPct:    spread,
			OpsPerSec:       medianOf(rates),
			MallocsPerOp:    medianOf(mallocs),
			BytesPerOp:      medianOf(bytesPer),
			TracingEnabled:  group[0].TracingEnabled,
			Errors:          errs,
			Timeouts:        timeouts,
			EvalsPerOp:      evalCount,
			ClockGranNS:     medianOf(collect(group, func(r RunResult) float64 { return r.ClockGranularityNS })),
			TimingOverhdNS:  medianOf(collect(group, func(r RunResult) float64 { return r.TimingOverheadNS })),
			MedianMinusOvhd: 0,
		}
		a.MedianMinusOvhd = a.MedianNS - a.TimingOverhdNS
		out = append(out, a)
	}
	return out
}

func collect(runs []RunResult, f func(RunResult) float64) []float64 {
	out := make([]float64, 0, len(runs))
	for _, r := range runs {
		out = append(out, f(r))
	}
	return out
}

func medianOf(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := make([]float64, len(vs))
	copy(s, vs)
	sort.Float64s(s)
	return percentile(s, 0.5)
}

// compare builds the statements the report is allowed to make, each from two
// aggregates that exist. A comparison whose modes were not measured is simply
// absent rather than zero.
func compare(aggs []Aggregate) []Comparison {
	index := map[string]Aggregate{}
	for _, a := range aggs {
		index[a.Kind+"/"+a.ModeID] = a
	}

	specs := []struct {
		kind, label, left, right, why string
	}{
		{kindPublish, "純閘門成本（部署姿勢）", "flag_off_relay", "no_flag_no_env",
			"relay 自動安裝、flag 關閉、不發 span：差額就是每次 Publish 兩次 OpenFeature 評估的成本。"},
		{kindPublish, "純閘門成本（測試姿勢）", "flag_off_memprovider", "no_flag_no_env",
			"同上，但 provider 由測試直接綁：多付 providerBound 慢路徑與每次評估一個 timeout context。"},
		{kindPublish, "測試姿勢 vs 部署姿勢", "flag_off_memprovider", "flag_off_relay",
			"舊版報告量的是左邊，demo 部署跑的是右邊。差額為負代表舊數字「低估」了部署：" +
				"測試姿勢省下的（3 次 registry 讀取 + 每次評估一個 timeout context）" +
				"少於 GOFF in-process rule 評估多付的（context 轉換與 variation 查找）。方向由量測決定，不預設。"},
		{kindPublish, "span 成本（無閘門）", "env_on_no_flag", "no_flag_no_env",
			"只開 env、不走 OpenFeature：差額是建立 span、組 attribute 與注入 W3C header 的成本。"},
		{kindPublish, "要送 trace 時加閘門的增量", "flag_on_relay", "env_on_no_flag",
			"兩邊都發 span，差別只有每次操作的兩次 OpenFeature 評估。這是決策時該看的數字。"},
		{kindPublish, "部署姿勢 vs 零閘門", "flag_on_relay", "no_flag_no_env",
			"完整成本對照下限基準。分母極小，倍數僅供參考，應以差額為準。"},
		{kindParallel, "併發下的純閘門成本", "flag_off_relay", "no_flag_no_env",
			"4×GOMAXPROCS 條 goroutine 共用一個 Conn：檢查閘門成本在競爭下是否還是常數。"},
		{kindParallel, "併發下加閘門的增量", "flag_on_relay", "env_on_no_flag",
			"併發版的關鍵比值，與 publish 版對照即可看出鎖競爭放大了多少。"},
		{kindRoundTr, "往返：加閘門的增量", "flag_on_relay", "env_on_no_flag",
			"一次往返解析四次閘門（共 8 次 OpenFeature 評估），但總時間由 NATS 傳遞主導。"},
		{kindRoundTr, "往返：閘門佔比對照", "flag_on_relay", "no_flag_no_env",
			"把閘門成本放回真實請求的時間軸上看，才知道它佔一次業務往返的多少。"},
	}

	out := make([]Comparison, 0, len(specs))
	for _, s := range specs {
		l, okL := index[s.kind+"/"+s.left]
		r, okR := index[s.kind+"/"+s.right]
		if !okL || !okR {
			continue
		}
		c := Comparison{
			Kind:        s.kind,
			Label:       s.label,
			LeftMode:    s.left,
			RightMode:   s.right,
			LeftUS:      l.MedianUS,
			RightUS:     r.MedianUS,
			DeltaUS:     l.MedianUS - r.MedianUS,
			Explanation: s.why,
		}
		if r.MedianUS > 0 {
			c.Ratio = l.MedianUS / r.MedianUS
		}
		out = append(out, c)
	}
	return out
}

func writeEvidence(t *testing.T, rep Report) {
	t.Helper()
	path := evidencePath("gate-latency.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir evidence: %v", err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s (%d runs, %d aggregates)", path, len(rep.Runs), len(rep.Aggregates))
}

func evidencePath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "docs", "evidence", "perf", name)
}

func intFromEnv(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func skipKinds() map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(os.Getenv(envSkipKinds), ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}
