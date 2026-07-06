#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Bybit 确认源价值研究(2026-07-06 延迟解剖后):OKX 官方确认到达中位 1225ms(频道节流,不可减);
Bybit allLiquidation 若同币更快到达,接入"任一所确认即放行"可合法砍掉大段等待。
对同一批已确认 tape_burst 枪:比较 OKX 确认到达 vs Bybit 同币首笔爆仓到达(都从 burst t0 起算)。
"""
import json
import os
import bisect
import statistics
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


def load(fn, symkey="symbol"):
    d = {}
    with open(os.path.join(DIR, fn)) as f:
        for line in f:
            try:
                r = json.loads(line)
                d.setdefault(r[symkey], []).append(tsu(r["recorded_at"]))
            except Exception:
                continue
    for v in d.values():
        v.sort()
    return d


okx = load("fast_liquidation_events.jsonl")
byb = load("fast_xvenue_liq_events.jsonl")  # symbol 形如 TRB-USDT(无 -SWAP)

rows = []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst" or not r.get("event_age_ms"):
            continue
        t0 = tsu(r["recorded_at"]) - timedelta(milliseconds=r["event_age_ms"])
        sym = r["symbol"]

        def first_after(arr):
            i = bisect.bisect_right(arr, t0)
            if i < len(arr) and (arr[i] - t0).total_seconds() <= 3.5:
                return (arr[i] - t0).total_seconds() * 1000.0
            return None

        o = first_after(okx.get(sym, []))
        b = first_after(byb.get(sym.replace("-SWAP", ""), []))
        rows.append((sym, o, b))

n = len(rows)
both = [r for r in rows if r[1] is not None and r[2] is not None]
only_o = [r for r in rows if r[1] is not None and r[2] is None]
only_b = [r for r in rows if r[1] is None and r[2] is not None]
print("tape_burst 枪(3.5s窗内)=%d  OKX确认=%d  Bybit可确认=%d  双所=%d  仅OKX=%d  仅Bybit(新增猎物)=%d" % (
    n, len([r for r in rows if r[1] is not None]), len([r for r in rows if r[2] is not None]),
    len(both), len(only_o), len(only_b)))

if both:
    om = statistics.median([r[1] for r in both])
    bm = statistics.median([r[2] for r in both])
    d = [r[1] - r[2] for r in both]
    faster = len([x for x in d if x > 0])
    print("\n== 双所都确认的枪(n=%d)==" % len(both))
    print("OKX 确认到达中位 = %.0fms" % om)
    print("Bybit 确认到达中位 = %.0fms" % bm)
    print("Bybit 更快的枪占比 = %.0f%%  提前量中位 = %+.0fms" % (100.0 * faster / len(both), statistics.median(d)))
    # 任一所先到 = min(o,b)
    first = [min(r[1], r[2]) for r in both] + [r[1] for r in only_o] + [r[2] for r in only_b]
    cur = [r[1] for r in both] + [r[1] for r in only_o]
    print("\n== 改造后确认延迟(任一所先到)vs 现状(仅OKX)==")
    print("现状(仅OKX)   中位 = %.0fms (n=%d)" % (statistics.median(cur), len(cur)))
    print("任一所先到     中位 = %.0fms (n=%d,含仅Bybit新增%d枪)" % (statistics.median(first), len(first), len(only_b)))
