#!/usr/bin/env python3
"""Render docs/otel-nats-gate-latency.zh-TW.html from docs/evidence/perf/gate-latency.json."""
from __future__ import annotations

import html
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
EVID = ROOT / "docs" / "evidence" / "perf" / "gate-latency.json"
OUT = ROOT / "docs" / "otel-nats-gate-latency.zh-TW.html"

KIND_TITLES = {
    "publish": "序列 Publish",
    "parallel": "併發 Publish",
    "roundtrip": "完整往返（publish → 消費 → 回覆 → 消費）",
}

KIND_NOTES = {
    "publish": "單一 goroutine 逐次 conn.Publish。閘門成本要看這裡的差額。",
    "parallel": "同一個 Conn 上 4×GOMAXPROCS 條 goroutine 併發 Publish。單次延遲含排隊，"
                "重點是與序列版的差距 — 那就是鎖競爭放大的部分。",
    "roundtrip": "等同 natsflow 的 demo 往返（扣掉 5ms 模擬工作）。一次往返解析四次閘門、共 8 次 OpenFeature 評估，"
                 "但總時間由 NATS 傳遞與排隊主導 — 這張表回答的是「閘門佔一次真實業務往返的多少」。",
}

MODE_COLORS = {
    "no_flag_no_env": "#3ecf8e",
    "env_on_no_flag": "#3d9cf0",
    "flag_off_relay": "#9aa8bc",
    "flag_on_relay": "#e6a23c",
    "flag_off_memprovider": "#6b7a90",
    "flag_on_memprovider": "#c97f2e",
}


def esc(s) -> str:
    return html.escape("" if s is None else str(s))


def pre(text: str) -> str:
    return f'<pre class="code">{esc(text)}</pre>'


def fmt_us(v: float) -> str:
    if v is None:
        return "—"
    if abs(v) < 0.01:
        return f"{v:.4f}"
    if abs(v) < 1:
        return f"{v:.3f}"
    if abs(v) < 100:
        return f"{v:.2f}"
    return f"{v:,.1f}"


def ns_to_us(v: float) -> float:
    return (v or 0) / 1000.0


