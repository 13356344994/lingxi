#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""烟花能量出场·圣日协议完整验证(2026-07-09;用户"所有的枪都要这样验证"指令;只读)。

背景:57 号翻案的 train 混入了 07-08(当时圣日未立),且账本烟花行 07-08 才通电——
现役枪(fast_firework_energy_exit_enabled=true)欠一次干净的圣日验证。
本脚本用普查流回溯重建烟花细胞到 07-05(与 _firework_kfold_study 同法),
分析集=07-06/07(train)+07-09(holdout),圣日=07-05(周日)+07-08 一次性终验。

冻结口径(与已部署的枪逐条同源,零新参数):
  细胞=普查行 ret60≥60 且 ret15<0.25×ret60 且 0<spread≤6,同币 600s 去重;
  三特征=非惯犯(24h细胞<3,走前)×完全力竭(ret15<0.05×ret60)×无本币爆仓(sym_liq_5m=0);
  进场=细胞时刻后首个标尺点做空;成本 12;
  能量出场=unit=clip(|ret60|,40,200),灾难1×/trail激活0.8×/回吐0.4×/cap600(=fastLedgerFireworkExitES);
  对照=旧尺(-40 固定/300s)。
圣日消费登记:假设「烟花能量出场×(全体/力竭/三特征)」消费圣日一次,结果不论好坏入 HANDOFF。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
TRAIN = {"2026-07-06", "2026-07-07"}
HOLDOUT = {"2026-07-09"}
VAULT = {"2026-07-05", "2026-07-08"}

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# ---- 细胞重建(普查流;惯犯计数走前)----
cells = []
last = {}
prior_ring = {}
with open(f"{REC}/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        r60 = r.get("ret_60s_bps", 0.0)
        r15 = r.get("ret_15s_bps", 0.0)
        sp = r.get("spread_bps", 0.0)
        if r60 < 60 or r15 >= 0.25 * r60 or sp <= 0 or sp > 6:
            continue
        t0 = ts(r["recorded_at"])
        if t0 < ts("2026-07-05T07:10:00"):
            continue
        sym = r["symbol"]
        if sym in last and t0 - last[sym] < 600:
            continue
        last[sym] = t0
        ring = prior_ring.setdefault(sym, [])
        cut = t0 - 86400
        while ring and ring[0] < cut:
            ring.pop(0)
        prior = len(ring)
        ring.append(t0)
        cells.append(dict(sym=sym, t0=t0, r15=r15, r60=r60, prior=prior,
                          liq=r.get("sym_liq_5m", 0), day=day_of(t0)))
cells.sort(key=lambda c: c["t0"])
print(f"细胞={len(cells)} (train={sum(1 for c in cells if c['day'] in TRAIN)} "
      f"holdout={sum(1 for c in cells if c['day'] in HOLDOUT)} 圣日={sum(1 for c in cells if c['day'] in VAULT)})")

# ---- 路径 ----
idx = {}
for c in cells:
    idx.setdefault(c["sym"], []).append(c["t0"])
paths = {(c["sym"], c["t0"]): [] for c in cells}
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
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t) - 1
            if j >= 0 and t0s[j] <= t <= t0s[j] + 630:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

rows = []
for c in cells:
    pts = sorted(set(paths[(c["sym"], c["t0"])]))
    pts = [(t - c["t0"], m) for t, m in pts]
    if len(pts) < 3:
        continue
    ent = pts[0][1]
    unit = min(max(abs(c["r60"]), 40.0), 200.0)
    # 能量尺(空头)
    peak, px = 0.0, None
    for t, m in pts:
        u = -(m - ent) / ent * 1e4
        if t >= 600 or u <= -unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    es = -(px - ent) / ent * 1e4 - COST if px else None
    # 旧尺(-40/300s)
    px2 = None
    for t, m in pts:
        u = -(m - ent) / ent * 1e4
        if t >= 300 or u <= -40:
            px2 = m
            break
        px2 = m
    old = -(px2 - ent) / ent * 1e4 - COST if px2 else None
    if es is None or old is None:
        continue
    rows.append(dict(c, es=es, old=old))
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key, name in (("es", "能量"), ("old", "旧尺")):
        n = [r[key] for r in sub]
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {name}: n={len(n):4d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

SUBSETS = [
    ("全体细胞", lambda r: True),
    ("完全力竭", lambda r: r["r15"] < 0.05 * r["r60"]),
    ("三特征(枪口径)", lambda r: r["prior"] < 3 and r["r15"] < 0.05 * r["r60"] and r["liq"] == 0),
]
for name, cond in SUBSETS:
    print(f"== {name} ==")
    rep([r for r in rows if cond(r) and r["day"] in TRAIN], "train(06/07)")
    rep([r for r in rows if cond(r) and r["day"] in HOLDOUT], "holdout(09) ")

print("\n== 🏛 圣日终验(07-05+07-08;一次性消费,登记 HANDOFF)==")
for name, cond in SUBSETS:
    rep([r for r in rows if cond(r) and r["day"] in VAULT], f"圣日·{name}")
