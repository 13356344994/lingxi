#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""WHERE 钥匙 2 年稳定性(2026-07-09 夜班;长历史半场第一判决;只读)。

问题:惯犯钥匙(滚动 24h 暴动计数,3 天窗 AUC 0.91)在 2024-2026 的全天气里站不站得住?
数据:records/history/okx_1h/*.jsonl(首轮已落 ~205 币,字母序偏置如实标注;到一批验一批)。
口径(1H 分辨率适配,运行前冻结):
  暴动 bar=|1H ret|≥100bps;惯犯=滚动回看 24 根 bar 的暴动数(逐时点重算,全币一视同仁);
  奖品=前向 4h |ret|≥300bps(同币 24h 去重);对照=同时刻随机非奖品币;
  出分=逐月 AUC + 天气分层(全市场暴动率三分位);≥18 个月同向=钥匙是结构不是天气特产。
"""
import json, os, bisect, statistics, random
from datetime import datetime, timezone

random.seed(11)
D = "/root/lingxi/records/history/okx_1h"
RIOT = 100.0
PRIZE = 300.0

coins = {}
for fn in sorted(os.listdir(D)):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-SWAP", "")
    rows = []
    try:
        with open(f"{D}/{fn}") as f:
            for line in f:
                try:
                    d = json.loads(line)["data"]
                    rows.append((int(d[0]) / 1000.0, float(d[4])))
                except Exception:
                    continue
    except Exception:
        continue
    rows = sorted(set(rows))
    if len(rows) >= 24 * 14:  # 至少两周
        coins[sym] = rows
print(f"1H 币数={len(coins)}(字母序首轮批次,偏置如实)")
if len(coins) < 30:
    raise SystemExit("批次太小,等下一批")

# 暴动 bar 与滚动惯犯
riot_ts = {}
for sym, c in coins.items():
    rb = []
    for i in range(1, len(c)):
        if c[i - 1][1] > 0 and abs(c[i][1] / c[i - 1][1] - 1) * 1e4 >= RIOT:
            rb.append(c[i][0])
    riot_ts[sym] = rb
tsidx = {s: [x[0] for x in c] for s, c in coins.items()}

def habit(sym, t):
    a = riot_ts[sym]
    return bisect.bisect_left(a, t) - bisect.bisect_left(a, t - 86400)

# 全市场天气钟(小时网格暴动率;死水/平静/风暴三分位)
hours = {}
for sym, rb in riot_ts.items():
    for t in rb:
        hours[int(t // 3600)] = hours.get(int(t // 3600), 0) + 1
alive = {}
for sym, c in coins.items():
    for t, _ in c[:: 24]:
        pass
wx_vals = sorted(hours.values())
q1 = wx_vals[len(wx_vals) // 3] if wx_vals else 1
q2 = wx_vals[2 * len(wx_vals) // 3] if wx_vals else 2

def weather(t):
    n = hours.get(int(t // 3600), 0)
    return "死水" if n <= q1 else ("风暴" if n > q2 else "平静")

# 奖品扫描
prizes = []
for sym, c in coins.items():
    tsa = tsidx[sym]
    lastT = 0.0
    for i, (t, px) in enumerate(c):
        if t - lastT < 86400:
            continue
        j = bisect.bisect_right(tsa, t + 4 * 3600) - 1
        if j <= i or tsa[j] - t < 3 * 3600 or px <= 0:
            continue
        r = (c[j][1] / px - 1) * 1e4
        if abs(r) < PRIZE:
            continue
        lastT = t
        prizes.append(dict(sym=sym, t0=t, ret=r,
                           mo=datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m"),
                           wx=weather(t)))
print(f"奖品(4h|ret|≥{PRIZE:.0f})={len(prizes)},月份跨度={len(set(p['mo'] for p in prizes))}")

# 对照
prize_cd = set((p["sym"], int(p["t0"] // 86400)) for p in prizes)
controls = []
symlist = list(coins)
for p in prizes:
    got = tries = 0
    while got < 2 and tries < 30:
        tries += 1
        s = random.choice(symlist)
        if s == p["sym"] or (s, int(p["t0"] // 86400)) in prize_cd:
            continue
        j = bisect.bisect_left(tsidx[s], p["t0"])
        if j >= len(tsidx[s]) or abs(tsidx[s][j] - p["t0"]) > 3600:
            continue
        controls.append(dict(sym=s, t0=p["t0"], mo=p["mo"], wx=p["wx"]))
        got += 1

def auc(pos, neg):
    if len(pos) < 15 or len(neg) < 15:
        return None
    allv = sorted([(v, 1) for v in pos] + [(v, 0) for v in neg])
    rs, i = 0.0, 0
    while i < len(allv):
        j = i
        while j < len(allv) and allv[j][0] == allv[i][0]:
            j += 1
        r = (i + j + 1) / 2
        for k in range(i, j):
            if allv[k][1] == 1:
                rs += r
        i = j
    n1, n0 = len(pos), len(neg)
    return (rs - n1 * (n1 + 1) / 2) / (n1 * n0)

print("\n== WHERE 钥匙(滚动24h暴动数)逐月 AUC ==")
months = sorted(set(p["mo"] for p in prizes))
okm = totm = 0
for mo in months:
    pv = [habit(p["sym"], p["t0"]) for p in prizes if p["mo"] == mo]
    cv = [habit(c["sym"], c["t0"]) for c in controls if c["mo"] == mo]
    a = auc(pv, cv)
    if a is None:
        continue
    totm += 1
    if a > 0.5:
        okm += 1
    bar = "█" * int(max(a - 0.5, 0) * 40)
    print(f"  {mo}: AUC={a:.3f} n={len(pv):4d} {bar}")
print(f"  同向月份 {okm}/{totm}")

print("\n== 按天气分层(全期)==")
for wx in ("死水", "平静", "风暴"):
    pv = [habit(p["sym"], p["t0"]) for p in prizes if p["wx"] == wx]
    cv = [habit(c["sym"], c["t0"]) for c in controls if c["wx"] == wx]
    a = auc(pv, cv)
    print(f"  {wx}: AUC={a if a is None else round(a,3)} 奖品n={len(pv)}")

print("\n== 方向副产品:惯犯奖品的方向分布(全期逐月,向上%;50=硬币)==")
for mo in months[-12:]:
    sel = [p for p in prizes if p["mo"] == mo and habit(p["sym"], p["t0"]) >= 3]
    if len(sel) < 15:
        continue
    up = sum(1 for p in sel if p["ret"] > 0)
    print(f"  {mo}: n={len(sel):4d} 向上{up/len(sel)*100:.0f}%")
