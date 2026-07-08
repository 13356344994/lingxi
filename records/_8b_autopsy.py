#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""#8b 资费后崩·三问尸检(判负例行处置;迷宫循环:失败照亮哪些相邻格)。

判负事实:n=73,做空净中位+2.0/PF0.95;跨日翻向(07-07 post5中位-15.3 → 07-08 +9.5)。
三问:①哪部分真?②哪部分假?③假的预示什么?
尸检轴=州(结算时刻的燃料/潮汐/广度)——假设:后崩是风暴州现象,平静州收款者不慌着卖。
"""
import json
import bisect
import calendar
import collections
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = "2026-07-07T08:25:00"
F_EXTREME = -0.0005


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
liq_ts, liq_long, liq_short = [], [], []
with open(REC + "/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
            t = ep(d["recorded_at"])
        except (ValueError, KeyError):
            continue
        if t < t_since - 1800:
            continue
        liq_ts.append(t)
        if d.get("pos_side") == "long":
            liq_long.append(t)
        elif d.get("pos_side") == "short":
            liq_short.append(t)
liq_ts.sort(); liq_long.sort(); liq_short.sort()


def cnt(arr, lo, hi):
    return bisect.bisect_right(arr, hi) - bisect.bisect_left(arr, lo)


ev = {}
for line in open(REC + "/fast_xmeta_snapshots.jsonl"):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    T = ep(d["recorded_at"])
    if T < t_since - 1800:
        continue
    for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
        if isinstance(v, list) and len(v) >= 3 and v[2] > 0 and float(v[0]) <= F_EXTREME:
            st = int(v[2]) / 1000.0
            if st > T and st - T <= 1800 and st >= t_since:
                ev[(sym, st)] = float(v[0])

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
        if t < t_since - 600:
            continue
        b = int(t // 300)
        if b not in grid:
            try:
                grid[b] = json.loads(line).get("mids", {})
            except ValueError:
                continue


def px(sym, t):
    for b in (int(t // 300), int(t // 300) - 1):
        if b in grid:
            m = grid[b].get(sym)
            if m:
                return m
    return None


rows = []
for (sym, st), f in ev.items():
    p0, p5 = px(sym, st), px(sym, st + 300)
    if not (p0 and p5):
        continue
    fuel = cnt(liq_ts, st - 300, st)
    ebb = cnt(liq_long, st - 1800, st)
    flood = cnt(liq_short, st - 1800, st)
    tide = "ebb" if (ebb >= 2 * max(flood, 1) and ebb >= 5) else ("flood" if (flood >= 2 * max(ebb, 1) and flood >= 5) else "mixed")
    rows.append({"st": st, "sym": sym, "post5": (p5 - p0) / p0 * 1e4, "fuel": fuel, "tide": tide})
print("#8b 尸检:结算事件=%d(封样后)" % len(rows))


def stat(sel, lab):
    if len(sel) < 8:
        print("  %-24s n=%d 不足" % (lab, len(sel)))
        return
    v = [r["post5"] for r in sel]
    dn = 100 * sum(1 for x in v if x < 0) / len(v)
    print("  %-24s n=%-3d post5中位=%+7.1f 向下率=%3.0f%%(做空净中位=%+.1f)" % (
        lab, len(sel), statistics.median(v), dn, -statistics.median(v) - 12))


print("== 按州分格(重组假设:#8b×风暴州) ==")
stat([r for r in rows if r["fuel"] >= 40], "风暴(fuel≥40)")
stat([r for r in rows if r["fuel"] < 40], "平静(fuel<40)")
stat([r for r in rows if r["tide"] == "ebb"], "跌潮")
stat([r for r in rows if r["fuel"] >= 40 and r["tide"] == "ebb"], "风暴×跌潮")
stat([r for r in rows if r["fuel"] < 20], "死水(fuel<20)")
print("== 按日(跨日翻向的州解释检验) ==")
byday = collections.defaultdict(list)
for r in rows:
    byday[datetime.utcfromtimestamp(r["st"]).strftime("%m-%d")].append(r)
for day in sorted(byday):
    sel = byday[day]
    storm_pct = 100 * sum(1 for r in sel if r["fuel"] >= 40) / len(sel)
    stat(sel, "%s(风暴事件占%.0f%%)" % (day, storm_pct))
print("(尸检不判决;若风暴格显著更肥→登记'#8b×风暴州'复合假设入织网,走预注册。)")
