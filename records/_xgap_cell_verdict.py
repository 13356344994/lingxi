#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「E×平静海」格子发证考试 判决脚本(预注册 2026-07-07 17:30 BJ,HANDOFF ㉙;口径冻结不许改)。

格子 = E-v2 规则 × duty=xgap:
  E-v2:OKX比Bybit锚(新鲜≤8s)便宜≥40bps + 新鲜度(回看1800s价差须现过>-20,可算点<10 fail-closed)
       → 买OKX;收敛≤10 / 600s / -60灾难;同币冷却1800s;成本12。
  duty=xgap(值班表v0.1冻结):非风暴(fuel_5m<40)且 广度30m中位>-15(广度不可算=fail-closed跳过)。
判决:封样(2026-07-07 09:30Z)后新样本 n≥30 且 净中位>0 且 净PF≥1.3 且 胜率(刮痕中性化)≥45%
     且 top币≤20% → 格子持证,连同影子线执行层证据(gate_skip 分布)递用户裁探针。
用法:python3 _xgap_cell_verdict.py [SINCE_ISO(默认封样时刻)]
"""
import json
import bisect
import calendar
import collections
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T09:30:00"
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
load_floor = t_since - 2100  # 新鲜度回看1800s+锚容忍

# ---- 官方爆仓(燃料/潮汐) ----
liq_ts, liq_long, liq_short = [], [], []
with open(REC + "/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
            t = ep(d["recorded_at"])
        except (ValueError, KeyError):
            continue
        if t < load_floor - 1800:
            continue
        liq_ts.append(t)
        if d.get("pos_side") == "long":
            liq_long.append(t)
        elif d.get("pos_side") == "short":
            liq_short.append(t)
liq_ts.sort(); liq_long.sort(); liq_short.sort()


def cnt(arr, lo, hi):
    return bisect.bisect_right(arr, hi) - bisect.bisect_left(arr, lo)


# ---- 广度(market_mids 5min 网格) ----
grid = {}
with open(REC + "/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ep(line[p + 15:p + 45].split('"')[0])
        except (ValueError, IndexError):
            continue
        if t < load_floor - 1900:
            continue
        b = int(t // 300)
        if b in grid:
            continue
        try:
            grid[b] = json.loads(line).get("mids", {})
        except ValueError:
            continue
breadth = {}
for b in sorted(grid):
    old = grid.get(b - 6)
    if not old:
        continue
    m = grid[b]
    rets = [(m[s] - old[s]) / old[s] * 1e4 for s in m if s in old and old[s] > 0]
    if len(rets) >= 100:
        breadth[b] = statistics.median(rets)


def duty_is_xgap(t):
    if cnt(liq_ts, t - 300, t) >= 40:
        return False  # 风暴(不论潮汐,都不是E的班)
    br = breadth.get(int(t // 300)) or breadth.get(int(t // 300) - 1)
    return br is not None and br > -15


# ---- 热币路径 + Bybit 锚 ----
paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 45].split('"')[0]) < load_floor:
                    continue
            except (ValueError, IndexError):
                pass
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            v = d.get("backlog_venue")
            tgt = anch[d["backlog_sym"]] if v == "bybit" else (paths[d["backlog_sym"]] if not v else None)
            if tgt is not None:
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= load_floor - 600:
                        tgt.append((pt[0] / 1000.0, pt[1]))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((T, mid))
        for sym, val in (d.get("anchors") or {}).get("bybit", {}).items():
            if isinstance(val, (list, tuple)) and len(val) >= 2:
                anch[sym].append((val[1] / 1000.0, val[0]))
for s in paths:
    paths[s] = sorted(set(paths[s]))
for s in anch:
    anch[s] = sorted(set(anch[s]))
ak = {s: [x[0] for x in arr] for s, arr in anch.items()}
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def a_at(sym, t, tol=8):
    arr = anch.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(ak[sym], t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


def gap_fresh(sym, t_in):
    arr = paths.get(sym)
    lo = bisect.bisect_left(pk[sym], t_in - 1800)
    hi = bisect.bisect_left(pk[sym], t_in)
    n_ok, seen = 0, False
    for i in range(lo, hi):
        T, okx = arr[i]
        by = a_at(sym, T)
        if not by or by <= 0:
            continue
        n_ok += 1
        if (okx - by) / by * 1e4 > -20:
            seen = True
    return n_ok >= 10 and seen


trades, last = [], {}
skipped = collections.Counter()
for sym, arr in paths.items():
    if sym not in anch or len(anch[sym]) < 10:
        continue
    n = len(arr)
    j = 0
    while j < n:
        T, okx = arr[j]
        if T < t_since or (sym in last and T - last[sym] < 1800):
            j += 1
            continue
        by = a_at(sym, T)
        if by and by > 0 and (okx - by) / by * 1e4 <= -40:
            if not gap_fresh(sym, T):
                skipped["stale_gap"] += 1
                last[sym] = T
                j += 1
                continue
            if not duty_is_xgap(T):
                skipped["off_duty"] += 1
                last[sym] = T
                j += 1
                continue
            entry, t_in = okx, T
            out = None
            for k in range(j + 1, n):
                T2, okx2 = arr[k]
                if T2 - t_in > 600:
                    break
                by2 = a_at(sym, T2)
                ret = (okx2 - entry) / entry * 1e4
                out = ret
                if ret <= -60:
                    break
                if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
                    break
            if out is not None:
                trades.append((t_in, sym, out - COST))
                last[sym] = t_in
        j += 1

v = [x[2] for x in trades]
print("E×平静海格子 判决集 since %s | n=%d | 拦截=%s" % (SINCE, len(v), dict(skipped)))
if v:
    wins = [x for x in v if x > 5]
    losses = [x for x in v if x < -5]
    wr = 100 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    pf = pw / pl if pl > 0 else 99
    c = collections.Counter(x[1] for x in trades)
    top, tn = c.most_common(1)[0]
    med = statistics.median(v)
    print("净中位=%+.1f 净均=%+.1f 胜(中性)=%.0f%% 净PF=%.2f top=%s:%d(%d%%)" % (
        med, sum(v) / len(v), wr, pf, top.replace("-USDT", ""), tn, 100 * tn // len(v)))
    if len(v) >= 30:
        ok = med > 0 and pf >= 1.3 and wr >= 45 and tn / len(v) <= 0.20
        print("【到线】格子终审:%s" % ("持证 → 连同影子执行层证据递用户裁探针" if ok else "不过 → 三问解剖"))
    else:
        print("进度 %d/30" % len(v))
