# docs

## 量測環境

三份證據（單元矩陣、targeting、效能）與叢集實測**都在同一台機器上取得**，
叢集是同一台機器上的 kind：

| 項目 | 值 |
|---|---|
| OS / arch | linux/amd64 |
| CPU | 2 vCPU，Intel Xeon @ 2.20GHz |
| cgroup | `cpu.max=200000 100000`（= 2 顆核心） |
| RAM | 7.7 GiB |
| Go | go1.26.5 |
| 叢集 | `kind-demo-trace`（`kind create cluster --config deploy/kind-cluster.yaml`） |

先前效能數字取自一台筆電，而叢集證據來自另一個環境，兩半在講不同的硬體。
現在兩者一致，`gate-latency.json` 的 `env` 區塊與 `live-summary.json` 的 `cluster`
欄位都會記錄實際取得證據的機器，不需要靠 README 宣稱。

叢集實測只部署 live 證據需要的元件（namespace、feature-flags ConfigMap、
backend、NATS、ClickHouse + rotel、relay proxy）；Grafana、VictoriaMetrics 與
frontend 在這個規模的機器上與證據無關，因此略過。完整堆疊請用 `make deploy`。

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

## relay targeting by service name（正確性）

- **證據**：[`evidence/targeting-unit.json`](./evidence/targeting-unit.json)
- **重跑**：`cd backend && go test ./internal/flagtargeting/ -v -count=1`

回答一個上下游都沒測到的問題：**operator 在 relay 上寫一條依服務名選擇的規則，
到底選不選得到這個 process？**

| 層級 | 檔案 | 實際涵蓋 |
|---|---|---|
| 上游 UT | `otel-flags/flags_test.go` | 唯一出現 `OTEL_SERVICE_NAME` 的地方，斷言 `currentEvalCtx().Attributes()["serviceName"]` 帶了值 — 屬性有沒有**送出去** |
| 上游 e2e | `otel-nats/tests/integration/relayflags_test.go` | 真的 relay proxy container，但 flag fixture 只有 `defaultRule`，整份測試沒有任何 `targeting` |

上游 UT 證明屬性送得出去，e2e 證明 relay 連得到，**沒有人把兩者接起來**。

三個案例，一個 case 一個子行程（`OTEL_SERVICE_NAME` 是在 `installProviderFromEnv`
裡讀的，而安裝在 process 內是不可逆的 latch）：

| ID | relay 規則 | `OTEL_SERVICE_NAME` | 預期 |
|---|---|---|---|
| T01 | `serviceName eq "demo-backend"` | `demo-backend` | ON |
| T02 | `serviceName eq "demo-backend"` | `other-service` | OFF |
| T03 | `service.name eq "demo-backend"` | `demo-backend` | OFF |

T01/T02 只差服務名，T01/T03 只差規則寫法，兩軸各自都會改變結果 —— 所以不可能是碰巧全開或全關。

**T03 釘的是一個有文件記載的陷阱。** `otel-flags` 兩種拼法都送（`service.name` 與
`serviceName`），因為前者是 semconv 讀者預期看到的名字，而**只有後者能被 targeting 用**：
nikunjy 與 JSONLogic 都把點當成巢狀路徑分隔符，所以 `service.name eq "..."` 會去解一條路徑、
找不到東西，於是一個 process 都選不到 —— 安靜地、每個 process 都如此，看起來就只是「flag 沒作用」。
哪天有人把沒有點的拼法拿掉，會在 T03 爆掉，而不是在叢集裡。

規則由 `internal/fakerelay` 供應：行程內的假 relay 回應 GOFF provider 的
`POST /v1/flag/configuration`，所以查詢是**真的 GO Feature Flag rule 引擎**在解析
（陷阱就住在那個 parser 裡，in-memory stub 抓不到），但不需要 Docker。

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

### 哪些數字可以引用，哪些只能讀方向

**run 內很穩，campaign 之間會漂。** 同一份 evidence 裡每個 mode 跑 5 個獨立子行程，
彼此的離散多在 5% 以下；但整份重跑之間，中位數會漂到 22%。這台是 2 vCPU 的共用 VM，
機器狀態在 campaign 之間的變化大於某些效應本身。

因此分兩類看：

| 比較 | 差額 | 可引用性 |
|---|---|---|
| 加閘門 vs 零閘門 | ~+10 到 +20 µs | 遠大於漂移，可引用 |
| span 成本（無閘門） | ~+5 µs | 可引用 |
| 往返吞吐（ops/s） | 降到 27% | 可引用 |
| **memprovider vs relay 姿勢** | +0.3 到 +4.8 µs | **只能讀方向，不要引用微秒數** |

最後一列是唯一一個效應與漂移同量級的比較。四次獨立 campaign：
+3.76、+0.26、+4.75 µs，加上 benchstat 的 +53.5%（p=0.000，n=10 —— 但那個 p 值只在單次
benchstat 內有效，不涵蓋 campaign 之間的漂移）。方向一致（relay 較貴），數值不可靠。

**判斷穩定性請看 allocs/op。** 它不隨主機負載變動，四次 campaign 都到小數點後兩位一致，
且與 `go test -benchmem` 完全吻合（[`evidence/perf/benchstat.txt`](./evidence/perf/benchstat.txt)）。
姿勢差異在配置次數上是硬的：**每次 Publish 固定多 10 次配置**（每次評估多 5 次），
四次 campaign 皆為 +10.01。所以「部署姿勢比舊版量的那個姿勢貴」這個結論站在配置次數上，
不站在微秒上。

### 縮小的 run 不會覆蓋 evidence

上面那些 `FLAGPERF_*` 旋鈕是給煙霧測試用的。只要 ops、repeats 任一低於完整設定，
或用了 `FLAGPERF_SKIP_KINDS`，driver 就只印結果而**不寫**
`docs/evidence/perf/gate-latency.json`。一個叫做 evidence、實際上來自 5,000 次操作
單一 repeat 的檔案，比沒有檔案更糟。
