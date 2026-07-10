#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""规则洞→方向:cap 钉死资费当方向信号(2026-07-10 用户令;只读,当场出分)。

被迫流问题:资费钉死 cap(|f|≥0.95%)时,拥挤侧每期被规则强制付满 1% **却不走**——
付钱留下=最强方向表态;规则不让资费涨到出清水平=拥挤度被钳位低估。
方向假设(每条只答"被迫方朝哪个方向推价"):
  H1 顺拥挤:f 钉负(空头拥挤付租)→ 做空;f 钉正 → 做多。入场=钉死结算时刻,持 1 期/4h/24h。
  H2 骑段:段首(首个钉死结算)进场顺拥挤,段末(首个非钉死结算)离场。
  H3 解除反转:段末时刻反向进场(被迫付租流消失=压力释放),持 4h/24h。
数据:okx_funding 3 个月 × okx_1h K线(2年,401币);成本 12bps;止损无(1h 粒度,报 MAE)。
train/holdout:holdout=最后 14 天;单币主导报告。
"""
import json, os, bisect, statistics
from datetime import datetime, timezone

H = "/root/lingxi/records/history"
COST = 12.0
PIN = 0.0095

# 1H K线
coins = {}
for fn in sorted(os.listdir(f"{H}/okx_1h")):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    rows = []
    with open(f"{H}/okx_1h/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
                rows.append((int(x[0]) / 1000.0, float(x[4])))
            except Exception:
                continue
    rows = sorted(set(rows))
    if rows:
        coins[sym] = rows
tsidx = {s: [x[0] for x in c] for s, c in coins.items()}
print(f"1H 币数={len(coins)}")

# 资费史 → 钉死结算与段
events = []   # 每个钉死结算 (sym, t, f)
segs = []     # (sym, t_start, t_end_settle, f_sign, periods) t_end=首个非钉死结算时刻
for fn in os.listdir(f"{H}/okx_funding"):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    if sym not in coins:
        continue
    rows = []
    with open(f"{H}/okx_funding/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
                fr = float(x.get("realizedRate") or x.get("fundingRate") or 0)
                ft = int(x.get("fundingTime", 0) or 0) / 1000.0
                if ft:
                    rows.append((ft, fr))
            except Exception:
                continue
    rows = sorted(set(rows))
    i = 0
    while i < len(rows):
        if abs(rows[i][1]) >= PIN:
            j = i
            while j < len(rows) and abs(rows[j][1]) >= PIN and (rows[j][1] > 0) == (rows[i][1] > 0):
                events.append(dict(sym=sym, t=rows[j][0], f=rows[j][1]))
                j += 1
            t_end = rows[j][0] if j < len(rows) else None
            segs.append(dict(sym=sym, t0=rows[i][0], t_end=t_end, sgn=1 if rows[i][1] > 0 else -1, periods=j - i))
            i = j
        else:
            i += 1
print(f"钉死结算事件={len(events)} 段={len(segs)}")

t_hi = max(e["t"] for e in events)
split = t_hi - 14 * 86400

def px_at(sym, t):
    tsa = tsidx.get(sym)
    if not tsa:
        return None
    j = bisect.bisect_right(tsa, t) - 1
    if j < 0 or t - tsa[j] > 7200:
        return None
    return coins[sym][j][1]

def ret(sym, t0, t1, sgn):
    p0, p1 = px_at(sym, t0), px_at(sym, t1)
    if not p0 or not p1:
        return None
    return sgn * (p1 / p0 - 1) * 1e4 - COST

def rep(vals, tag):
    v = [x for x in vals if x is not None]
    if len(v) < 10:
        print(f"  {tag}: n={len(v)}(不足)")
        return None
    gp = sum(x for x in v if x > 0)
    gl = -sum(x for x in v if x < 0)
    med = statistics.median(v)
    print(f"  {tag}: n={len(v):4d} 中位{med:+7.1f} 均{sum(v)/len(v):+7.1f} PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in v if x>0)/len(v)*100:.0f}%")
    return med

print("\n== H1 顺拥挤(钉死结算时刻进,f<0→空/f>0→多)==")
deliver = []
for hz, tag in ((3600, "1h"), (14400, "4h"), (86400, "24h")):
    for pt, cond in (("tr", lambda e: e["t"] < split), ("ho", lambda e: e["t"] >= split)):
        sel = [e for e in events if cond(e)]
        # 同币 4h 去重(密集钉死期逐期重复=伪独立)
        ded, last = [], {}
        for e in sorted(sel, key=lambda e: e["t"]):
            if e["sym"] in last and e["t"] - last[e["sym"]] < 14400:
                continue
            last[e["sym"]] = e["t"]
            ded.append(e)
        med = rep([ret(e["sym"], e["t"], e["t"] + hz, 1 if e["f"] > 0 else -1) for e in ded],
                  f"H1×{tag} [{pt}]")
        if pt == "ho" and med is not None:
            deliver.append((med, f"H1 顺拥挤×{tag}"))

print("\n== H2 骑段(段首顺拥挤,段末离场;段≥2期)==")
for pt, cond in (("tr", lambda s: s["t0"] < split), ("ho", lambda s: s["t0"] >= split)):
    sel = [s for s in segs if cond(s) and s["periods"] >= 2 and s["t_end"]]
    med = rep([ret(s["sym"], s["t0"], s["t_end"], s["sgn"]) for s in sel], f"H2 [{pt}]")
    if pt == "ho" and med is not None:
        deliver.append((med, "H2 骑段"))

print("\n== H3 解除反转(段末反向,持4h/24h)==")
for hz, tag in ((14400, "4h"), (86400, "24h")):
    for pt, cond in (("tr", lambda s: s["t_end"] and s["t_end"] < split), ("ho", lambda s: s["t_end"] and s["t_end"] >= split)):
        sel = [s for s in segs if cond(s)]
        med = rep([ret(s["sym"], s["t_end"], s["t_end"] + hz, -s["sgn"]) for s in sel], f"H3×{tag} [{pt}]")
        if pt == "ho" and med is not None:
            deliver.append((med, f"H3 解除反转×{tag}"))

print("\n== 附:方向不对称拆分(H1×4h,train;负钉 vs 正钉)==")
ded, last = [], {}
for e in sorted(events, key=lambda e: e["t"]):
    if e["sym"] in last and e["t"] - last[e["sym"]] < 14400:
        continue
    last[e["sym"]] = e["t"]
    ded.append(e)
rep([ret(e["sym"], e["t"], e["t"] + 14400, -1) for e in ded if e["f"] < 0 and e["t"] < split], "负钉→空 [tr]")
rep([ret(e["sym"], e["t"], e["t"] + 14400, -1) for e in ded if e["f"] < 0 and e["t"] >= split], "负钉→空 [ho]")
rep([ret(e["sym"], e["t"], e["t"] + 14400, 1) for e in ded if e["f"] > 0 and e["t"] < split], "正钉→多 [tr]")
rep([ret(e["sym"], e["t"], e["t"] + 14400, 1) for e in ded if e["f"] > 0 and e["t"] >= split], "正钉→多 [ho]")

print("\n== 汇报(holdout 净中位)==")
for med, name in sorted(deliver, reverse=True):
    print(f"  {med:+7.1f} bps  {name}")
