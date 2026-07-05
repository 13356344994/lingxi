#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""高波动币漏斗诊断(只读;07-05 用户质询"波动大的币特别多,为何只有NES触发三重奏")。

对当日暴动次数 top 币逐个回答:它在我们管线的哪一关被漏掉/挡掉?
  关1 覆盖:是否进过 hot_micro(动态热币订阅上限40,不在=完全不可见);
  关2 方向读数:活跃秒里有多少成为决策时刻(burst或|r15|>=5);
  关3 OI 仪表:oi_now>0 的秒占比(无OI数据=三重奏永不可能触发);
  关4 OI 骤降:60s<=-1% 的决策时刻数(波动大但OI不降=非杠杆瓦解型波动);
  关5 跨所锚:有锚读数的决策时刻占比(未跨所上市=锚价差条件结构性不可满足);
  关6 三重奏命中(episode 300s 去重)。
用法:python3 _volatile_coin_funnel.py [SINCE] [TOPN]
"""
import json
import sys
import bisect
import collections
import datetime
import os

DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "okx_directional")
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-05T01:45"
TOPN = int(sys.argv[2]) if len(sys.argv) > 2 else 15


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


def tsf(s):
    return datetime.datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f")


# ---- 普查:当日暴动榜 ----
riot = collections.Counter()
riot_max = collections.defaultdict(float)
for line in open(DIR + "/fast_mover_census.jsonl"):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    if r.get("recorded_at", "") < SINCE:
        continue
    riot[r["symbol"]] += 1
    riot_max[r["symbol"]] = max(riot_max[r["symbol"]], abs(r.get("ret_60s_bps", 0) or 0))
top = [s for s, _ in riot.most_common(TOPN)]

# ---- 价格路径 + 锚(与 oos 同源)----
paths = collections.defaultdict(list)
anch = collections.defaultdict(lambda: collections.defaultdict(list))
with open(DIR + "/fast_hot_mids.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d["recorded_at"] < SINCE and not d.get("backlog_sym"):
            continue
        if d.get("backlog_sym"):
            tgt = anch[d["backlog_venue"]][d["backlog_sym"]] if d.get("backlog_venue") else paths[d["backlog_sym"]]
            for ms, mid in d.get("backlog", []):
                tgt.append((datetime.datetime.utcfromtimestamp(ms / 1000), mid))
            continue
        t = ts(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((t, mid))
        for v, m in (d.get("anchors") or {}).items():
            for sym, val in m.items():
                anch[v][sym].append((datetime.datetime.utcfromtimestamp(val[1] / 1000), val[0]))
for s in paths:
    paths[s] = sorted(set(paths[s]))
for v in anch:
    for s in anch[v]:
        anch[v][s] = sorted(set(anch[v][s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def series_at(arr, keys, t, tol):
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or (t - arr[i][0]).total_seconds() > tol:
        return None
    return arr[i][1]


# ---- 逐秒漏斗统计 ----
Z = lambda: collections.defaultdict(int)
secs, mom, oiav, oihit, gapav, gaphit, imbhit, trio = Z(), Z(), Z(), Z(), Z(), Z(), Z(), Z()
trio_last = {}
trio_epi = Z()
state_vol = collections.defaultdict(lambda: collections.deque(maxlen=60))
nscan = 0
for fn in (DIR + "/fast_hot_micro.jsonl.1", DIR + "/fast_hot_micro.jsonl"):
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            i = line.find('"recorded_at":"')
            if i < 0 or line[i + 15:i + 34] < SINCE:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            nscan += 1
            if nscan % 3 != 0:
                continue
            sym = d["symbol"]
            secs[sym] += 1
            t = tsf(d["recorded_at"])
            mid = d.get("mid", 0)
            if mid <= 0:
                continue
            b, s_ = d.get("trade_buy_qty_1s", 0), d.get("trade_sell_qty_1s", 0)
            tot = b + s_
            hist = state_vol[sym]
            base = sorted(hist) if hist else []
            med = base[len(base) // 2] if base else 0
            hist.append(tot)
            m15 = series_at(paths[sym], pk[sym], t - datetime.timedelta(seconds=15), 30) if sym in paths else None
            r15 = (mid - m15) / m15 * 10000 if m15 else 0
            burst = med > 0 and tot >= med * 5 and tot * mid >= 500
            if burst:
                side = 1 if b > s_ else -1
            elif abs(r15) >= 5:
                side = 1 if r15 > 0 else -1
            else:
                continue
            if d.get("trade_count_1s", 0) == 0 and not burst:
                continue
            mom[sym] += 1
            oi_now = d.get("oi_now", 0)
            oi_d = d.get("oi_delta_60s", 0)
            if oi_now > 0:
                oiav[sym] += 1
            c_oi = oi_now > 0 and oi_d < -0.01 * oi_now
            if c_oi:
                oihit[sym] += 1
            imb = d.get("book_imbalance_now", 0)
            c_imb = imb * side > 0.5
            if c_imb:
                imbhit[sym] += 1
            gaps = []
            for v in ("bybit", "binance", "gate"):
                a = anch[v].get(sym)
                if a:
                    av = series_at(a, [x[0] for x in a], t, 10)
                    if av:
                        gaps.append((mid - av) / av * 10000)
            gap = sorted(gaps)[len(gaps) // 2] if gaps else None
            if gap is not None:
                gapav[sym] += 1
            c_gap = gap is not None and gap * side >= 10
            if c_gap:
                gaphit[sym] += 1
            if c_imb and c_oi and c_gap:
                trio[sym] += 1
                t0 = trio_last.get(sym)
                if t0 is None or (t - t0).total_seconds() >= 300:
                    trio_epi[sym] += 1
                    trio_last[sym] = t

print("窗口起点(UTC) %s | 暴动币总数=%d | 其中进过hot_micro覆盖=%d" % (
    SINCE, len(riot), sum(1 for s in riot if secs.get(s, 0) > 0)))
print("\n%-14s %6s %7s | %6s %6s | %5s %5s | %5s %5s | %4s %4s" % (
    "币(按暴动数)", "暴动", "max60s", "活跃秒", "决策秒", "OI有", "OI降", "锚有", "锚同", "trio", "epi"))
for s in top:
    print("%-14s %6d %6.0f  | %6d %6d | %4.0f%% %5d | %4.0f%% %5d | %4d %4d" % (
        s, riot[s], riot_max[s], secs.get(s, 0), mom.get(s, 0),
        100.0 * oiav.get(s, 0) / max(1, mom.get(s, 0)), oihit.get(s, 0),
        100.0 * gapav.get(s, 0) / max(1, mom.get(s, 0)), gaphit.get(s, 0),
        trio.get(s, 0), trio_epi.get(s, 0)))
