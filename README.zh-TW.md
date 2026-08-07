# instrumentation-demo

本專案是 **W3C trace 傳播** 與同組織 instrumentation 函式庫
（`instrumentation-go` / `instrumentation-js`）的端對端示範，在本機
`kind` 叢集中執行。

瀏覽器前端會開啟一條 trace，以 HTTP 呼叫 Go 後端；後端以 `otelnats` 對 NATS
做發布／訂閱；整條路徑經 `rotel` → ClickHouse 寫入，並在 Grafana 檢視（指標則走
VictoriaMetrics）。GOFF relay proxy 從 Kubernetes ConfigMap（`demo-feature-flags`）
提供 **函式庫** 功能開關（例如 `otel-nats-tracing`），用來證明維運人員可在執行中的
程序上依 `instrumentation-go` 的階梯（`relay > env > option > default`）**開啟或關閉**
instrumentation — 不需改應用程式碼、不需重啟。

**English:** [README.md](README.md)

---

## 這個 demo 要用來做什麼

用來在**接近真實的呼叫鏈**上檢查 instrumentation 函式庫是否正常，而不只是
單元測試綠燈：

| 函式庫／套件 | 在本 demo 中的角色 |
|---|---|
| **`otelnats`**（`instrumentation-go`） | NATS 發布／訂閱 span、訊息標頭上的 W3C 脈絡、非同步 **span link**、執行期旗標 `otel-nats-tracing` |
| **OpenFeature + GOFF provider**（**應用零程式碼**） | `otelnats` 依 `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` 自行安裝 provider，並在每次操作解析 `otel-nats-tracing` |
| **瀏覽器 OTel**（`@opentelemetry/sdk-trace-web` + fetch instrumentation） | CLIENT span，並在 `POST /api/demo-trace` 注入 `traceparent` |
| **`@akira-core/otel-nats`**（`instrumentation-js` submodule） | 已掛在 tree 內供 JS NATS 使用；本 UI demo 著重 HTTP→Go→NATS 路徑 |

點一次按鈕後，Grafana 裡能看到正確的 span 圖，且切換旗標能即時停止或恢復函式庫的
instrumentation、同時業務路徑照常運作，就代表接線方式符合我們建議應用程式
採用的模式。

---

## 架構

