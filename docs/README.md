# docs

## otel-nats 功能旗標矩陣報告（繁中）

- **報告 HTML**：[`otel-nats-feature-flag-matrix.zh-TW.html`](./otel-nats-feature-flag-matrix.zh-TW.html)
- **原始證據**：[`evidence/`](./evidence/)
  - `matrix-unit.json` — 29 組單元閘門/回退組合（Go 測試），分四類：
    - `local` (10) — env > option > default 三個本地階級
    - `relay` (11) — relay 階級，雙向權威；含「只裝 default slot 的 provider 不算 relay」
    - `dynamic` (3) — **連線建立之後**翻 relay，同一條連線不重連就要跟上
    - `invalid` (5) — 必須讓建構失敗的設定，含 `_ENDPOINT` 與 `_POLL_INTERVAL` 兩個 process 級變數
  - `live-summary.json` + `live-step-c*` — 叢集 option C + ConfigMap 翻轉（API + ClickHouse）
- **重跑**
  - 單元：`cd backend && go test ./internal/flagmatrix/ -v -count=1`
  - 叢集：`./docs/scripts/capture-live-evidence.sh`（需要能存取 `demo` namespace）
  - 重產 HTML：`python3 docs/scripts/render-flag-matrix-html.py`

每個案例在 HTML 內都有：**設定步驟 → 實際設定 → 預期 → 證據（API/測試輸出）→ 結果**。

### dynamic 這一類為什麼要存在

其餘案例都只在 `Connect` 之後讀一次 `conn.TracingEnabled()`。那種斷言無法區分
「每次操作重新解析」與「建構時就定死」——而每次操作重新解析正是 0.8.0 的賣點。
`D01`/`D02` 在連線活著的時候改綁 provider 再讀一次；`D03` 反過來證明
`RelayPossible` 是建構時解析的，所以事後才綁上的 provider 對既有連線無效
（這是文件寫明的順序要求）。

## otel-nats feature-flag 閘門延遲（效能）

- **報告 HTML**：[`otel-nats-gate-latency.zh-TW.html`](./otel-nats-gate-latency.zh-TW.html)
- **證據**：[`evidence/perf/gate-latency.json`](./evidence/perf/gate-latency.json)
- **重跑**
  ```bash
  # 全套（約 25 分鐘）
  cd backend && go test ./internal/flagperf/ -run TestGateLatencyEvidence -v -count=1 -timeout 60m
  python3 docs/scripts/render-gate-latency-html.py

  # 縮小規模的煙霧測試（縮小的 run 不會覆蓋 evidence，見下）
  FLAGPERF_TOTAL_OPS=5000 FLAGPERF_RT_OPS=2000 FLAGPERF_REPEATS=2 FLAGPERF_RT_REPEATS=1 \
    go test ./internal/flagperf/ -run TestGateLatencyEvidence -count=1

  # 完全跳過（例如跑 go test ./... 時）
  go test -short ./...

  # allocs/op 與 benchstat — 一個 mode 一個行程（見下）
  for m in no_flag_no_env env_on_no_flag flag_off_memprovider \
           flag_on_memprovider flag_off_relay flag_on_relay; do
    FLAGPERF_MODE=$m go test ./internal/flagperf/ -run '^$' \
      -bench 'BenchmarkPublish$' -benchmem -count=10 | tee "bench-$m.txt"
  done
  benchstat bench-no_flag_no_env.txt bench-flag_on_relay.txt
  ```

固定量級 **500,000 次操作**，三個面向（序列 Publish、併發 Publish、完整往返），
六種設定模式，每個模式各跑數個獨立子行程並取各 run 中位數的中位數。

### 三個必須知道的設計決定

**一、每個 mode 一個子行程。** `otel-flags` 的 provider 安裝在 process 內是不可逆的
latch（`installDone` / `autoInstalled` / `explicitBind`），而 OpenFeature SDK 沒有解除
domain 綁定的方法。在同一個行程裡依序量多個 mode，後面的 mode 會沿用前面留下的安裝狀態，
量到的不是它宣稱的設定。同樣的理由，`Benchmark*` 也是一個 mode 一個行程，用
`FLAGPERF_MODE` 選，不在測試內部迴圈跑完所有 mode。

**二、量兩種 provider 姿勢，因為它們走的不是同一條程式碼路徑。**

| 姿勢 | 怎麼裝 | 每次評估要付什麼 |
|---|---|---|
| `*_memprovider` | `openfeature.SetNamedProviderAndWait`（單元測試的做法） | `providerBound` 慢路徑（3 次 provider registry metadata 讀取）＋ 一個 250ms `context.WithTimeout` |
| `*_relay` | 設 `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT`，由 otel-flags 自行安裝 GOFF provider | 兩個 atomic load ＋ `context.Background()`，但 rule 評估是真的 GOFF |

`deploy/base/backend.yaml` 走的是**後者**。`*_relay` 模式連的是行程內的假 relay
（`relay_test.go` 起一個 `httptest` server 供應 `POST /v1/flag/configuration`），
所以量到的是評估成本，不是 relay 的網路成本 —— GOFF in-process provider 只在啟動與
輪詢時取設定，評估本身不連網。

**三、計時器本身也要量。** 零閘門基準的單次成本與 `time.Now` 呼叫、時鐘粒度同量級，
所以每個 run 都會探測 `clock_granularity_ns` 與 `timing_overhead_ns` 並記進證據，
聚合表另有 `median_minus_timing_overhead_ns`。

### 只保留單一量級

先前的版本掃 N ∈ {100, 1e3, 1e4, 1e5} 並拿最小的那組去算比值。100 筆樣本裡一個
148µs 的離群值就會把平均拉高 1.5µs，而零閘門基準本身只有幾個時鐘 tick 寬 ——
那幾列量到的是暖機與量化誤差，不是閘門。把它們和穩態列並排，還會讓人以為存在
一個隨量級上升的趨勢。現在只留一個實際服務會到達的量級。

主指標是**中位數**與**差額**，不是平均與倍數：對次微秒的基準取比值會得到很大但
沒有決策價值的數字。

### 縮小的 run 不會覆蓋 evidence

上面那些 `FLAGPERF_*` 旋鈕是給煙霧測試用的。只要 ops、repeats 任一低於完整設定，
或用了 `FLAGPERF_SKIP_KINDS`，driver 就只印結果而**不寫**
`docs/evidence/perf/gate-latency.json`。一個叫做 evidence、實際上來自 5,000 次操作
單一 repeat 的檔案，比沒有檔案更糟。
