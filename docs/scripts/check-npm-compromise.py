#!/usr/bin/env python3
"""Scan this repo (and third_party JS locks) for known-compromised npm versions.

Exit 0 if clean, 1 if any exact compromised version is present.
Also flags IOC malware files and reports watch-list packages at safe versions.

Sources (non-exhaustive, updated as of 2026-08):
  - Aikido: keyv / cacheable family Shai-Hulud (2026-08-04)
  - Prior chalk/debug campaign (2025-09) high-profile versions
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# package -> set of known-malicious versions
COMPROMISED: dict[str, set[str]] = {
    # 2026-08 keyv family (Shai-Hulud again)
    "keyv": {"6.0.0"},
    "flat-cache": {"6.1.24"},
    "file-entry-cache": {"11.1.6"},
    "cacheable-request": {"13.0.20"},
    "cacheable": {"2.5.1"},
    "@cacheable/memory": {"2.2.1"},
    "cache-manager": {"7.2.10"},
    "@cacheable/node-cache": {"3.1.2"},
    "@cacheable/utils": {"2.5.1"},
    "@cacheable/net": {"2.1.1"},
    "ecto": {"5.0.1"},
    "@deliveroo/reevent": {"1.0.1"},
    "@or-sdk/invitations": {"1.4.9"},
    "@picsart/ai-sdk": {"3.32.2"},
    "@qlik/embed-runtime": {"1.6.4"},
    "picasso.js": {"2.11.6"},
    # 2025-09 chalk/debug campaign (selected high-signal versions)
    "debug": {"4.4.2"},
    "chalk": {"5.6.1"},
    "ansi-styles": {"6.2.2"},
    "supports-color": {"10.2.1"},
    "strip-ansi": {"7.1.1"},
    "ansi-regex": {"6.2.1"},
    "wrap-ansi": {"9.0.1"},
    "color-convert": {"3.1.1"},
    "color-name": {"2.0.1"},
    "is-arrayish": {"0.3.3"},
    "slice-ansi": {"7.1.1"},
    "error-ex": {"1.3.3"},
    "simple-swizzle": {"0.2.3"},
    "has-ansi": {"6.0.1"},
    "chalk-template": {"1.1.1"},
    "supports-hyperlinks": {"4.1.1"},
    "backslash": {"0.2.1"},
    # asyncapi campaign sample
    "@asyncapi/generator": {"3.3.1"},
    "@asyncapi/specs": {"6.11.2", "6.11.2-alpha.1"},
    "@asyncapi/generator-helpers": {"1.1.1"},
    "@asyncapi/generator-components": {"0.7.1"},
}

IOC_NAMES = ("setup.mjs", "Math_Symbol.js", "math_init.js")

PKG_KEY_RE = re.compile(
    r"(?m)^ {2}('?)(@?[^'@\s][^']*?)@([^':\s]+)\1:\s*$"
)


def parse_pnpm_lock(path: Path) -> dict[str, set[str]]:
    text = path.read_text(encoding="utf-8", errors="replace")
    pkgs: dict[str, set[str]] = {}
    for m in PKG_KEY_RE.finditer(text):
        name, ver = m.group(2), m.group(3).split("(")[0]
        pkgs.setdefault(name, set()).add(ver)
    return pkgs


def main() -> int:
    locks = [
        p
        for p in list(ROOT.rglob("pnpm-lock.yaml"))
        + list(ROOT.rglob("package-lock.json"))
        + list(ROOT.rglob("yarn.lock"))
        if "node_modules" not in p.parts
    ]

    print(f"Root: {ROOT}")
    print(f"Lockfiles: {len(locks)}")
    for lf in locks:
        print(f"  - {lf.relative_to(ROOT)}")

    all_pkgs: dict[str, set[str]] = {}
    alerts: list[str] = []

    for lf in locks:
        if lf.name != "pnpm-lock.yaml":
            # package-lock / yarn: simple scan
            text = lf.read_text(encoding="utf-8", errors="replace")
            for name, bad_vers in COMPROMISED.items():
                for ver in bad_vers:
                    if f"{name}@{ver}" in text or f'"{name}": "{ver}"' in text:
                        alerts.append(f"{lf.relative_to(ROOT)}: {name}@{ver}")
            continue
        pkgs = parse_pnpm_lock(lf)
        for n, vers in pkgs.items():
            all_pkgs.setdefault(n, set()).update(vers)
            if n in COMPROMISED:
                hit = vers & COMPROMISED[n]
                for v in sorted(hit):
                    alerts.append(f"{lf.relative_to(ROOT)}: {n}@{v}")

    print("\n=== Exact compromised version hits ===")
    if alerts:
        for a in alerts:
            print(f"  ALERT: {a}")
    else:
        print("  none")

    print("\n=== Cache-family packages present (should be non-malicious versions) ===")
    for name in sorted(
        {
            "keyv",
            "flat-cache",
            "file-entry-cache",
            "cacheable",
            "cacheable-request",
            "cache-manager",
            "@cacheable/memory",
            "@cacheable/utils",
            "@cacheable/node-cache",
            "@cacheable/net",
            "ecto",
        }
    ):
        if name in all_pkgs:
            bad = COMPROMISED.get(name, set())
            versions = sorted(all_pkgs[name])
            marked = [v for v in versions if v in bad]
            status = f"COMPROMISED {marked}" if marked else "OK (not known-bad version)"
            print(f"  {name}: {versions}  — {status}")
        else:
            print(f"  {name}: (not present)")

    print("\n=== Malware IOC files ===")
    ioc_hits = []
    for name in IOC_NAMES:
        for p in ROOT.rglob(name):
            if ".git" in p.parts:
                continue
            ioc_hits.append(str(p.relative_to(ROOT)))
    if ioc_hits:
        for h in ioc_hits:
            print(f"  ALERT: {h}")
        alerts.extend(ioc_hits)
    else:
        print("  none (setup.mjs / Math_Symbol.js / math_init.js)")

    print("\n=== Scope notes ===")
    print("  - demo frontend + root: OpenTelemetry web SDK + vite (pnpm-lock.yaml)")
    print("  - third_party/instrumentation-js: eslint tooling pulls keyv/flat-cache (safe versions)")
    print("  - third_party/instrumentation-go + backend: Go modules only (not npm)")

    if alerts:
        print(f"\nFAILED: {len(alerts)} issue(s)")
        return 1
    print("\nPASSED: no known-compromised npm versions or IOC malware files found")
    return 0


if __name__ == "__main__":
    sys.exit(main())