### 元件總覽

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  kind 叢集  (namespace: demo)                                               │
│                                                                             │
│  瀏覽器 ──port-forward──► Frontend (nginx + 靜態 JS)                        │
│       │                         │                                           │
│       │  POST /api/demo-trace   │  OTLP/HTTP（瀏覽器 span）                 │
│       │  + traceparent          ▼                                           │
│       └──────────────────► Backend (Go)               Relay proxy (GOFF)    │
│                                  │  ▲                 ▲                     │
│                                  │  └─otelnats 輪詢───┘ ConfigMap API       │
│                                  │    （零應用碼）    │ demo-feature-flags  │
│                                  ▼                    │ （configmap retriever）│
│                               NATS                    │                     │
│                                  │                                          │
│  Backend + Frontend + Relay ──► rotel ──► ClickHouse ◄── Grafana           │
│  (OTLP)                         │                                           │
│                                  └──► VictoriaMetrics ◄── Grafana（指標）   │
│                                       （亦 scrape NATS/CH/Grafana/relay）   │
└─────────────────────────────────────────────────────────────────────────────┘
```

| 元件 | 來源 | 職責 |
|---|---|---|
| **frontend** | `frontend/` | 建立 CLIENT span、注入 W3C 標頭、顯示結果與 Grafana 連結 |
| **backend** | `backend/` | SERVER span、以 `otelnats` 做 NATS 請求／回覆（本身沒有任何功能開關程式碼） |
| **NATS** | vendored chart | demo 訊息匯流排 |
| **relay-proxy** | GOFF chart（官方、未改） | 從 ConfigMap `demo-feature-flags`（configmap retriever）提供 `otel-nats-tracing` |
| **rotel** | ClickHouse chart（子圖） | OTLP collector → ClickHouse（管線指標 → VM） |
| **ClickHouse** | vendored chart（Altinity operator） | trace 儲存（CHI + Keeper） |
| **Grafana** | vendored chart | trace 與 metrics 儀表板 |
| **VictoriaMetrics** | vendored chart | 指標儲存（OTLP push + Prometheus scrape） |

### 請求流程（點一次 **Start Trace**）

1. **Frontend** — WebTracerProvider + Fetch instrumentation 建立 CLIENT span，
   並在 `POST /api/demo-trace` 注入 `traceparent` / `tracestate`。
2. **Backend HTTP** — 抽出脈絡，建立 SERVER span（`POST /api/demo-trace`）。
   port-forward 與 CORS 傳播設定正確時，**trace ID 與瀏覽器相同**。
3. **NATS 發布** — 成功路徑一律執行；函式庫 tracing 開啟時為 `otelnats`
   PRODUCER span；W3C 脈絡寫入 NATS 訊息標頭。Subject：`demo.trace.request`。
4. **NATS 消費與回覆** — 同行程訂閱者（模擬下游消費者）以 `otelnats`
   在 `process demo.trace.request` 建立 CONSUMER span，再發布至
   `demo.trace.reply`。
5. **回應** — JSON `{ traceId, spanId }`，可把 ID 貼進 Grafana。

### 重要：NATS span 使用 span link（多個 trace ID）

一次完整執行會產生**不只一個 trace ID**，這是**刻意設計**。同步路徑
（frontend → backend → NATS publish）共用 UI 顯示的 ID；NATS
**consumer** span 則是獨立 root：`otelnats` 以 OTel **span link** 連到
publisher，而不是 parent — 符合非同步訊息的 OTel 語意指引。

Grafana 預建儀表板含 **span-linked async spans** 面板，不必硬湊成單一
waterfall 也能看完整故事。

### 遙測管線

```
各服務 (OTLP/HTTP 或 gRPC)
        │
        ▼
     rotel  ──批次──►  ClickHouse (otel_traces)  ──查詢──►  Grafana（traces）
        │
        └── OTLP metrics ──► VictoriaMetrics ──查詢──► Grafana（Ecosystem Metrics）
```

NATS、ClickHouse、Grafana、relay 的 Prometheus scrape 目標也會進
VictoriaMetrics。

### 功能開關（僅函式庫）

| 旗標 | 由誰讀取 | 設為 `disabled` 時 |
|---|---|---|
| `otel-nats-tracing` | **`otelnats` 函式庫**（應用不需改碼） | 不再產生 NATS producer/consumer span；round trip **仍會跑** |

**沒有**應用層旗標閘住 NATS，而且**後端完全沒有碰 OpenFeature**。設定
`OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` 就是全部的接線：`otelnats` 會在第一次
instrumented 操作時自行建立 GO Feature Flag provider，綁到它自己的 OpenFeature
domain（`otel-instrumentation-go`），並寫死 in-process 評估與停用 data collector。

每個開關沿著四階梯解析，**最先表態的那一層贏**：

```
relay  >  env  >  option (With*Enabled)  >  寫死的預設值
```

relay **兩個方向都有權威**。本 demo 採 **選項 C**：

| 旋鈕 | 部署值 | 角色 |
|---|---|---|
| `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `1` | 總開關否決權保持開 |
| `OTEL_NATS_TRACING_ENABLED` | `false` | 模組 env 明確關閉 |
| ConfigMap `otel-nats-tracing` | 預設 `enabled` | 由 relay **啟用** tracing |

開箱即有 NATS span，是因為 relay 覆寫了 falsy 的模組 env。把 ConfigMap 改成
`disabled` 可停掉 span；改回 `enabled` 再恢復 — 無需重新部署。relay 連不上時，
由 falsy env 決定，不會產生 NATS span（業務路徑仍可運作）。

demo 連線**不要**呼叫 `otelnats.WithTracingEnabled(...)`，讓 option 那一層保持
沉默，結果只由 relay 與 env 解釋。（函式庫裡 option 仍是合法的第三階；本 demo
刻意不用。）

---

## 目錄結構

