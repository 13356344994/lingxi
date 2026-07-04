#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""跨所强平引领研究(只读):Bybit 强平簇 → OKX 价格前向响应。

机制假设:同一币在 Bybit 被强平,套利者/做市商跨所调价 + 恐慌传染 → OKX 跟随。
关键分组:
  bybit_only = 簇时刻 ±60s OKX 无同币强平推送 → 纯跨所信号(OKX 爆仓频道使用者看不见);
  both       = 两所同时爆 → 传染确认。
对照:OKX 自己的强平簇(同口径)。
"""
import json
import datetime
import bisect
import collections
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


# ---- OKX 价格标尺(热币路径 + shadow base) ----
paths = collections.defaultdict(list)
with open(os.path.join(DIR, "fast_hot_mids.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            for ms, mid in d.get("backlog", []):
                paths[d["backlog_sym"]].append((datetime.datetime.utcfromtimestamp(ms / 1000), mid))
        else:
            t = ts(d["recorded_at"])
            for sym, mid in d.get("mids", {}).items():
                paths[sym].append((t, mid))
for s in paths:
    paths[s].sort()


def mid_at(sym, t, tol=40):
    arr = paths.get(sym)
    if not arr:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or (t - arr[i][0]).total_seconds() > tol:
        return None
    return arr[i][1]


def fwd(sym, t, m0, secs):
    arr = paths.get(sym)
    if not arr or not m0:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(arr) or (arr[i][0] - t).total_seconds() > secs + 40:
        return None
    return (arr[i][1] - m0) / m0 * 10000


# ---- 两所强平流 ----
def load_liqs(path, sym_key, side_key):
    out = collections.defaultdict(list)
    if not os.path.exists(path):
        return out
    with open(path) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            out[d[sym_key]].append((ts(d["recorded_at"]), d.get(side_key, "")))
    for s in out:
        out[s].sort()
    return out


by = load_liqs(os.path.join(DIR, "fast_xvenue_liq_events.jsonl"), "symbol", "pos_side")
ok = load_liqs(os.path.join(DIR, "fast_liquidation_events.jsonl"), "symbol", "pos_side")
# 只比较 hot_mids 覆盖期(07-04 01:52 UTC 起)
CUT = ts("2026-07-04T01:52:00")


def clusters(liqmap, min_n=2):
    out = []
    for sym, arr in liqmap.items():
        last = None
        for i, (t, ps) in enumerate(arr):
            if t < CUT:
                continue
            if last and (t - last).total_seconds() < 15:
                continue
            nl = ns = 0
            for tt, pp in arr[max(0, i - 200):i + 1]:
                if 0 <= (t - tt).total_seconds() <= 30:
                    nl += pp == "long"
                    ns += pp == "short"
            if nl + ns < min_n or nl == ns:
                continue
            out.append((t, sym, "long" if nl > ns else "short", nl + ns))
            last = t
    return out


bcl = clusters(by)
ocl = clusters(ok)
print(f"Bybit 簇: {len(bcl)} | OKX 簇(对照): {len(ocl)}(hot_mids 覆盖期内)")


def okx_liq_near(sym, t, win=60):
    arr = ok.get(sym, [])
    for tt, _ in arr:
        if abs((tt - t).total_seconds()) <= win:
            return True
    return False


def study(cl, tag, check_only=None):
    rows = []
    for t, sym, catch_side, n in cl:
        if check_only == "bybit_only" and okx_liq_near(sym, t):
            continue
        if check_only == "both" and not okx_liq_near(sym, t):
            continue
        m0 = mid_at(sym, t)
        if m0 is None:
            continue
        # 跟随方向:多头爆 → OKX 应跌(follow short 收益 = -fwd)
        sgn = -1 if catch_side == "long" else 1
        r = {}
        for w in (15, 30, 60, 180):
            f = fwd(sym, t, m0, w)
            r[w] = sgn * f if f is not None else None
        rows.append((sym, r, n))
    print(f"\n== {tag} (n={len(rows)}) ==")
    for w in (15, 30, 60, 180):
        v = [r[w] for _, r, _ in rows if r[w] is not None]
        if len(v) < 5:
            print(f"  +{w:3d}s n={len(v)}(太少)")
            continue
        pos = sum(1 for x in v if x > 0)
        print(f"  +{w:3d}s n={len(v):4d} 顺向率={pos/len(v)*100:3.0f}% 均值={sum(v)/len(v):+6.1f}bps 中位={sorted(v)[len(v)//2]:+6.1f}")
    syms = collections.Counter(s for s, _, _ in rows)
    if rows:
        print(f"  top币: {dict(syms.most_common(3))}")
    return rows


b_only = study(bcl, "Bybit-only 簇(±60s OKX 无同币强平)→ OKX 跟随", "bybit_only")
b_both = study(bcl, "两所同爆簇 → OKX 跟随", "both")
o_all = study(ocl, "OKX 自己的簇(对照)")

# bybit-only 大簇(n≥3)子集
big = [(t, s, sd, n) for t, s, sd, n in bcl if n >= 3]
study(big, "Bybit 簇 n≥3(不分组)")
