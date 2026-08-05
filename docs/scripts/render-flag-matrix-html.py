#!/usr/bin/env python3
"""Render docs/otel-nats-feature-flag-matrix.zh-TW.html from evidence JSON."""
from __future__ import annotations

import html
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
EVID = ROOT / "docs" / "evidence"
OUT = ROOT / "docs" / "otel-nats-feature-flag-matrix.zh-TW.html"


def esc(s) -> str:
    return html.escape("" if s is None else str(s))


def badge(ok: bool) -> str:
    if ok:
        return '<span class="badge ok">通過</span>'
    return '<span class="badge fail">失敗</span>'


def pre(text: str) -> str:
    return f'<pre class="code">{esc(text)}</pre>'


def main() -> None:
    unit = json.loads((EVID / "matrix-unit.json").read_text(encoding="utf-8"))
    live = json.loads((EVID / "live-summary.json").read_text(encoding="utf-8"))
    baseline_env = (EVID / "live-baseline-env.txt").read_text(encoding="utf-8").strip()

    unit_sections: list[str] = []
    for r in unit["results"]:
        c = r["case"]
        steps = "\n".join(f"{i+1}. {s}" for i, s in enumerate(c["config_steps"]))
        cfg_lines = []
        if c.get("master_env") is not None:
            cfg_lines.append(f"OTEL_INSTRUMENTATION_GO_TRACING_ENABLED={c['master_env']!r}")
        else:
            cfg_lines.append("OTEL_INSTRUMENTATION_GO_TRACING_ENABLED=(unset)")
        if c.get("module_env") is not None:
            cfg_lines.append(f"OTEL_NATS_TRACING_ENABLED={c['module_env']!r}")
        else:
            cfg_lines.append("OTEL_NATS_TRACING_ENABLED=(unset)")
        if c.get("option") is not None:
            cfg_lines.append(f"WithTracingEnabled({c['option']})")
        else:
            cfg_lines.append("WithTracingEnabled=(none)")
        cfg_lines.append(f"relay_mode={c['relay_mode']}")
        if c.get("relay_master") is not None:
            cfg_lines.append(f"relay otel-instrumentation-go-tracing={c['relay_master']}")
        if c.get("relay_module") is not None:
            cfg_lines.append(f"relay otel-nats-tracing={c['relay_module']}")

        if c.get("expect_error"):
            expect = "建構錯誤 (ErrInvalidFlagValue)"
        elif c.get("expect_on"):
            expect = "tracing ON (conn.TracingEnabled() == true)"
        else:
            expect = "tracing OFF (conn.TracingEnabled() == false)"

        if r.get("got_error"):
            got = f"error: {r['got_error']}"
        elif r.get("got_on") is not None:
            got = f"TracingEnabled={r['got_on']}"
        else:
            got = "(n/a)"

        result_text = f"got: {got}\npass: {r['pass']}"
        evid_text = "\n".join(r.get("evidence") or [])

        unit_sections.append(
            f"""
    <article class="case" id="unit-{esc(c['id'])}">
      <header>
        <h3><code>{esc(c['id'])}</code> {esc(c['title_zh'])} {badge(r['pass'])}</h3>
        <p class="meta">類別：{esc(c['category'])} · 耗時 {esc(r.get('duration_ms', 0))} ms · {esc(r.get('ran_at', ''))}</p>
      </header>
      <h4>① 設定步驟</h4>
      {pre(steps)}
      <h4>② 實際套用設定</h4>
      {pre(chr(10).join(cfg_lines))}
      <h4>③ 預期</h4>
      {pre(expect)}
      <h4>④ 證據（測試執行紀錄）</h4>
      {pre(evid_text)}
      <h4>⑤ 結果</h4>
      {pre(result_text)}
    </article>
"""
        )

    live_sections: list[str] = []
    for c in live["cases"]:
        steps = """1. backend Deployment 維持 option C 環境變數（見「叢集基準設定」）
2. kubectl apply ConfigMap demo-feature-flags（本案例的 defaultRule.variation）
3. 等待約 75 秒（kubelet ConfigMap mount 刷新 + GOFF poll 1s + provider poll 2s）
4. POST GOFF /v1/feature/otel-nats-tracing/eval 確認 relay 值
5. POST /api/demo-trace 取得 traceId
6. ClickHouse 查詢 otel.otel_traces WHERE TraceId = <traceId>"""
        live_sections.append(
            f"""
    <article class="case" id="live-{esc(c['id'])}">
      <header>
        <h3><code>{esc(c['id'])}</code> {esc(c['title'])} {badge(c['pass'])}</h3>
        <p class="meta">traceId=<code>{esc(c.get('traceId'))}</code> · nats={esc(c.get('nats_count'))} · http={esc(c.get('http_count'))}</p>
      </header>
      <h4>① 設定步驟</h4>
      {pre(steps)}
      <h4>② Relay eval API 回應</h4>
      {pre(json.dumps(c.get('relay_eval'), ensure_ascii=False, indent=2))}
      <h4>③ Demo API 回應</h4>
      {pre(json.dumps(c.get('api'), ensure_ascii=False, indent=2))}
      <h4>④ ClickHouse 查詢結果</h4>
      {pre(c.get('clickhouse') or '')}
      <h4>⑤ 證據檔案（docs/evidence/）</h4>
      {pre(json.dumps(c.get('files'), ensure_ascii=False, indent=2))}
    </article>
"""
        )

    def toc_for(cat: str) -> str:
        items = []
        for r in unit["results"]:
            if r["case"]["category"] != cat:
                continue
            cid = r["case"]["id"]
            title = r["case"]["title_zh"][:28]
            items.append(
                f'<li><a href="#unit-{esc(cid)}"><code>{esc(cid)}</code> {esc(title)}</a></li>'
            )
        return "\n".join(items)

    toc_live = "\n".join(
        f'<li><a href="#live-{esc(c["id"])}"><code>{esc(c["id"])}</code> {esc(c["title"][:32])}</a></li>'
        for c in live["cases"]
    )

    unit_rows = []
    for r in unit["results"]:
        c = r["case"]
        exp = "ERR" if c.get("expect_error") else ("ON" if c.get("expect_on") else "OFF")
        if r.get("got_error"):
            got = "ERR"
        else:
            got = "ON" if r.get("got_on") else "OFF"
        mark = "✅" if r["pass"] else "❌"
        unit_rows.append(
            f"<tr><td><a href='#unit-{esc(c['id'])}'><code>{esc(c['id'])}</code></a></td>"
            f"<td>{esc(c['title_zh'])}</td><td>{exp}</td><td>{got}</td><td>{mark}</td></tr>"
        )

    live_rows = []
    for c in live["cases"]:
        mark = "✅" if c["pass"] else "❌"
        live_rows.append(
            f"<tr><td><a href='#live-{esc(c['id'])}'><code>{esc(c['id'])}</code></a></td>"
            f"<td>{esc(c['title'])}</td>"
            f"<td>{esc((c.get('relay_eval') or {}).get('value'))}</td>"
            f"<td>{esc(c.get('nats_count'))}</td>"
            f"<td>{esc(c.get('http_count'))}</td>"
            f"<td>{mark}</td></tr>"
        )

    ladder = """優先序（先有意見者勝出）：
  relay  >  env  >  option (WithTracingEnabled)  >  hardcoded default

合成：
  tracing = master && moduleTracing

  master  : relay key otel-instrumentation-go-tracing
            | OTEL_INSTRUMENTATION_GO_TRACING_ENABLED
            | default true（否決開關，truthy 不會「打開」任何模組）
  module  : relay key otel-nats-tracing
            | OTEL_NATS_TRACING_ENABLED
            | WithTracingEnabled
            | default false

OpenFeature domain：otel-instrumentation-go
（named provider；只裝 default slot 不算 library relay）"""

    repro = """# 1) 單元矩陣 → docs/evidence/matrix-unit.json
cd backend && go test ./internal/flagmatrix/ -v -count=1

# 2) 叢集 live 證據（需 demo namespace）
# 逐步：apply ConfigMap → wait 75s → curl relay eval → curl demo-trace → clickhouse
# 或：
./docs/scripts/capture-live-evidence.sh

# 3) 重產本 HTML
python3 docs/scripts/render-flag-matrix-html.py

# 瀏覽
open docs/otel-nats-feature-flag-matrix.zh-TW.html"""

    doc = f"""<!DOCTYPE html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>otel-nats 功能旗標閘門與回退矩陣 — 測試證據報告（繁中）</title>
  <style>
    :root {{
      --bg:#0f1419; --surface:#1a2332; --surface-2:#243044; --border:#2d3a4f;
      --text:#e7ecf3; --muted:#9aa8bc; --accent:#3d9cf0; --ok:#3ecf8e; --fail:#e85d5d;
      --code-bg:#0b1016; --mono:"JetBrains Mono","SF Mono",ui-monospace,monospace;
      --sans:"Noto Sans TC","PingFang TC","Microsoft JhengHei",system-ui,sans-serif;
    }}
    * {{ box-sizing:border-box; }}
    body {{ margin:0; font-family:var(--sans); background:var(--bg); color:var(--text); line-height:1.65; }}
    .layout {{ display:grid; grid-template-columns:280px 1fr; min-height:100vh; }}
    nav {{ position:sticky; top:0; height:100vh; overflow:auto; background:var(--surface); border-right:1px solid var(--border); padding:1.25rem 1rem 2rem; }}
    nav .brand {{ color:var(--accent); font-weight:700; font-size:.95rem; }}
    nav .sub {{ color:var(--muted); font-size:.75rem; margin:.25rem 0 1rem; }}
    nav h4 {{ margin:1rem 0 .35rem; font-size:.7rem; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); }}
    nav ul {{ list-style:none; padding:0; margin:0; }}
    nav a {{ display:block; color:var(--muted); text-decoration:none; font-size:.78rem; padding:.3rem .45rem; border-radius:4px; }}
    nav a:hover {{ background:var(--surface-2); color:var(--text); }}
    main {{ max-width:960px; padding:2rem 2.25rem 4rem; }}
    h1 {{ font-size:1.55rem; margin:0 0 .5rem; line-height:1.35; }}
    h2 {{ margin-top:2.5rem; border-bottom:1px solid var(--border); padding-bottom:.4rem; }}
    h3 {{ margin:.25rem 0; font-size:1.05rem; }}
    h4 {{ margin:1rem 0 .35rem; color:var(--muted); font-size:.85rem; }}
    .lead {{ color:var(--muted); }}
    .stats {{ display:flex; gap:1rem; flex-wrap:wrap; margin:1rem 0 1.5rem; }}
    .stat {{ background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:.75rem 1rem; min-width:140px; }}
    .stat b {{ display:block; font-size:1.4rem; }}
    .stat span {{ color:var(--muted); font-size:.8rem; }}
    .case {{ background:var(--surface); border:1px solid var(--border); border-radius:10px; padding:1rem 1.15rem 1.25rem; margin:1rem 0 1.5rem; }}
    .meta {{ color:var(--muted); font-size:.8rem; margin:.2rem 0 .6rem; }}
    .badge {{ display:inline-block; font-size:.72rem; font-weight:700; padding:.15rem .45rem; border-radius:999px; vertical-align:middle; }}
    .badge.ok {{ background:rgba(62,207,142,.15); color:var(--ok); border:1px solid rgba(62,207,142,.35); }}
    .badge.fail {{ background:rgba(232,93,93,.15); color:var(--fail); border:1px solid rgba(232,93,93,.35); }}
    pre.code {{ background:var(--code-bg); border:1px solid var(--border); border-radius:8px; padding:.75rem .9rem; overflow:auto; font-family:var(--mono); font-size:.78rem; line-height:1.45; white-space:pre-wrap; word-break:break-word; }}
    table {{ width:100%; border-collapse:collapse; font-size:.88rem; margin:1rem 0; }}
    th, td {{ border:1px solid var(--border); padding:.45rem .55rem; text-align:left; vertical-align:top; }}
    th {{ background:var(--surface-2); }}
    code {{ font-family:var(--mono); font-size:.85em; color:#9cd1ff; }}
    .callout {{ background:var(--surface-2); border-left:3px solid var(--accent); padding:.75rem 1rem; margin:1rem 0; border-radius:0 8px 8px 0; }}
    footer {{ color:var(--muted); font-size:.8rem; margin-top:3rem; }}
    @media (max-width:900px) {{ .layout {{ grid-template-columns:1fr; }} nav {{ position:relative; height:auto; }} }}
  </style>
</head>
<body>
<div class="layout">
  <nav>
    <div class="brand">otel-nats flag matrix</div>
    <div class="sub">閘門 / 回退 · 逐步設定 · 證據 · zh-TW</div>
    <h4>總覽</h4>
    <a href="#overview">報告說明</a>
    <a href="#ladder">階梯模型</a>
    <a href="#baseline">叢集基準設定</a>
    <a href="#summary-table">結果總表</a>
    <h4>單元 · local</h4>
    <ul>{toc_for("local")}</ul>
    <h4>單元 · invalid</h4>
    <ul>{toc_for("invalid")}</ul>
    <h4>單元 · relay</h4>
    <ul>{toc_for("relay")}</ul>
    <h4>叢集 live</h4>
    <ul>{toc_live}</ul>
    <h4>重跑</h4>
    <a href="#repro">如何重跑證據</a>
  </nav>
  <main>
    <h1>otel-nats 功能旗標閘門與回退組合<br/>測試證據報告</h1>
    <p class="lead">每個案例都記錄：<strong>你改了什麼設定</strong> → <strong>預期</strong> → <strong>API / 測試 / ClickHouse 證據</strong> → <strong>是否通過</strong>。
    證據時間（UTC）：單元 <code>{esc(unit.get('generated_at'))}</code> · 叢集 <code>{esc(live.get('generated_at'))}</code>。</p>

    <div class="stats" id="overview">
      <div class="stat"><b>{unit['passed']}/{unit['total']}</b><span>單元矩陣通過</span></div>
      <div class="stat"><b>{live['passed']}/{live['total']}</b><span>叢集實測通過</span></div>
      <div class="stat"><b>{unit['failed']}</b><span>單元失敗數</span></div>
      <div class="stat"><b>{esc(live.get('cluster'))}</b><span>kubectl context</span></div>
    </div>

    <div class="callout">
      <strong>證據類型</strong><br/>
      · <em>單元矩陣</em>：Go 測試呼叫 <code>otelnats.ConnectWithOptions</code> + in-memory OpenFeature named provider，讀 <code>conn.TracingEnabled()</code>。原始 JSON：<code>docs/evidence/matrix-unit.json</code>。<br/>
      · <em>叢集實測</em>：真實 GOFF + ConfigMap volume + backend zero-code。每步保留 relay eval / demo API / ClickHouse 輸出。<br/>
      · 未另附 Grafana 截圖；ClickHouse span 列即「有沒有 NATS instrumentation」的客觀證據。
    </div>

    <h2 id="ladder">階梯模型</h2>
    {pre(ladder)}

    <h2 id="baseline">叢集基準設定（option C）</h2>
    <p>Live 測試期間 <code>deployment/backend</code> 環境（只改 ConfigMap、不重啟 backend）：</p>
    {pre(baseline_env)}
    <p class="meta">{esc(live.get('note', ''))}</p>

    <h2 id="summary-table">結果總表</h2>
    <h3>單元矩陣</h3>
    <table>
      <thead><tr><th>ID</th><th>標題</th><th>預期</th><th>實際</th><th>結果</th></tr></thead>
      <tbody>
      {''.join(unit_rows)}
      </tbody>
    </table>

    <h3>叢集實測</h3>
    <table>
      <thead><tr><th>ID</th><th>標題</th><th>relay value</th><th>NATS</th><th>HTTP</th><th>結果</th></tr></thead>
      <tbody>
      {''.join(live_rows)}
      </tbody>
    </table>

    <h2 id="unit">單元矩陣詳情</h2>
    {''.join(unit_sections)}

    <h2 id="live">叢集實測詳情</h2>
    {''.join(live_sections)}

    <h2 id="repro">如何重跑證據</h2>
    {pre(repro)}

    <footer>
      證據目錄：<code>docs/evidence/</code><br/>
      函式庫文件：<code>third_party/instrumentation-go/docs/feature-flags.zh-TW.md</code><br/>
      矩陣測試：<code>backend/internal/flagmatrix/matrix_test.go</code>
    </footer>
  </main>
</div>
</body>
</html>
"""
    OUT.write_text(doc, encoding="utf-8")
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
