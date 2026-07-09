#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""开盘反隔夜复算(2026-07-09;㉛候选①,用户"不等"指令下用 market_mids 全量数据当场出分;只读)。

机制:真股市开盘时刻,套利商必把夜盘 perp 投机漂移拽回真股价。机构进不去(合规),
分钟级无速度竞争,正宗"大机构看不上、进不去"的池塘。
口径与 _roster_research.py ① 完全一致(冻结):|隔夜(昨收→开前5m)|≥30bps → 开盘反向进场,
开后 30m 出场,taker 成本 12bps;簇=同一开盘时刻(独立性按簇计)。
纪律 14b:最后一个交易日簇留 holdout。
"""
import json, statistics
from datetime import datetime, timezone

MIDS = "/root/lingxi/records/okx_directional/fast_market_mids.jsonl"
COST = 12.0
KR = ["SAMSUNG-USDT", "SKHYNIX-USDT"]
US = ["SNDK-USDT", "AAPL-USDT", "SOXL-USDT", "TSLA-USDT", "NVDA-USDT", "MSTR-USDT",
      "COIN-USDT", "CRCL-USDT", "HOOD-USDT", "META-USDT", "AMZN-USDT", "GOOGL-USDT",
      "MCD-USDT", "ORCL-USDT"]
SYMS = set(KR + US)

# 需要价格的 (日期,时刻) 网格:昨收 KR 06:30 / US 20:00;开前5m;开后15/30/60m
need_min = set()  # "MM-DDTHH:MM"
days = ["2026-07-05", "2026-07-06", "2026-07-07", "2026-07-08", "2026-07-09"]
for d in days:
    for hm in ("06:30", "20:00",                      # 收盘价
               "23:55",                                 # KR 开前(次日00:00开)
               "00:15", "00:30", "01:00",              # KR 开后
               "13:25", "13:45", "14:00", "14:30"):    # US 开前/开后
        need_min.add(d + "T" + hm)

px = {}  # (day"T"HH:MM, sym) -> 首见 mid
with open(MIDS) as f:
    for line in f:
        t = line.find('"recorded_at":"')
        if t < 0:
            continue
        stamp = line[t + 15:t + 31]  # 2026-07-05T07:06
        if stamp not in need_min:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        mids = r.get("mids") or {}
        for s in SYMS:
            if s in mids and (stamp, s) not in px:
                px[(stamp, s)] = mids[s]

def P(day_idx, hm, sym):
    return px.get((days[day_idx] + "T" + hm, sym))

events = []  # (cluster_id, sym, drift, ret30, ret15, ret60)
for i, d in enumerate(days):
    wd = datetime.strptime(d, "%Y-%m-%d").replace(tzinfo=timezone.utc).weekday()
    if wd >= 5:
        continue
    # KR 开盘 d 00:00:昨收=前一日 06:30(周一用上周五,数据窗内简化=前一天;07-07周一昨收=07-04不在窗,跳过)
    if i >= 1:
        for s in KR:
            pc, pp = P(i - 1, "06:30", s), P(i, "23:55" if False else "23:55", s)
            # KR 开前 5m = 前一日 23:55
            pp = px.get((days[i - 1] + "T23:55", s))
            o15, o30, o60 = P(i, "00:15", s), P(i, "00:30", s), P(i, "01:00", s)
            if pc and pp and o30:
                drift = (pp - pc) / pc * 1e4
                events.append((d + "_KR", s, drift,
                               (o30 - pp) / pp * 1e4,
                               ((o15 - pp) / pp * 1e4) if o15 else None,
                               ((o60 - pp) / pp * 1e4) if o60 else None))
    # US 开盘 d 13:30:昨收=前一日 20:00
    if i >= 1:
        for s in US:
            pc = px.get((days[i - 1] + "T20:00", s))
            pp, o15, o30, o60 = P(i, "13:25", s), P(i, "13:45", s), P(i, "14:00", s), P(i, "14:30", s)
            if pc and pp and o30:
                drift = (pp - pc) / pc * 1e4
                events.append((d + "_US", s, drift,
                               (o30 - pp) / pp * 1e4,
                               ((o15 - pp) / pp * 1e4) if o15 else None,
                               ((o60 - pp) / pp * 1e4) if o60 else None))

print(f"可测开盘事件={len(events)}(价格点覆盖 {len(px)})")
big = [(cid, s, dr, r30, r15, r60) for cid, s, dr, r30, r15, r60 in events if abs(dr) >= 30]
print(f"|隔夜|≥30bps 事件={len(big)},簇={len(set(c for c, *_ in big))}")
clusters = {}
for cid, s, dr, r30, r15, r60 in big:
    net = (-r30 if dr > 0 else r30) - COST  # 反向隔夜,30m 出,taker
    clusters.setdefault(cid, []).append(net)
    print(f"  {cid} {s[:-5]:9s} 隔夜{dr:+8.1f} → 30m{r30:+7.1f} 反向净{net:+7.1f} {'回吐' if dr*r30<0 else '延续'}")
print("\n== 簇口径判决读数(预注册门:簇中位净>0 且 ≥7/10 簇回吐方向;当前簇数进度) ==")
cl = sorted(clusters.items())
meds = []
for cid, nets in cl:
    m = statistics.median(nets)
    meds.append(m)
    print(f"  {cid}: n={len(nets)} 簇内净中位 {m:+.1f}")
if meds:
    pos = sum(1 for m in meds if m > 0)
    print(f"  簇数={len(meds)} 簇中位的中位={statistics.median(meds):+.1f} 正簇 {pos}/{len(meds)}")
    print(f"  holdout(最后一簇)={meds[-1]:+.1f} | train 簇={len(meds)-1} 中位={statistics.median(meds[:-1]) if len(meds)>1 else float('nan'):+.1f}")