- `frontend/` — 瀏覽器應用；開啟 trace 並呼叫後端。
- `backend/` — Go HTTP 服務；延續 trace、NATS 發布／訂閱。沒有任何功能開關程式碼：`otelnats` 依環境變數自行接到 relay。
- `deploy/` — `kind` 叢集設定、in-house 服務的 Kustomize base、vendored chart 的 Helm values。
- `charts/` — 第三方 Helm charts（NATS、Altinity operator 傘狀 ClickHouse、Grafana、VictoriaMetrics、GO Feature Flag relay proxy）。各子目錄有 `SOURCE.txt` 記錄拉取版本。
- `deploy/loadgen/` — 按需負載 Job，不在預設 `deploy` 路徑內。
- `third_party/` — 兄弟 instrumentation 倉庫的 git submodule
  （`instrumentation-js`、`instrumentation-go`）。
- `openspec/` — 本倉庫變更的規劃文件。

---

## 取得程式碼

跨倉依賴以 git submodule 管理（`instrumentation-js` 的 `@akira-core/otel-nats`、
`instrumentation-go` 的 `otelnats` / `oteljetstream`）。請連同 submodule 一併
clone：

```sh
git clone --recurse-submodules <this-repo-url>
```

若先前 clone 時未加 `--recurse-submodules`，或要拉到 submodule 追蹤分支的最新
commit：

```sh
make bootstrap
```

各 submodule 追蹤的是**分支**（非永久釘死某 commit）— 見 `.gitmodules`。
`instrumentation-go` 追蹤 `main`；`instrumentation-js` 追蹤 `feat/otel-nats`
（`@akira-core/otel-nats` 尚未進 `main`）。推進到追蹤分支 tip 是**刻意、
明確**的步驟，clone / pull 不會自動發生：

```sh
git submodule update --remote third_party/instrumentation-go
git submodule update --remote third_party/instrumentation-js
```

若要改追蹤另一個分支：

```sh
git submodule set-branch --branch <branch> third_party/<repo>
git submodule update --remote third_party/<repo>
```

---

## 執行 demo

本機需要 `docker`、`kind`、`helm`、`kubectl`。

```sh
make deploy       # 沿用目前 kubectl context（或自動選 kind 叢集）、
                   # 建置並載入映像、安裝／升級 charts、套用服務
make port-forward  # 用目前 kubectl context；等到 Ready 後一次開好本機 port（Ctrl-C 結束）
# 別名：make pf
```

`make deploy` / `make port-forward` **不寫死** kind 叢集名稱：預設用目前的
kubectl context（例如 Docker Desktop 或你已選好的 kind context）。若要指定：

```sh
make deploy CLUSTER_NAME=my-kind        # 建立或重用名為 my-kind 的 kind 叢集
make port-forward CLUSTER_NAME=my-kind  # 切到 kind-my-kind 再轉發
make teardown CLUSTER_NAME=my-kind      # 只刪該 kind 叢集
```

未指定時：若 context 已是 `kind-*` 則用該名稱；若本機只有一個 kind 叢集則用它；
否則新建預設名 `demo-trace`。若目前 context 已可連線且不是 kind（例如 Docker
Desktop），則**跳過** kind create/load，直接對該叢集 helm/apply。

`make deploy` 可安全重跑：既有叢集 **只重用、不刪除重建**；Helm 原地升級、
manifest 再 apply。`make teardown` 只刪 kind 叢集，不會動 Docker Desktop。

### 存取方式

`make deploy` 完成後，用一個指令開 port-forward（本 demo 不裝 Ingress）：

```sh
make port-forward
```

會先等核心 Deployment Ready，再轉發：

| 本機 URL | 服務 | 說明 |
|---|---|---|
| http://localhost:8081 | frontend | 開此頁，點 **Start Trace** |
| http://localhost:8080 | backend | 瀏覽器會直接打這支 |
| http://localhost:3000 | grafana | `admin` / `demo-grafana-admin` |
| http://localhost:4318 | rotel | 瀏覽器 OTLP（frontend span） |

保持該終端機執行；Ctrl-C 會一併關掉所有 forward。若 port 衝突可覆寫：

```sh
make port-forward PF_FRONTEND_PORT=9081 PF_GRAFANA_PORT=3001
```

---

## 如何驗證 instrumentation 是否正常

