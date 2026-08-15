# otel-nats 跨 runtime 等價性：Go vs JS

`otel-nats`（Go）與 `@akira-core/otel-nats`（JS）對同一個 messaging 操作是不是
**做同一件事**——以及這個宣稱有哪幾部分是本 demo 端到端驗證過的，哪幾部分靠的是
兩邊各自的測試套件。

**Languages:** [English (parity.md)](parity.md)

稽核當下的 submodule 狀態：

| Submodule | Commit | 版本 |
|---|---|---|
| `third_party/instrumentation-go` | tag `otel-nats/v0.9.1` | `otelnats` `0.9.1`（`otelnats/conn.go:21`） |
| `third_party/instrumentation-js` | `feat/otel-nats-consumer-span-links`（[PR #3](https://github.com/akira-core/instrumentation-js/pull/3)） | `@akira-core/otel-nats` `0.3.0`、`@akira-core/otel-flags` `0.1.0` |

JS `0.2.0` 把 span 名稱與 inbox 屬性對齊 Go `0.9.0`/`0.9.1`；JS `0.3.0` 補上了
最後一個行為缺口（consumer span 拓樸）。本文件是對完整宣稱的查核。

---

## 結論

比對 21 個面向，**19 個一致**——包括曾經不等價的 consumer span 拓樸（見下方
歷史）。仍不同的 2 個都是平台差異下的刻意選擇、有文件記載，不是漂移：

- **#6 — 旗標解析時機。** Go 每次操作同步解析；JS 對背景更新的 snapshot 解析。
  同一道梯子、同一個結論，只是「翻旗標到生效」的上界不同（3 秒 vs 5 秒）。
- **#21 — context 傳遞。** Go 顯式傳 `ctx`；JS 讀 ambient `AsyncLocalStorage`。
  刻意為之，JS README 已載明。

---

## 對照表

圖例 —— **demo**：由 `docs/scripts/capture-parity-evidence.sh`（`make parity`）
對實跑叢集端到端驗證。**suite**：由兩邊自己的測試涵蓋，demo 路徑碰不到。
**read**：由閱讀兩邊實作確立。

| # | 面向 | Go 0.9.1 | JS 0.3.0 | 等價 | 依據 |
|---|---|---|---|---|---|
| 1 | 旗標 key | `otel-nats-tracing` | `otel-nats-tracing` | 一致 | demo |
| 2 | 模組環境變數 | `OTEL_NATS_TRACING_ENABLED` | `OTEL_NATS_TRACING_ENABLED` | 一致 | demo |
| 3 | 主開關（veto）環境變數 | `OTEL_INSTRUMENTATION_GO_TRACING_ENABLED` | `OTEL_INSTRUMENTATION_JS_TRACING_ENABLED` | 形狀一致，按 runtime 分家是設計 | demo |
| 4 | 模組預設值 | `false` | `false` | 一致 | read |
| 5 | 梯子 | relay > env > option > default | 同 | 一致 | demo |
| 6 | 解析時機 | 每次操作、同步 | 背景更新的 snapshot | **不同**（3s vs 5s 上界） | demo |
| 7 | publish span 名稱 | `publish {subject}` | `publish {subject}` | 一致 | demo |
| 8 | request span 名稱 | `request {subject}` | `request {subject}` | 一致 | suite |
| 9 | inbox 目的地從 span 名稱移除 | 三種情況都是 | 三種情況都是 | 一致 | suite |
| 10 | wildcard 上的 `messaging.destination.template` | 有 | 有 | 一致 | suite |
| 11 | inbox 屬性（`temporary`/`anonymous`/`conversation_id`） | 有 | 有 | 一致 | suite |
| 12 | `_INBOX.>` 視為有界 | 是 | 是 | 一致 | suite |
| 13 | `InboxPrefixes()` / `inboxPrefixes()` | 有 | 有 | 一致 | read |
| 14 | Span kind | semconv messaging 對應 | 同一套對應 | 一致 | demo |
| 15 | Span 屬性集與寫入條件 | `conn.go:397-457` | `attributes.ts:49-101` | 一致 | demo |
| 16 | reply-receive span 帶 span link | 有 | 有 | 一致 | suite |
| 17 | 對 inbox 發出的 request 抑制後寫的 `conversation_id` | 有 | 有 | 一致 | suite |
| 18 | core subscribe：consumer span 的 parent | 新 trace root | 新 trace root（0.3.0+） | 一致 | demo |
| 19 | core subscribe：連回 producer 的 link | 有，恰一個 | 有，恰一個（0.3.0+） | 一致 | demo |
| 20 | JetStream consumer：parent 與 link | 新 root + link | 新 root + link（0.3.0+） | 一致 | suite |
| 21 | context 傳遞 | 顯式 `ctx` 參數 | ambient `AsyncLocalStorage` | 刻意不同 | read |

### 關於第 15 列

兩邊在相同條件下組出相同屬性集：`messaging.system`、
`messaging.destination.name`、`messaging.operation.type`、
`messaging.operation.name` 一律寫入；body 非空時寫 `messaging.message.body.size`；
`reply` 有值時寫 `messaging.message.conversation_id`；有 queue group 時寫
`messaging.consumer.group.name`；server address/port 最後附上。兩邊也都不把
`conversation_id` 寫進 JetStream span（`$JS.ACK…` 是協定管線，不是對話識別碼）。

### 關於第 18–20 列（歷史）

JS `0.3.0` 之前，`@akira-core/otel-nats` 把每個 consumer span 掛在從訊息標頭抽出
的 context 之下，於是 JS consumer 內嵌在 producer 的 trace 上，而 Go consumer 開
自己的 linked-root trace——一個真實、系統性的不等價，正是本 demo 的 parity 擷取
揭露的（其 `D01`/`D02` 兩列刻意寫成一般斷言，讓修正落地時不動 demo 就自己變綠，
事實也正是如此）。根因是 `instrumentation-js` 的規格用詞（「a context whose
active trace matches the publisher's trace」），實作忠實照做；OpenSpec change
`js-otel-nats-consumer-span-links`（2026-08-15 歸檔）把契約改寫為明確的
root-plus-link 拓樸，實作跟進，且兩邊現在都有測試釘住拓樸。

現在兩種 runtime 對每一條 consume 路徑都輸出：新 trace 的 root span、恰一個連回
producer 的 span link、handler／訊息 context 帶著 consumer span。同一則訊息被兩
種 runtime 消費，得到同一種 trace 形狀。

---

## 仍值得追蹤的殘餘差異

`request` 的 **reply-receive span** 兩邊都帶 span link，但細節不同：Go 掛在
request context 之下（responder 有傳播時掛在抽出的 reply context 之下），link
指向抽出的 responder context；JS 掛在 ambient context 之下，link 永遠指向
request span，且不抽 reply 自己的標頭。Go 自己的規格文字也已漂移（寫 CONSUMER，
兩邊實作都是 CLIENT）。留待獨立的 change，讓兩個 repo 對齊在同一份成文契約上——
已記錄於歸檔 change 的 `design.md`。

---

## demo 路徑涵蓋不到的部分

demo 在兩個 runtime 上跑的是 **core NATS 的 publish 與 push-subscribe**。沒有
request/reply RPC、沒有 wildcard 訂閱、沒有 inbox subject、也沒有 JetStream，
所以第 8–12、16、17、20 列標的是 **suite**：由
`otel-nats/otelnats/conn_test.go`、`oteljetstream` 的套件與
`packages/otel-nats/test/{unit,integration}/` 直接涵蓋。

兩邊套件都在稽核當下的 commit 上跑成綠燈：

```
$ (cd third_party/instrumentation-go/otel-nats && go test ./otelnats/... ./oteljetstream/...)
ok  github.com/akira-core/instrumentation-go/otel-nats/otelnats        1.154s
ok  github.com/akira-core/instrumentation-go/otel-nats/oteljetstream  10.966s

$ (cd third_party/instrumentation-js/packages/otel-nats && pnpm test)
Test Files  8 passed (8)      Tests  183 passed (183)     # unit
Test Files  5 passed (5)      Tests   29 passed  (29)     # integration
```

自 `0.3.0` 起 JS 套件也釘住 consumer span 拓樸
（`test/unit/consumer-topology.test.ts` 與 integration 套件的 linked-root
斷言），所以「兩邊各自綠燈」如今也涵蓋了以前只有本 demo 的跨 runtime 對照才
抓得到的那個面向。

---

## 重現

```sh
make deploy CLUSTER_NAME=parity      # 用當前 submodule 建 image
make parity CLUSTER_NAME=parity      # docs/scripts/capture-parity-evidence.sh
```

叢集必須由**當前** submodule 建出來——擷取報告的是執行中 image 編譯時綁的版本，
不是磁碟上 checkout 的版本。Docker Desktop 的 Kubernetes 看不到本機 build 的
image（它的 containerd 與 Docker daemon 是分開的 image store），所以上面用
`kind`：`make deploy` 會明確把 image 載進去。

產出 `docs/evidence/parity-summary.json`（機器可讀的斷言結果）、
`docs/evidence/parity-spans.txt`（擷取到的 span 表格），以及每個被比對 span 各
一份 `docs/evidence/parity-span-*.json`。十四條斷言——span 形狀（`P01`–`P12`）
與拓樸（`D01`/`D02`）——必須全數通過，否則腳本以非零狀態結束。
