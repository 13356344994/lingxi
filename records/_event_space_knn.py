#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""事件嵌入空间·结构探针(㊵ LLM-同构升级第一块;2026-07-08)。

可证伪问题:"相似事件→相似结局"在可执行口径(rest)数据里成立吗?
协议(严格无前视):按时间走前;对事件 i,邻居只取【出场时刻早于 i 入场】的已完成事件;
  特征=入场时刻可知的 8 维(折价深度/REST残余/点差/燃料/广度/新鲜位/深折位/股票位),
  逐维 z 标准化(统计量只用过去);距离=欧氏;K=20;预测=邻居净值中位。
读出:①IC(预测与实际的秩相关) ②按预测三分位分桶的实际净值——顶桶>底桶且差距>10bps=空间有结构。
这是地图层探针,不判决;若有结构→"邻域期望"作为提名信号走预注册。
"""
import json
import math
import statistics

LEDGER = "/root/lingxi/records/okx_directional/particle_ledger.jsonl"
K = 20
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
rows = rows[-2000:]  # O(n²)防护:账本增长后只看最近2000事件(空间的"近期性"本就是引力场语义)
print("可执行事件(rest)=%d,warmup=%d,K=%d" % (len(rows), WARMUP, K))


def feats(r):
    return [
        max(r.get("gap_det_bps", -40), -300),
        max(r.get("gap_rest_bps", 0), -300),
        min(r.get("spread_bps", 0), 12),
        math.log1p(r.get("fuel_5m", 0)),
        max(min(r.get("breadth_30m", 0), 30), -60),
        1.0 if r["bits"] & 1 else 0.0,    # fresh
        1.0 if r["bits"] & 16 else 0.0,   # d60
        0.0 if r["bits"] & 32 else 1.0,   # stock perp
    ]


X = [feats(r) for r in rows]
Y = [r["net_bps"] for r in rows]
T0 = [r["t0"] for r in rows]
TX = [r["time"] for r in rows]  # 出场时刻(邻居资格=出场早于目标入场)

preds, actual = [], []
D = len(X[0])
for i in range(WARMUP, len(rows)):
    hist = [j for j in range(i) if TX[j] < T0[i]]  # 无前视:已完成者才可当邻居
    if len(hist) < K + 10:
        continue
    mu = [statistics.mean(X[j][d] for j in hist) for d in range(D)]
    sd = [statistics.pstdev([X[j][d] for j in hist]) or 1.0 for d in range(D)]
    zi = [(X[i][d] - mu[d]) / sd[d] for d in range(D)]
    dist = []
    for j in hist:
        zj = [(X[j][d] - mu[d]) / sd[d] for d in range(D)]
        dist.append((sum((zi[d] - zj[d]) ** 2 for d in range(D)), j))
    dist.sort()
    nb = [Y[j] for _, j in dist[:K]]
    preds.append(statistics.median(nb))
    actual.append(Y[i])

n = len(preds)
print("走前样本=%d" % n)
if n >= 60:
    def ranks(v):
        order = sorted(range(len(v)), key=lambda k: v[k])
        rk = [0.0] * len(v)
        for pos, k in enumerate(order):
            rk[k] = pos
        return rk
    rp, ra = ranks(preds), ranks(actual)
    mp, ma = statistics.mean(rp), statistics.mean(ra)
    cov = sum((rp[k] - mp) * (ra[k] - ma) for k in range(n))
    ic = cov / (math.sqrt(sum((x - mp) ** 2 for x in rp)) * math.sqrt(sum((x - ma) ** 2 for x in ra)) or 1)
    print("IC(秩相关)=%.3f" % ic)
    order = sorted(range(n), key=lambda k: preds[k])
    lo = [actual[k] for k in order[:n // 3]]
    hi = [actual[k] for k in order[-(n // 3):]]
    md = [actual[k] for k in order[n // 3:-(n // 3)]]
    for lab, v in (("预测最差桶", lo), ("中间桶", md), ("预测最好桶", hi)):
        pw = sum(x for x in v if x > 0)
        pl = -sum(x for x in v if x < 0)
        print("  %-8s n=%-3d 实际净中位=%+7.1f PF=%.2f" % (lab, len(v), statistics.median(v), pw / pl if pl > 0 else 99))
    print("结构判定:顶桶-底桶中位差=%+.1f bps(>10=空间有结构,值得建引力场)" % (statistics.median(hi) - statistics.median(lo)))
else:
    print("样本不足,继续攒")
