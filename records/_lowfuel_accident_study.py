#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""低燃料"接事故"研究(只读):暴动(|60s|≥25bps)之后,反向接的前向收益。

对手模型:低燃料期的暴动=流动性事故(孤立强平/大单砸穿薄盘),没有后续人群接力 → 弹回;
高燃料期的暴动=传染的一环 → 延续,接=接刀。
判据:反向接的收益 = -sign(ret60) × 未来位移(bps),正=弹回可接。
执行现实:接用 maker 挂单有成交即证伪风险(fade 教训),故按 taker 口径算,需 >12bps 才过成本。

数据:fast_mover_census.jsonl(事件)× fast_hot_mids.jsonl(前向路径)。
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


# ---- 热币路径(快照行 + backlog 行) ----
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

def fwd(sym, t, m0, secs, tol=30):
    arr = paths.get(sym)
    if not arr or not m0:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(arr) or (arr[i][0] - t).total_seconds() > secs + tol:
        return None
    return (arr[i][1] - m0) / m0 * 10000


# ---- 普查事件 ----
rows = []
with open(os.path.join(DIR, "fast_mover_census.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        t = ts(d["recorded_at"])
        r60 = d["ret_60s_bps"]
        sgn = 1 if r60 > 0 else -1
        f60 = fwd(d["symbol"], t, d["mid"], 60)
        f180 = fwd(d["symbol"], t, d["mid"], 180)
        if f60 is None:
            continue
        rows.append(dict(
            sym=d["symbol"], t=t, r60=r60, spread=d.get("spread_bps", 99),
            fuel=d.get("market_liq_5m", 0), symliq=d.get("sym_liq_5m", 0),
            xliq=d.get("xliq_n_5m", 0), r15=d.get("ret_15s_bps"), r30=d.get("ret_30s_bps"),
            rev60=-sgn * f60, rev180=(-sgn * f180 if f180 is not None else None)))

print(f"可算前向的暴动事件: {len(rows)}(总普查 {sum(1 for _ in open(os.path.join(DIR,'fast_mover_census.jsonl')))})")


def rep(tag, sub, show_syms=True):
    v = [r["rev60"] for r in sub]
    if len(v) < 5:
        print(f"  {tag:38s} n={len(v)}(太少)")
        return
    v180 = [r["rev180"] for r in sub if r["rev180"] is not None]
    pos = sum(1 for x in v if x > 0)
    m = sorted(v)[len(v) // 2]
    syms = collections.Counter(r["sym"] for r in sub)
    top = f" top:{dict(syms.most_common(2))}" if show_syms else ""
    l180 = ""
    if v180:
        m180 = sorted(v180)[len(v180) // 2]
        l180 = f" | 180s均={sum(v180)/len(v180):+6.1f} 中={m180:+6.1f}"
    print(f"  {tag:38s} n={len(v):5d} 反向60s: 弹回率={pos/len(v)*100:3.0f}% 均={sum(v)/len(v):+6.1f} 中={m:+6.1f}{l180}{top}")


# 全体基线
exe = [r for r in rows if r["spread"] <= 15]
print("\n== 基线(点差≤15bps 可执行子集) ==")
rep("全部暴动·反向接", exe)

print("\n== 按燃料 regime ==")
rep("死水(燃料<20)", [r for r in exe if r["fuel"] < 20])
rep("中间(20-40)", [r for r in exe if 20 <= r["fuel"] < 40])
rep("活跃(≥40)", [r for r in exe if r["fuel"] >= 40])

print("\n== 死水 × 有无本币强平(事故 vs 强平驱动) ==")
dead = [r for r in exe if r["fuel"] < 20]
rep("死水·零强平(纯事故)", [r for r in dead if r["symliq"] == 0 and r["xliq"] == 0])
rep("死水·有强平(单所或跨所)", [r for r in dead if r["symliq"] > 0 or r["xliq"] > 0])

print("\n== 死水 × 形状(15s占60s位移比;减速=事故完成,加速=还在进行) ==")
shaped = [r for r in dead if r["r15"] is not None and r["r60"] != 0]
for lo, hi, tag in [(0, 0.2, "已停(|r15|<20%)"), (0.2, 0.5, "减速中(20-50%)"), (0.5, 9, "仍在加速(>50%)")]:
    rep(f"死水·{tag}", [r for r in shaped if lo <= abs((r['r15'] or 0) / r['r60']) < hi])

print("\n== 死水 × 幅度 ==")
rep("死水·25-40bps", [r for r in dead if abs(r["r60"]) < 40])
rep("死水·40-80bps", [r for r in dead if 40 <= abs(r["r60"]) < 80])
rep("死水·≥80bps", [r for r in dead if abs(r["r60"]) >= 80])

# 最优复合桶(如果有):死水×已停×中大幅度,剔除top币看稳健性
best = [r for r in shaped if abs((r["r15"] or 0) / r["r60"]) < 0.2 and abs(r["r60"]) >= 40]
print("\n== 候选复合桶:死水·已停·≥40bps ==")
rep("复合桶", best)
if best:
    top = collections.Counter(r["sym"] for r in best).most_common(1)[0][0]
    rep(f"复合桶·剔除{top}", [r for r in best if r["sym"] != top])
    # 分时段稳定性
    lo_t = min(r["t"] for r in best)
    half = lo_t + (max(r["t"] for r in best) - lo_t) / 2
    rep("复合桶·前半", [r for r in best if r["t"] < half], show_syms=False)
    rep("复合桶·后半", [r for r in best if r["t"] >= half], show_syms=False)
