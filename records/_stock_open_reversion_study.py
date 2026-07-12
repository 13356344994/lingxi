#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""股票 perp 开盘反隔夜——簇口径累积读数(预注册 ㉛①,2026-07-07 登记;只读)。

对手模型(天道级):真股市闭市期间 perp 只有投机盘定价,产生隔夜漂移;开盘铃响,
套利商必须把 perp 拽回真股价(股票有真锚,加密长尾没有)。对手=闭市时段的方向投机者。
机构进不去:OKX 股票 perp 深度数千 U,单笔绝对利润对机构无意义,对几千 U 账户=大肉。

预注册口径(冻结,㉛①):每个开盘=1 簇;判决 n≥10 簇;门=簇中位净>0 且 ≥7/10 簇回吐方向;
|隔夜漂移|≥30bps 才计;交易方向=反漂移;持有 30min;成本 12bps。
本脚本=累积读数(簇不足 10 时只报进度不判决)。
数据=fast_market_mids.jsonl(07-05 起全市场 3s 落盘)。
"""
import json, statistics
from datetime import datetime, timedelta, timezone

DIR = "/root/lingxi/records/okx_directional"
KR = ["SAMSUNG-USDT", "SKHYNIX-USDT"]
US = ["SNDK-USDT", "AAPL-USDT", "SOXL-USDT", "TSLA-USDT", "NVDA-USDT", "MSTR-USDT",
      "COIN-USDT", "CRCL-USDT", "HOOD-USDT", "META-USDT", "AMZN-USDT", "GOOGL-USDT",
      "ORCL-USDT", "MCD-USDT"]
COST_BPS = 12.0
MIN_DRIFT = 30.0

# 目标时刻表:(簇名, 市场, 前收时刻, 开盘时刻, 开盘+30m)
# KR 09:00-15:30 KST = 00:00-06:30 UTC;US(7月 EDT)13:30-20:00 UTC。
# 数据起点 07-05 10:00Z;07-04/05 周末休市;07-03 美股独立日休市。
targets = []
for d in (6, 7, 8, 9):
    day = datetime(2026, 7, d, tzinfo=timezone.utc)
    prev = day - timedelta(days=1)
    # KR:周一(07-06)前收=上周五 07-03 06:30(数据起点前,跳过)
    kr_prev = prev if prev.weekday() < 5 else None
    if d == 6:
        kr_prev = None
    if kr_prev is not None:
        targets.append((f"KR-07{d:02d}", KR, kr_prev.replace(hour=6, minute=30),
                        day.replace(hour=0, minute=0), day.replace(hour=0, minute=30)))
    us_prev = prev if prev.weekday() < 5 else None
    if d in (6,):  # 07-06 前收=07-03(美股休)→ 07-02,数据起点前
        us_prev = None
    if us_prev is not None and day.weekday() < 5:
        targets.append((f"US-07{d:02d}", US, us_prev.replace(hour=20, minute=0),
                        day.replace(hour=13, minute=30), day.replace(hour=14, minute=0)))

# 需要采样的时刻集合(±90s 容忍)
want = []
for _, _, a, b, c in targets:
    want += [a, b, c]
want_keys = {t.strftime("%Y-%m-%dT%H:%M") for t in want}
# 相邻分钟也收(3s 节拍,容忍缺采)
extra = set()
for t in want:
    for dm in (-1, 1):
        extra.add((t + timedelta(minutes=dm)).strftime("%Y-%m-%dT%H:%M"))
want_keys |= extra

syms = set(KR + US)
snap = {}  # "YYYY-mm-ddTHH:MM" -> {sym: mid}(取该分钟首行)
with open(f"{DIR}/fast_market_mids.jsonl") as f:
    for line in f:
        # 廉价预筛:只解析目标分钟的行
        k = line[16:32]
        # recorded_at 位置不定,退回 find
        i = line.find('"recorded_at":"')
        if i < 0:
            continue
        key = line[i + 15:i + 31]
        if key not in want_keys:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        m = r.get("mids")
        if not m:
            continue
        if key not in snap:
            snap[key] = {s: m[s] for s in syms if s in m}

def mid_at(t, sym):
    for dm in (0, 1, -1):
        k = (t + timedelta(minutes=dm)).strftime("%Y-%m-%dT%H:%M")
        v = snap.get(k, {}).get(sym)
        if v:
            return v
    return None

print("== 股票 perp 开盘反隔夜·簇累积读数(预注册口径;n≥10 簇才判决) ==")
clusters = []
for name, symlist, t_prev, t_open, t_post in targets:
    rets = []
    for s in symlist:
        p0, p1, p2 = mid_at(t_prev, s), mid_at(t_open, s), mid_at(t_post, s)
        if not (p0 and p1 and p2):
            continue
        drift = (p1 / p0 - 1) * 1e4
        if abs(drift) < MIN_DRIFT:
            continue
        move = (p2 / p1 - 1) * 1e4
        # 反漂移交易:漂移>0 → 开盘做空,net=-move-成本;漂移<0 → 做多
        net = (-move if drift > 0 else move) - COST_BPS
        rets.append((s, drift, move, net))
    if not rets:
        print(f"  {name}: 无 |漂移|≥30 样本")
        continue
    med = statistics.median([x[3] for x in rets])
    rev = sum(1 for x in rets if (x[1] > 0) != (x[2] > 0))
    clusters.append((name, med, rev, len(rets)))
    print(f"  {name}: 簇净中位 {med:+7.1f}bps 回吐 {rev}/{len(rets)}  明细: " +
          " ".join(f"{s.split('-')[0]}(漂{d:+.0f}→动{m:+.0f})" for s, d, m, _ in rets))

if clusters:
    meds = [c[1] for c in clusters]
    pos = sum(1 for c in clusters if c[1] > 0)
    revs = sum(c[2] for c in clusters)
    tot = sum(c[3] for c in clusters)
    print(f"\n  簇数={len(clusters)}/10  簇中位的中位={statistics.median(meds):+.1f}bps  "
          f"正簇 {pos}/{len(clusters)}  总回吐率 {revs}/{tot}")
    print("  (判决门:n≥10 簇 且 簇中位净>0 且 ≥7/10 簇回吐方向;当前为进度读数)")