def main() -> None:
    data = json.loads(EVID.read_text(encoding="utf-8"))
    modes = data["modes"]
    mode_by_id = {m["id"]: m for m in modes}
    aggs = data["aggregates"]
    env = data["env"]

    # kind -> mode_id -> aggregate
    by_kind: dict[str, dict[str, dict]] = {}
    for a in aggs:
        by_kind.setdefault(a["kind"], {})[a["mode_id"]] = a

    # ---- per-kind result tables -------------------------------------------
    kind_sections = []
    for kind, per_mode in by_kind.items():
        rows = []
        for m in modes:
            a = per_mode.get(m["id"])
            if not a:
                continue
            rows.append(
                f"<tr><td><code>{esc(m['id'])}</code></td>"
                f"<td class='num'><strong>{fmt_us(a['median_us'])}</strong></td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['trimmed_avg_ns']))}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['avg_ns']))}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['p95_ns']))}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['p99_ns']))}</td>"
                f"<td class='num'>{a['run_spread_pct']:.1f}%</td>"
                f"<td class='num'>{a['mallocs_per_op']:.2f}</td>"
                f"<td class='num'>{a['ops_per_sec']:,.0f}</td>"
                f"<td>{'是' if a['tracing_enabled'] else '否'}</td>"
                f"<td class='num'>{a['evals_per_op']}</td>"
                f"<td class='num'>{a['errors'] + a['timeouts']}</td></tr>"
            )
        any_agg = next(iter(per_mode.values()))
        kind_sections.append(f"""
    <article class="case" id="kind-{esc(kind)}">
      <header>
        <h3>{esc(KIND_TITLES.get(kind, kind))} <code>{esc(kind)}</code></h3>
        <p class="meta">{esc(KIND_NOTES.get(kind, ''))}</p>
        <p class="meta">每 run {any_agg['repeats']} 次獨立子行程 · goroutine 數 {any_agg['workers']}</p>
      </header>
      <table>
        <thead><tr>
          <th>mode</th><th>中位數 µs<br/><span class="meta">主指標</span></th>
          <th>截尾均值 µs</th><th>平均 µs</th><th>p95 µs</th><th>p99 µs</th>
          <th>run 間離散</th><th>allocs/op</th><th>ops/s</th>
          <th>有 span</th><th>評估次數</th><th>錯誤</th>
        </tr></thead>
        <tbody>{''.join(rows)}</tbody>
      </table>
    </article>""")

    # ---- derived comparisons ----------------------------------------------
    comp_rows = []
    for c in data["comparisons"]:
        ratio = f"{c['ratio']:.2f}×" if c["ratio"] else "—"
        comp_rows.append(
            f"<tr><td><code>{esc(c['kind'])}</code></td>"
            f"<td>{esc(c['label_zh'])}</td>"
            f"<td><code>{esc(c['left_mode'])}</code><br/>{fmt_us(c['left_us'])} µs</td>"
            f"<td><code>{esc(c['right_mode'])}</code><br/>{fmt_us(c['right_us'])} µs</td>"
            f"<td class='num'><strong>{fmt_us(c['delta_us'])} µs</strong></td>"
            f"<td class='num'>{ratio}</td>"
            f"<td class='meta'>{esc(c['explanation_zh'])}</td></tr>"
        )

    # ---- bar chart, serial publish ----------------------------------------
    bars = []
    publish = by_kind.get("publish", {})
    if publish:
        max_us = max(a["median_us"] for a in publish.values())
        order = ["no_flag_no_env", "env_on_no_flag", "flag_off_relay",
                 "flag_off_memprovider", "flag_on_relay", "flag_on_memprovider"]
        for mid in order:
            a = publish.get(mid)
            if not a:
                continue
            pct = max(2.0, 100.0 * a["median_us"] / max_us) if max_us else 0
            bars.append(
                f'<div class="bar-row"><span class="bar-label"><code>{esc(mid)}</code></span>'
                f'<div class="bar-track"><div class="bar" style="width:{pct:.1f}%;'
                f'background:{MODE_COLORS.get(mid, "#3d9cf0")}"></div></div>'
                f'<span class="bar-val">{fmt_us(a["median_us"])} µs</span></div>'
            )

    # ---- per-mode detail ---------------------------------------------------
    mode_sections = []
    for m in modes:
        steps = "\n".join(f"{i+1}. {s}" for i, s in enumerate(m["config_steps"]))
        rows = []
        for kind in by_kind:
            a = by_kind[kind].get(m["id"])
            if not a:
                continue
            rows.append(
                f"<tr><td><code>{esc(kind)}</code></td>"
                f"<td class='num'>{fmt_us(a['median_us'])}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['min_run_median_ns']))} – "
                f"{fmt_us(ns_to_us(a['max_run_median_ns']))}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['p99_ns']))}</td>"
                f"<td class='num'>{a['mallocs_per_op']:.2f}</td>"
                f"<td class='num'>{a['bytes_per_op']:,.0f}</td>"
                f"<td class='num'>{fmt_us(ns_to_us(a['median_minus_timing_overhead_ns']))}</td></tr>"
            )
        raw = [a for kind in by_kind for a in [by_kind[kind].get(m["id"])] if a]
        mode_sections.append(f"""
    <article class="case" id="mode-{esc(m['id'])}">
      <header>
        <h3><code>{esc(m['id'])}</code> {esc(m['title_zh'])}</h3>
        <p class="meta">{esc(m['description_zh'])}</p>
        <p class="meta">provider 姿勢：<code>{esc(m['posture'])}</code> ·
        RelayPossible：{'是' if m['expect_relay_possible'] else '否'} ·
        每次 Publish 的 OpenFeature 評估次數：{m['evals_per_publish']}</p>
      </header>
      <h4>① 設定步驟</h4>
      {pre(steps)}
      <h4>② 各量測結果（µs / op）</h4>
      <table>
        <thead><tr><th>kind</th><th>中位數</th><th>各 run 中位數範圍</th><th>p99</th>
        <th>allocs/op</th><th>B/op</th><th>扣掉計時開銷</th></tr></thead>
        <tbody>{''.join(rows)}</tbody>
      </table>
      <h4>③ 原始聚合 JSON</h4>
      {pre(json.dumps(raw, ensure_ascii=False, indent=2))}
    </article>""")

    caveats = "".join(f"<li>{esc(c)}</li>" for c in data["caveats_zh"])

    host = (f"{env['goos']}/{env['goarch']} · {env['go_version']} · "
            f"{env['num_cpu']} CPU · GOMAXPROCS={env['gomaxprocs']} · "
            f"GOGC={env['gogc']} · nats-server {env['nats_server_version']}")
    cgroup = env.get("cgroup_cpu_max") or "（未偵測到 cgroup CPU 限制）"

    pub = by_kind.get("publish", {})
    any_pub = next(iter(pub.values())) if pub else {}
    clock = any_pub.get("clock_granularity_ns", 0)
    overhead = any_pub.get("timing_overhead_ns", 0)

    repro = (
        "# 全套量測 → docs/evidence/perf/gate-latency.json（約 25 分鐘）\n"
        "cd backend && go test ./internal/flagperf/ -run TestGateLatencyEvidence -v -count=1 -timeout 60m\n"
        "\n"
        "# 縮小規模的煙霧測試\n"
        "FLAGPERF_TOTAL_OPS=5000 FLAGPERF_RT_OPS=2000 FLAGPERF_REPEATS=2 FLAGPERF_RT_REPEATS=1 \\\n"
        "  go test ./internal/flagperf/ -run TestGateLatencyEvidence -count=1\n"
        "\n"
        "# allocs/op 與 benchstat（一個 mode 一個行程 — provider 安裝是 process latch）\n"
        "for m in no_flag_no_env env_on_no_flag flag_off_memprovider \\\n"
        "         flag_on_memprovider flag_off_relay flag_on_relay; do\n"
        "  FLAGPERF_MODE=$m go test ./internal/flagperf/ -run '^$' \\\n"
        "    -bench 'BenchmarkPublish$' -benchmem -count=10 | tee \"bench-$m.txt\"\n"
        "done\n"
        "benchstat bench-no_flag_no_env.txt bench-flag_on_relay.txt\n"
        "\n"
        "# 產生 HTML\n"
        "python3 docs/scripts/render-gate-latency-html.py\n"
    )

    toc_kinds = "\n".join(
        f'<li><a href="#kind-{esc(k)}">{esc(KIND_TITLES.get(k, k))}</a></li>' for k in by_kind
    )
    toc_modes = "\n".join(
        f'<li><a href="#mode-{esc(m["id"])}"><code>{esc(m["id"])}</code></a></li>' for m in modes
    )

    doc = f"""<!DOCTYPE html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>otel-nats feature-flag 閘門延遲 — 效能證據報告（繁中）</title>
  <style>
    :root {{
      --bg:#0f1419; --surface:#1a2332; --surface-2:#243044; --border:#2d3a4f;
      --text:#e7ecf3; --muted:#9aa8bc; --accent:#3d9cf0; --ok:#3ecf8e; --warn:#e6a23c;
      --code-bg:#0b1016; --mono:"JetBrains Mono","SF Mono",ui-monospace,monospace;
      --sans:"Noto Sans TC","PingFang TC","Microsoft JhengHei",system-ui,sans-serif;
    }}
    * {{ box-sizing:border-box; }}
    body {{ margin:0; font-family:var(--sans); background:var(--bg); color:var(--text); line-height:1.65; }}
    .layout {{ display:grid; grid-template-columns:260px 1fr; min-height:100vh; }}
    nav {{ position:sticky; top:0; height:100vh; overflow:auto; background:var(--surface); border-right:1px solid var(--border); padding:1.25rem 1rem 2rem; }}
    nav .brand {{ color:var(--accent); font-weight:700; }}
    nav .sub {{ color:var(--muted); font-size:.75rem; margin:.25rem 0 1rem; }}
    nav h4 {{ margin:1rem 0 .35rem; font-size:.7rem; text-transform:uppercase; letter-spacing:.06em; color:var(--muted); }}
    nav ul {{ list-style:none; padding:0; margin:0; }}
    nav a {{ display:block; color:var(--muted); text-decoration:none; font-size:.8rem; padding:.3rem .45rem; border-radius:4px; }}
    nav a:hover {{ background:var(--surface-2); color:var(--text); }}
    main {{ max-width:1100px; padding:2rem 2.25rem 4rem; }}
    h1 {{ font-size:1.5rem; margin:0 0 .5rem; line-height:1.35; }}
    h2 {{ margin-top:2.25rem; border-bottom:1px solid var(--border); padding-bottom:.4rem; }}
    h3 {{ margin:.25rem 0; font-size:1.05rem; }}
    h4 {{ margin:1rem 0 .35rem; color:var(--muted); font-size:.85rem; }}
    .lead,.meta {{ color:var(--muted); }}
    .case {{ background:var(--surface); border:1px solid var(--border); border-radius:10px; padding:1rem 1.15rem 1.25rem; margin:1rem 0 1.5rem; }}
    pre.code {{ background:var(--code-bg); border:1px solid var(--border); border-radius:8px; padding:.75rem .9rem; overflow:auto; font-family:var(--mono); font-size:.78rem; line-height:1.45; white-space:pre-wrap; word-break:break-word; }}
    .scroll {{ overflow-x:auto; }}
    table {{ width:100%; border-collapse:collapse; font-size:.85rem; margin:1rem 0; }}
    th, td {{ border:1px solid var(--border); padding:.45rem .55rem; text-align:left; vertical-align:top; }}
    th {{ background:var(--surface-2); }}
    td.num {{ text-align:right; font-family:var(--mono); font-size:.8rem; white-space:nowrap; }}
    code {{ font-family:var(--mono); font-size:.85em; color:#9cd1ff; }}
    .callout {{ background:var(--surface-2); border-left:3px solid var(--accent); padding:.75rem 1rem; margin:1rem 0; border-radius:0 8px 8px 0; }}
    .warn {{ border-left-color:var(--warn); }}
    .stats {{ display:flex; gap:1rem; flex-wrap:wrap; margin:1rem 0; }}
    .stat {{ background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:.75rem 1rem; min-width:150px; }}
    .stat b {{ display:block; font-size:1.25rem; }}
    .stat span {{ color:var(--muted); font-size:.8rem; }}
    .bar-row {{ display:grid; grid-template-columns:200px 1fr 100px; gap:.6rem; align-items:center; margin:.4rem 0; }}
    .bar-label {{ font-size:.78rem; }}
    .bar-track {{ background:var(--code-bg); border:1px solid var(--border); border-radius:4px; height:18px; overflow:hidden; }}
    .bar {{ height:100%; border-radius:3px; }}
    .bar-val {{ font-family:var(--mono); font-size:.78rem; text-align:right; color:var(--muted); }}
    footer {{ color:var(--muted); font-size:.8rem; margin-top:3rem; }}
    @media (max-width:900px) {{ .layout {{ grid-template-columns:1fr; }} nav {{ position:relative; height:auto; }} .bar-row {{ grid-template-columns:1fr; }} }}
  </style>
</head>
<body>
<div class="layout">
  <nav>
    <div class="brand">otel-nats gate latency</div>
    <div class="sub">feature flag 閘門 · {data['ops_per_run']:,} 次請求 · 證據 · zh-TW</div>
    <h4>總覽</h4>
    <a href="#overview">目的與方法</a>
    <a href="#chart">序列 Publish 長條圖</a>
    <a href="#comparisons">關鍵差額</a>
    <h4>量測面向</h4>
    <ul>{toc_kinds}</ul>
    <h4>模式</h4>
    <ul>{toc_modes}</ul>
    <h4>重跑</h4>
    <a href="#repro">如何重跑</a>
  </nav>
  <main>
    <h1>otel-nats feature-flag 閘門延遲<br/>效能證據報告</h1>
    <p class="lead">問題：在需要送 trace 的高流量下，<strong>每次操作評估一次 feature-flag 閘門</strong>要多少成本？
    對照組有兩個 — 完全沒有閘門，以及有 tracing 但沒有閘門。
    量級固定 <strong>{data['ops_per_run']:,} 次操作</strong>，每個模式各跑 {data['repeats']} 個獨立子行程。</p>

    <div class="stats" id="overview">
      <div class="stat"><b>{data['ops_per_run']:,}</b><span>每次 run 的操作數</span></div>
      <div class="stat"><b>{len(modes)}</b><span>設定模式</span></div>
      <div class="stat"><b>{len(data['runs'])}</b><span>獨立子行程</span></div>
      <div class="stat"><b>{esc(data['generated_at'])}</b><span>UTC 產生時間</span></div>
    </div>

    <div class="callout">
      <strong>行程隔離</strong><br/>{esc(data['process_isolation_zh'])}
    </div>

    <div class="callout">
      <strong>量測方法</strong><br/>{esc(data['method_zh'])}<br/><br/>
      主機：<code>{esc(host)}</code><br/>
      cgroup：<code>{esc(cgroup)}</code><br/>
      payload {data['payload_bytes']} bytes · warmup {data['warmup_ops']:,} · library：{esc(data['library'])}<br/>
      時鐘粒度 <code>{clock:.0f} ns</code> · 計時開銷 <code>{overhead:.0f} ns</code>
      — 零閘門基準與這兩個數字同量級，解讀時請一併看。
    </div>

    <div class="callout warn">
      <strong>已知限制</strong>
      <ul>{caveats}</ul>
    </div>

    <h2 id="chart">序列 Publish 中位數延遲</h2>
    {''.join(bars)}

    <h2 id="comparisons">關鍵差額</h2>
    <p class="meta">倍數只在分母有意義時才有意義。零閘門基準是次微秒等級，對它取比值會得到很大但沒有決策價值的數字，
    所以下表以<strong>差額</strong>為主、倍數為輔。</p>
    <div class="scroll">
    <table>
      <thead><tr><th>面向</th><th>比較</th><th>左</th><th>右</th><th>差額</th><th>倍數</th><th>說明</th></tr></thead>
      <tbody>{''.join(comp_rows)}</tbody>
    </table>
    </div>

    <h2 id="kinds">各面向量測結果</h2>
    {''.join(kind_sections)}

    <h2 id="modes">各模式逐步設定與證據</h2>
    {''.join(mode_sections)}

    <h2 id="repro">如何重跑</h2>
    {pre(repro)}

    <div class="callout">
      原始證據：<code>docs/evidence/perf/gate-latency.json</code> ·
      測試碼：<code>backend/internal/flagperf/</code>
      （<code>gate_latency_test.go</code> 驅動、<code>harness_test.go</code> 量測、
      <code>relay_test.go</code> 假 relay、<code>bench_test.go</code> benchstat 入口）
    </div>

    <footer>
      這是單機、行程內 NATS、丟棄式 span exporter 的微基準，用來比較<strong>閘門評估</strong>的相對量級，
      不是端到端 HTTP/OTLP 吞吐 SLA。網路 OTLP export 與 relay 遠端輪詢都會另外加上延遲。
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
