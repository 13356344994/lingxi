#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""邻域引力场探针(㊵ v2;attention 的市场移植;2026-07-08)。

问题:预报一个新事件的结局,"局部场"(相似×新近的已完成事件,时间衰减加权)是否强于
     "全局海况"(最近30笔全体滚动,现役刹车口径)?
协议(严格无前视):走前;两种场都只用【出场早于目标入场】的已完成事件。
  全局场 g_i = 最近30笔净值中位(不看相似度)
  引力场 f_i = Σ w_j·net_j / Σ w_j,  w_j = exp(-d_ij²/2σ²)·exp(-Δt/τ),  σ=中位近邻距,τ=2h
读出:各自按场值三分位分桶的实际净值 → 顶底桶差谁大,谁的"预报"更有力。
地图层探针,不判决;若引力场胜→预注册"局部刹车"替换流程(信息集审查:全部输入无前视✓)。
"""
import json
import math
import statistics

LEDGER = "/root/lingxi/records/okx_directional/particle_ledger.jsonl"
K_SIGMA = 20
TAU_SEC = 7200.0
WARMUP = 100

rows = []
for line in open(LEDGER):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    if r.get("src") != "rest" or r.get("exit") == "unfilled":
        continue
    rows.append(r)
rows.sort(key=lambda r: r["t0"])
rows = rows[-2000:]


def ep(s):
    import calendar
    from datetime import datetime
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


def feats(r):
    return [
        max(r.get("gap_det_bps", -40), -300),
        max(r.get("gap_rest_bps", 0), -300),
        min(r.get("spread_bps", 0), 12),
        math.log1p(r.get("fuel_5m", 0)),
        max(min(r.get("breadth_30m", 0), 30), -60),
        1.0 if r["bits"] & 1 else 0.0,
        1.0 if r["bits"] & 16 else 0.0,
        0.0 if r["bits"] & 32 else 1.0,
    ]


X = [feats(r) for r in rows]
Y = [r["net_bps"] for r in rows]
Tin = [ep(r["t0"]) for r in rows]
Tout = [ep(r["time"]) for r in rows]
D = len(X[0])
print("rest事件=%d warmup=%d" % (len(rows), WARMUP))

recs = []  # (global场, 引力场, 实际)
for i in range(WARMUP, len(rows)):
    hist = [j for j in range(i) if Tout[j] < Tin[i]]
    if len(hist) < 40:
        continue
    # 全局海况:最近30笔(按出场时刻)
    recent = sorted(hist, key=lambda j: Tout[j])[-30:]
    g = statistics.median([Y[j] for j in recent])
    # 引力场:相似×新近加权
    mu = [statistics.mean(X[j][d] for j in hist) for d in range(D)]
    sd = [statistics.pstdev([X[j][d] for j in hist]) or 1.0 for d in range(D)]
    zi = [(X[i][d] - mu[d]) / sd[d] for d in range(D)]
    d2s = []
    for j in hist:
        zj = [(X[j][d] - mu[d]) / sd[d] for d in range(D)]
        d2s.append((sum((zi[k] - zj[k]) ** 2 for k in range(D)), j))
    d2s.sort()
    sigma2 = max(d2s[min(K_SIGMA, len(d2s)) - 1][0], 1e-6)
    wsum, fsum = 0.0, 0.0
    for d2, j in d2s:
        w = math.exp(-d2 / (2 * sigma2)) * math.exp(-(Tin[i] - Tout[j]) / TAU_SEC)
        wsum += w
        fsum += w * Y[j]
    if wsum <= 1e-9:
        continue
    recs.append((g, fsum / wsum, Y[i]))

n = len(recs)
print("走前样本=%d" % n)
if n >= 60:
    def bucket_read(vals, label):
        order = sorted(range(n), key=lambda k: vals[k])
        th = n // 3
        lo = [recs[k][2] for k in order[:th]]
        hi = [recs[k][2] for k in order[-th:]]
        dm = statistics.median(hi) - statistics.median(lo)
        print("  %-14s 冷桶实际中位=%+7.1f 热桶实际中位=%+7.1f 顶底差=%+6.1f" % (
            label, statistics.median(lo), statistics.median(hi), dm))
        return dm
    dg = bucket_read([r[0] for r in recs], "全局海况(现役)")
    df = bucket_read([r[1] for r in recs], "邻域引力场")
    print("裁定:引力场分层力 %s 全局海况(差 %+.1f bps)" % ("强于" if df > dg else "不强于", df - dg))
    # 叠加检验:两场都冷 vs 都热
    both_hot = [r[2] for r in recs if r[0] > statistics.median([x[0] for x in recs]) and r[1] > statistics.median([x[1] for x in recs])]
    both_cold = [r[2] for r in recs if r[0] <= statistics.median([x[0] for x in recs]) and r[1] <= statistics.median([x[1] for x in recs])]
    if len(both_hot) >= 20 and len(both_cold) >= 20:
        print("  双热区 n=%d 中位=%+.1f | 双冷区 n=%d 中位=%+.1f(叠加是否更锋利)" % (
            len(both_hot), statistics.median(both_hot), len(both_cold), statistics.median(both_cold)))
else:
    print("样本不足,继续攒")
