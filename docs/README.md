# docs

## otel-nats 功能旗標矩陣報告（繁中）

- **報告 HTML**：[`otel-nats-feature-flag-matrix.zh-TW.html`](./otel-nats-feature-flag-matrix.zh-TW.html)
- **原始證據**：[`evidence/`](./evidence/)
  - `matrix-unit.json` — 22 組單元閘門/回退組合（Go 測試）
  - `live-summary.json` + `live-step-c*` — 叢集 option C + ConfigMap 翻轉（API + ClickHouse）
- **重跑**
  - 單元：`cd backend && go test ./internal/flagmatrix/ -v -count=1`
  - 重產 HTML：`python3 docs/scripts/render-flag-matrix-html.py`

每個案例在 HTML 內都有：**設定步驟 → 實際設定 → 預期 → 證據（API/測試輸出）→ 結果**。