完成 `make deploy` 與 port-forward 後，依下列清單操作。可視為本 demo 所依賴
函式庫的**手動驗收測試**。

### 1. 正常路徑 — 點一次

1. 開啟 `http://localhost:8081`。
2. 點 **Start Trace**。
3. UI 應出現：`traceId` / `spanId`（十六進位）。
4. 開啟 Grafana（`http://localhost:3000`，`admin` / `demo-grafana-admin`）。
5. 打開預建 **trace** 儀表板（或 Explore → ClickHouse traces）。
6. 貼上 UI 的 `traceId`。

**通過條件（同步路徑，同一 trace ID）：**

| 應看到的內容 | 證明什麼 |
|---|---|
| Frontend CLIENT / fetch span | 瀏覽器 SDK + fetch instrumentation |
| Backend `POST /api/demo-trace` SERVER span，**同一 trace ID** | HTTP 邊界的 W3C extract/inject |
| 該 trace 下的 NATS **publish** / PRODUCER 類 span | `otelnats` 發布端 instrumentation |

**通過條件（非同步 NATS，以 link 串起的其他 trace）：**

| 應看到的內容 | 證明什麼 |
|---|---|
| `process demo.trace.request`（與 reply 路徑）在**其他** trace ID 下 | Consumer 使用 span link，而非 parent-child |
| Consumer → producer 的 span link | NATS 訊息標頭上的傳播成功 |
| 儀表板「Span-linked async spans」看得到那些 consumer | 不必強迫單一 waterfall 也能看完整路徑 |

若 UI 成功但 Grafana **沒有**與前端 `traceId` 相同的 backend SERVER span，代表
傳播或匯出有問題（檢查 port-forward、CORS `traceparent`、rotel → ClickHouse）。

### 2. 函式庫旗標 — 即時開啟／關閉 NATS instrumentation

這是本 demo **唯一**需要的旗標驗證。預設部署已有 span（relay 在 env 關閉時啟用）。
要關閉：

```sh
kubectl edit configmap demo-feature-flags -n demo
# 將 otel-nats-tracing 的 defaultRule.variation 改為: disabled
```

等**幾秒**，再點一次 **Start Trace**。要走兩段：relay 在
`pollingInterval: 1000` 內經 API 重讀 ConfigMap，後端 provider 每
`OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL: 2s` 輪詢一次 relay。

| 預期 | 意義 |
|---|---|
| UI 仍回傳 `traceId` / `spanId`（成功） | NATS 業務路徑仍有執行 |
| Grafana **沒有**新的 NATS producer/consumer span | `otelnats` 在無需改 app 的情況下遵守了 relay 關閉 |

改回 `enabled`，下一擊應再出現 span — 這正是 relay **啟用**
`OTEL_NATS_TRACING_ENABLED=false` 關掉的東西（由
`TestRelayEnablesWhatEnvLeftOff` / `TestRelayFlagTogglesNatsInstrumentation` 釘住）。

正式環境的預設輪詢間隔是 **60 秒**，不是 2 秒。事故應變請以輪詢間隔規劃，
而不是以「立刻」規劃。

### 3. 指標快速檢查（可選）

```sh
kubectl port-forward -n demo svc/victoria-metrics 8428:8428
curl -s 'http://localhost:8428/api/v1/query?query=up'
```

scrape 目標應回報 `1`。Grafana 的 **Ecosystem Metrics** 儀表板應能看到 rotel
ingest 與相依服務健康狀態。

### 4. 「函式庫壞了」常見長相

| 現象 | 可能原因 |
|---|---|
| 請求成功但沒有 NATS span，且 `otel-nats-tracing` 為 enabled | 未用 `otelnats` 包住 publish/subscribe、relay 尚未可達，或總開關關閉 |
| 後端連線失敗／重試出現設定錯誤 | 無效的 `OTEL_*_ENABLED` 值（階梯模型下空字串是錯誤），或 NATS 連線問題 |
| 切換 `otel-nats-tracing` 沒有效果 | `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` 未設定、relay RBAC 讀不到 `demo-feature-flags`，或兩段輪詢尚未完成（給它幾秒） |
| Frontend 與 backend trace ID 不同 | 缺少 `traceparent`（CORS / fetch instrumentation / 後端 URL 錯誤） |
| Grafana 完全看不到 span | OTLP 匯出路徑（rotel、port-forward `4318`、ClickHouse） |

