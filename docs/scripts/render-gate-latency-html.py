#!/usr/bin/env python3
"""Render docs/otel-nats-gate-latency.zh-TW.html from docs/evidence/perf/gate-latency.json."""
from __future__ import annotations

import html
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
EVID = ROOT / "docs" / "evidence" / "perf" / "gate-latency.json"
OUT = ROOT / "docs" / "otel-nats-gate-latency.zh-TW.html"


def esc(s) -> str:
    return html.escape("" if s is None else str(s))


def pre(text: str) -> str:
    return f'<pre class="code">{esc(text)}</pre>'


def fmt_us(v: float) -> str:
    if v < 0.01:
        return f"{v:.4f}"
    if v < 1:
        return f"{v:.3f}"
    if v < 100:
        return f"{v:.2f}"
    return f"{v:.1f}"


def main() -> None:
    data = json.loads(EVID.read_text(encoding="utf-8"))
    modes = {m["id"]: m for m in data["modes"]}
    results = data["results"]
    comps = data["comparisons"]

    # pivot: mode -> n -> result
    by: dict[str, dict[int, dict]] = {}
    for r in results:
        by.setdefault(r["mode_id"], {})[r["n"]] = r

    mode_sections = []
    for m in data["modes"]:
        mid = m["id"]
        steps = "\n".join(f"{i+1}. {s}" for i, s in enumerate(m["config_steps"]))
        rows = []
        for n in data["volumes"]:
            r = by[mid][n]
            rows.append(
                f"<tr><td>{n:,}</td>"
                f"<td>{fmt_us(r['avg_us'])}</td>"
                f"<td>{fmt_us(r['p50_ns']/1000)}</td>"
                f"<td>{fmt_us(r['p95_ns']/1000)}</td>"
                f"<td>{r['ops_per_sec']:,.0f}</td>"
                f"<td>{'是' if r['tracing_enabled'] else '否'}</td>"
                f"<td>{r['errors']}</td></tr>"
            )
        mode_sections.append(
            f"""
    <article class="case" id="mode-{esc(mid)}">
      <header>
        <h3><code>{esc(mid)}</code> {esc(m['title_zh'])}</h3>
        <p class="meta">{esc(m['description_zh'])}</p>
      </header>
      <h4>① 設定步驟</h4>
      {pre(steps)}
      <h4>② 各量級平均延遲（µs / call）</h4>
      <table>
        <thead><tr><th>N（Publish 次數）</th><th>avg µs</th><th>p50 µs</th><th>p95 µs</th><th>ops/s</th><th>有 span</th><th>errors</th></tr></thead>
        <tbody>{''.join(rows)}</tbody>
      </table>
      <h4>③ 原始結果 JSON 片段</h4>
      {pre(json.dumps([by[mid][n] for n in data['volumes']], ensure_ascii=False, indent=2))}
    </article>
"""
        )

    comp_rows = []
    for c in comps:
        comp_rows.append(
            f"<tr><td>{c['n']:,}</td>"
            f"<td>{fmt_us(c['base_avg_us'])}</td>"
            f"<td>{fmt_us(c['env_on_avg_us'])}</td>"
            f"<td>{fmt_us(c['flag_on_avg_us'])}</td>"
            f"<td>{fmt_us(c['flag_off_avg_us'])}</td>"
            f"<td>{fmt_us(c['flag_on_minus_env_on_us'])} "
            f"({c['flag_on_over_env_on_ratio']:.2f}×)</td>"
            f"<td>{fmt_us(c['flag_on_minus_base_us'])} "
            f"({c['flag_on_over_base_ratio']:.1f}×)</td>"
            f"<td>{fmt_us(c['flag_off_minus_base_us'])} "
            f"({c['flag_off_over_base_ratio']:.1f}×)</td></tr>"
        )

    # simple bar chart using CSS for largest N
    largest_n = max(data["volumes"])
    bars = []
    max_avg = max(by[m][largest_n]["avg_us"] for m in by)
    colors = {
        "no_flag_no_env": "#3ecf8e",
        "env_on_no_flag": "#3d9cf0",
        "flag_on": "#e6a23c",
        "flag_off": "#9aa8bc",
    }
    for mid in ["no_flag_no_env", "env_on_no_flag", "flag_off", "flag_on"]:
        avg = by[mid][largest_n]["avg_us"]
        pct = max(2.0, 100.0 * avg / max_avg) if max_avg else 0
        bars.append(
            f'<div class="bar-row"><span class="bar-label"><code>{esc(mid)}</code></span>'
            f'<div class="bar-track"><div class="bar" style="width:{pct:.1f}%;background:{colors[mid]}"></div></div>'
            f'<span class="bar-val">{fmt_us(avg)} µs</span></div>'
        )

    method = data.get("method_zh", "")
    host = f"{data.get('goos')}/{data.get('goarch')} · {data.get('go_version')} · {data.get('num_cpu')} CPU"
    repro = (
        "# 跑效能套件 → docs/evidence/perf/gate-latency.json\n"
        "cd backend && go test ./internal/flagperf/ -v -count=1\n"
        "\n"
        "# 產生 HTML\n"
        "python3 docs/scripts/render-gate-latency-html.py\n"
        "\n"
        "# 開啟\n"
        "open docs/otel-nats-gate-latency.zh-TW.html\n"
    )

    toc_modes = "\n".join(
        f'<li><a href="#mode-{esc(m["id"])}"><code>{esc(m["id"])}</code></a></li>'
        for m in data["modes"]
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
    main {{ max-width:980px; padding:2rem 2.25rem 4rem; }}
    h1 {{ font-size:1.5rem; margin:0 0 .5rem; line-height:1.35; }}
    h2 {{ margin-top:2.25rem; border-bottom:1px solid var(--border); padding-bottom:.4rem; }}
    h3 {{ margin:.25rem 0; font-size:1.05rem; }}
    h4 {{ margin:1rem 0 .35rem; color:var(--muted); font-size:.85rem; }}
    .lead,.meta {{ color:var(--muted); }}
    .case {{ background:var(--surface); border:1px solid var(--border); border-radius:10px; padding:1rem 1.15rem 1.25rem; margin:1rem 0 1.5rem; }}
    pre.code {{ background:var(--code-bg); border:1px solid var(--border); border-radius:8px; padding:.75rem .9rem; overflow:auto; font-family:var(--mono); font-size:.78rem; line-height:1.45; white-space:pre-wrap; word-break:break-word; }}
    table {{ width:100%; border-collapse:collapse; font-size:.88rem; margin:1rem 0; }}
    th, td {{ border:1px solid var(--border); padding:.45rem .55rem; text-align:left; }}
    th {{ background:var(--surface-2); }}
    code {{ font-family:var(--mono); font-size:.85em; color:#9cd1ff; }}
    .callout {{ background:var(--surface-2); border-left:3px solid var(--accent); padding:.75rem 1rem; margin:1rem 0; border-radius:0 8px 8px 0; }}
    .warn {{ border-left-color:var(--warn); }}
    .stats {{ display:flex; gap:1rem; flex-wrap:wrap; margin:1rem 0; }}
    .stat {{ background:var(--surface); border:1px solid var(--border); border-radius:8px; padding:.75rem 1rem; min-width:150px; }}
    .stat b {{ display:block; font-size:1.25rem; }}
    .stat span {{ color:var(--muted); font-size:.8rem; }}
    .bar-row {{ display:grid; grid-template-columns:180px 1fr 90px; gap:.6rem; align-items:center; margin:.4rem 0; }}
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
    <div class="sub">feature flag 閘門 · 各量級 · 證據 · zh-TW</div>
    <h4>總覽</h4>
    <a href="#overview">目的與方法</a>
    <a href="#summary">比較總表</a>
    <a href="#chart">N={largest_n:,} 長條圖</a>
    <h4>模式</h4>
    <ul>{toc_modes}</ul>
    <h4>重跑</h4>
    <a href="#repro">如何重跑</a>
  </nav>
  <main>
    <h1>otel-nats feature-flag 閘門延遲<br/>效能證據報告</h1>
    <p class="lead">評估「有開 feature flag（每 call 評估閘門）」相對於「完全不使用 feature flag / env」
    在大量 Publish 需要送 trace 時，<strong>單次 call 平均 response time</strong> 的差異。
    量級：N = {', '.join(str(n) for n in data['volumes'])}。</p>

    <div class="stats" id="overview">
      <div class="stat"><b>{len(data['modes'])}</b><span>設定模式</span></div>
      <div class="stat"><b>{len(data['volumes'])}</b><span>請求量級</span></div>
      <div class="stat"><b>{len(results)}</b><span>量測結果列</span></div>
      <div class="stat"><b>{esc(data.get('generated_at'))}</b><span>UTC 產生時間</span></div>
    </div>

    <div class="callout">
      <strong>量測方法</strong><br/>
      {esc(method)}<br/><br/>
      主機：<code>{esc(host)}</code> · payload {data.get('payload_bytes')} bytes · library：{esc(data.get('library'))}
    </div>

    <div class="callout warn">
      <strong>解讀重點</strong>
      <ul>
        <li><code>no_flag_no_env</code>：零 OpenFeature、不發 span — 下限基準。</li>
        <li><code>env_on_no_flag</code>：有 span、無 OF — 「需要送 trace」但不走 feature flag 的基準。</li>
        <li><code>flag_on</code>：每 call 評估 OF 且發 span — 本 demo option C + relay 啟用路徑。</li>
        <li><code>flag_off</code>：每 call 仍評估 OF，但不發 span — 閘門本身開銷。</li>
        <li>關鍵比值：<strong>flag_on / env_on_no_flag</strong> = 在同樣要送 trace 時，加上 feature-flag 閘門的倍數成本。</li>
      </ul>
    </div>

    <h2 id="summary">比較總表（平均 µs / call）</h2>
    <table>
      <thead>
        <tr>
          <th>N</th>
          <th>no_flag_no_env<br/><span class="meta">零閘門</span></th>
          <th>env_on_no_flag<br/><span class="meta">有 span 無 OF</span></th>
          <th>flag_on<br/><span class="meta">OF+span</span></th>
          <th>flag_off<br/><span class="meta">OF 無 span</span></th>
          <th>flag_on − env_on<br/><span class="meta">閘門增量（送 trace）</span></th>
          <th>flag_on − base<br/><span class="meta">相對零閘門</span></th>
          <th>flag_off − base<br/><span class="meta">純 OF 評估</span></th>
        </tr>
      </thead>
      <tbody>
      {''.join(comp_rows)}
      </tbody>
    </table>

    <h2 id="chart">N = {largest_n:,} 平均延遲比較</h2>
    {''.join(bars)}

    <h2 id="modes">各模式逐步設定與證據</h2>
    {''.join(mode_sections)}

    <h2 id="repro">如何重跑</h2>
    {pre(repro)}

    <div class="callout">
      原始證據：<code>docs/evidence/perf/gate-latency.json</code> ·
      測試日誌：<code>docs/evidence/perf/gate-latency-test.log</code> ·
      測試碼：<code>backend/internal/flagperf/gate_latency_test.go</code>
    </div>

    <footer>
      這是單機、嵌入式 NATS、記憶體 tracer 的微基準，用來比較<strong>閘門評估</strong>相對量級，
      不是端到端 HTTP/OTLP 吞吐 SLA。網路 OTLP export、relay 遠端 poll 會另加延遲。
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
