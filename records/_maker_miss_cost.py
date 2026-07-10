#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""maker 未成交机会成本入账(2026-07-10 用户令三;66% 未成交是这把枪真瓶颈;只读)。

对账三件套的第②件:入场滑点(已在结算)+**未成交赢单机会成本**(本脚本)+止损穿线(已量化)。
方法:fast_maker_attempts.jsonl 里 maker_filled=false 的挂单尝试 → 用 market_mids 30s 网格反事实,
若按挂单价成交、跑该枪出场规则(固定30m/-150止损),错过的净 bps 是多少。
正=漏掉赢单(机会成本);负=挂单未成反而躲亏(有利偏差)。按 source 分列。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

miss = []
with open(f"{REC}/fast_maker_attempts.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        sr = str(r.get("score_reason", ""))
        if r.get("maker_filled") or not (sr.startswith("key_casc") or sr.startswith("key_sqz")):
            continue
        src = "key_casc_fade" if sr.startswith("key_casc") else "key_sqz_fade"
        miss.append(dict(sym=r["symbol"], t0=ts(r["recorded_at"]), px=r.get("maker_order_price", 0),
                         side=r.get("side", ""), src=src))
print(f"maker 未成交尝试(key 两源)={len(miss)}")
if not miss:
    raise SystemExit(0)

need = {}
for m in miss:
    need.setdefault(m["sym"], []).append(m["t0"])
for v in need.values():
    v.sort()
paths = {(m["sym"], m["t0"]): [] for m in miss}
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        row = None
        for sym, t0s in need.items():
            j = bisect.bisect_right(t0s, t) - 1
            if j >= 0 and t0s[j] <= t <= t0s[j] + 1900:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                mm = row.get(sym)
                if mm:
                    paths[(sym, t0s[j])].append((t, mm))

def simulate(m):
    pts = sorted(paths[(m["sym"], m["t0"])])
    pts = [(t, x) for t, x in pts if t >= m["t0"]]
    if len(pts) < 3 or m["px"] <= 0:
        return None
    ent = m["px"]
    sgn = 1 if m["side"] == "long" else -1
    px = None
    for t, x in pts:
        u = sgn * (x / ent - 1) * 1e4
        if t - m["t0"] >= 1800 or u <= -150:
            px = x
            break
        px = x
    if px is None:
        return None
    return sgn * (px / ent - 1) * 1e4 - COST

by = {"key_casc_fade": [], "key_sqz_fade": []}
for m in miss:
    r = simulate(m)
    if r is not None:
        by[m["src"]].append(r)
print("\n未成交单反事实结局(正=漏掉赢单=机会成本;负=躲掉亏单=有利偏差):")
for src, v in by.items():
    if len(v) < 5:
        print(f"  {src}: n={len(v)}(不足)")
        continue
    print(f"  {src}: n={len(v)} 错过净中位{statistics.median(v):+.1f}bps 均{sum(v)/len(v):+.1f} "
          f"合计{sum(v):+.0f}bps 漏赢单率{sum(1 for x in v if x>0)/len(v)*100:.0f}%")
allv = by["key_casc_fade"] + by["key_sqz_fade"]
if allv:
    print(f"\n合计机会成本:未成交 {len(allv)} 单反事实净合计 {sum(allv):+.0f}bps 均 {sum(allv)/len(allv):+.1f}bps/单")
    print("入账口径:此为对账三件套第②件;成交单入场滑点(结算 entry_slip_bps)+止损穿线(结算 reason=stop)已在账本。")