---

## 功能開關（參考）

旗標定義在 `deploy/base/feature-flags.yaml`（ConfigMap `demo-feature-flags`）。
vendored chart 是**官方** GOFF 發行版、沒有本地 patch — 見
`charts/relay-proxy/SOURCE.txt`。relay 透過 GOFF 的 `kind: configmap` retriever
讀取；`deploy/values/relay-proxy.yaml` 的 `extraManifests` 提供讀取用的
Role/RoleBinding。

可現場編輯 ConfigMap — relay 約一秒內重讀，**無需重啟**：

```sh
kubectl edit configmap demo-feature-flags -n demo
```

現場編輯會維持到下一次 `kubectl apply -k deploy/base`，屆時 ConfigMap 會依
`deploy/base/feature-flags.yaml` 還原。要永久改動請改該檔。`make deploy` 先跑
helm 再跑 kustomize；`startWithRetrieverError: true` 會在 ConfigMap 尚未出現時
讓 relay 先起來。

由 `otelnats` 自己讀取的環境變數（demo 預設）：

| 變數 | 設定值 | 用途 |
|---|---|---|
| `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `1` | 整個程序的總開關否決權（預設 true；只有 `false` 有效果） |
| `OTEL_NATS_TRACING_ENABLED` | `false` | 模組 env 階 — 關閉，讓 happy path 走 relay 啟用 |
| `OTEL_INSTRUMENTATION_GO_FLAGS_ENDPOINT` | `http://relay-proxy:1031` | 未設定 ⇒ 不安裝 provider，只走本地階梯 |
| `OTEL_INSTRUMENTATION_GO_FLAGS_POLL_INTERVAL` | `2s` | Go duration 字串。函式庫預設 `60s` |
| `OTEL_SERVICE_NAME` | `demo-backend` | 同時作為每次評估的 `service.name` targeting 屬性 |

因為 `OTEL_SERVICE_NAME` 會被當成 targeting 屬性送出，relay 規則可以只針對單一
服務：

```yaml
otel-nats-tracing:
  variations: { enabled: true, disabled: false }
  targeting:
    - query: service.name eq "demo-backend"
      variation: disabled
  defaultRule:
    variation: enabled
```

若 relay 無法連線，評估會落到 env／option／default。在本 demo falsy 的模組 env
下，代表 **沒有 NATS span**，直到 relay 恢復並提供 `enabled`。

---

## 指標

VictoriaMetrics（單節點）以兩種方式收集整疊指標：

- **OTLP push** — 後端應用／runtime 指標與 rotel 管線內部遙測，由 rotel 轉送。
- **Prometheus scraping** — NATS、ClickHouse、Grafana、relay proxy 的 `/metrics`。

Grafana 除 trace 儀表板外，另有預建 **Ecosystem Metrics** 儀表板；面板查詢
皆對過實際資料。

```sh
kubectl port-forward -n demo svc/victoria-metrics 8428:8428   # http://localhost:8428
curl -s 'http://localhost:8428/api/v1/query?query=up'         # scrape 目標應皆為 1
```

---

## 效能測試

`otel-loadgen`（rotel 作者提供）對 rotel 的 gRPC 端點送合成 OTLP **trace**
負載。**不會**隨 `make deploy` 啟動 — 需明確執行，結束後自行停止：

```sh
make load-test                                              # 預設：2 workers、60s
make load-test WORKERS=10 SPANS_PER_RESOURCE=500 DURATION=2m
make load-test-logs
make load-test-clean
```

可在 **Ecosystem Metrics** 儀表板觀察影響 — rotel 面板依協定拆分 ingest
（`grpc` 為負載產生器，`http` 為 demo 自身服務）。

在單節點 `kind` 上量測：2 workers、45 秒約送出 180,000 spans 端對端
（約 4,000 spans/sec）、零遺失、無 pod 重啟。此類數字僅供本機相對比較，
非正式 benchmark。

---

## 拆除

```sh
make teardown     # 刪除 kind 叢集及其中所有資源
```
