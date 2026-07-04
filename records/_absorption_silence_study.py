#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""夜间批次:头脑风暴②吸收率 + ③沉默钟(只读)。

② 吸收率(做市商库存逻辑):同样的瀑布,大量成交才砸出来=被动买盘厚,做市商吃了库存
   不得不回补(弹);一点量就砸穿=盘口真空(续)。度量=|位移|/成交量(冲击系数,分币标准化)。
③ 沉默钟(燃料逻辑):簇后 15s 无新强平=该死的死完了(弹);强平还在滴答=下一层还在被啃(续)。
决策时点统一为 簇+15s(信息集对齐:两个信号在该时刻均可实时计算),前向从决策时点起算。
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


# ---- 价格路径(热币) ----
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


def mid_at(sym, t, tol=30):
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
    if i >= len(arr) or (arr[i][0] - t).total_seconds() > secs + 30:
        return None
    return (arr[i][1] - m0) / m0 * 10000


# ---- 热币微结构(吸收率原料;只取需要的字段,流式) ----
micro = collections.defaultdict(list)  # sym -> [(t, sell1s, buy1s, refill_bid, refill_ask)]
with open(os.path.join(DIR, "fast_hot_micro.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        micro[d["symbol"]].append((ts(d["recorded_at"]), d.get("trade_sell_qty_1s", 0), d.get("trade_buy_qty_1s", 0),
                                   d.get("liquidity_refill_bid", 0), d.get("liquidity_refill_ask", 0)))
for s in micro:
    micro[s].sort()

# ---- OKX 强平流 → 簇(与实盘同口径) ----
liqs = collections.defaultdict(list)
liq_flat = []
with open(os.path.join(DIR, "fast_liquidation_events.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        t = ts(d["recorded_at"])
        liqs[d["symbol"]].append((t, d.get("pos_side", "")))
        liq_flat.append((d["symbol"], t))
liq_flat.sort(key=lambda x: x[1])

clusters = []
for sym, arr in liqs.items():
    arr.sort()
    last = None
    for i, (t, ps) in enumerate(arr):
        if last and (t - last).total_seconds() < 15:
            continue
        nl = ns = 0
        for tt, pp in arr[max(0, i - 200):i + 1]:
            if 0 <= (t - tt).total_seconds() <= 30:
                nl += pp == "long"
                ns += pp == "short"
        if nl + ns < 2 or nl == ns:
            continue
        clusters.append((t, sym, "long" if nl > ns else "short"))
        last = t

rows = []
for t, sym, catch in clusters:
    if sym not in micro:
        continue
    decision_t = t + datetime.timedelta(seconds=15)
    m_dec = mid_at(sym, decision_t)
    m0 = mid_at(sym, t)
    if m_dec is None or m0 is None:
        continue
    # ② 吸收率:簇后 15s 内,被迫方向成交量 vs 位移
    marr = micro[sym]
    mkeys = [x[0] for x in marr]
    lo = bisect.bisect_left(mkeys, t)
    hi = bisect.bisect_left(mkeys, decision_t)
    vol = refill = 0.0
    npt = 0
    for i in range(lo, hi):
        _, sell, buy, rb, ra = marr[i]
        vol += sell if catch == "long" else buy
        refill += rb if catch == "long" else ra
        npt += 1
    if npt < 5 or vol <= 0:
        continue
    move = abs((m_dec - m0) / m0 * 10000)
    impact = move / vol  # 冲击系数(跨币不可比,后面用分币分位数)
    # ③ 沉默钟:决策时点回看,簇最新爆仓(=t)之后到 decision_t 有没有新强平
    silent = True
    for tt, _ in liqs[sym]:
        if t < tt <= decision_t:
            silent = False
            break
    # 前向(从决策时点,顺被迫流方向=follow)
    sgn = -1 if catch == "long" else 1
    f60 = fwd(sym, decision_t, m_dec, 60)
    f180 = fwd(sym, decision_t, m_dec, 180)
    if f60 is None:
        continue
    rows.append(dict(sym=sym, t=t, catch=catch, impact=impact, vol=vol, refill=refill / max(npt, 1),
                     silent=silent, fol60=sgn * f60, fol180=(sgn * f180 if f180 is not None else None)))

print(f"可解剖簇(有微结构+路径覆盖): {len(rows)}")

# 冲击系数分币归一(每币中位为界:低冲击=吸收好)
by_sym_imp = collections.defaultdict(list)
for r in rows:
    by_sym_imp[r["sym"]].append(r["impact"])
med_imp = {s: sorted(v)[len(v) // 2] for s, v in by_sym_imp.items()}
for r in rows:
    r["absorbed"] = r["impact"] <= med_imp[r["sym"]]  # 低冲击=有人接=吸收好


def rep(tag, sub):
    v = [r["fol60"] for r in sub]
    if len(v) < 8:
        print(f"  {tag:44s} n={len(v)}(太少)")
        return
    v180 = [r["fol180"] for r in sub if r["fol180"] is not None]
    pos = sum(1 for x in v if x > 0)
    m = sorted(v)[len(v) // 2]
    syms = collections.Counter(r["sym"] for r in sub)
    l180 = ""
    if v180:
        l180 = f" | 180s均={sum(v180)/len(v180):+6.1f} 中={sorted(v180)[len(v180)//2]:+6.1f}"
    print(f"  {tag:44s} n={len(v):4d} 顺流60s: 延续率={pos/len(v)*100:3.0f}% 均={sum(v)/len(v):+6.1f} 中={m:+6.1f}{l180} top:{dict(syms.most_common(2))}")


print("\n== ② 吸收率(决策时点=簇+15s,方向=顺被迫流;延续率高=跟,低=接) ==")
rep("吸收差(高冲击,真空)→ 预测延续", [r for r in rows if not r["absorbed"]])
rep("吸收好(低冲击,有人接)→ 预测回弹", [r for r in rows if r["absorbed"]])

print("\n== ③ 沉默钟 ==")
rep("15s 内仍有新强平(燃料未尽)→ 预测延续", [r for r in rows if not r["silent"]])
rep("15s 沉默(该死的死完了)→ 预测回弹", [r for r in rows if r["silent"]])

print("\n== ②×③ 组合(四象限) ==")
rep("真空×还在爆(最强跟)", [r for r in rows if not r["absorbed"] and not r["silent"]])
rep("真空×沉默", [r for r in rows if not r["absorbed"] and r["silent"]])
rep("吸收×还在爆", [r for r in rows if r["absorbed"] and not r["silent"]])
rep("吸收×沉默(最强接)", [r for r in rows if r["absorbed"] and r["silent"]])
