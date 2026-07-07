#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「股票perp开盘反隔夜」前向判决(预注册 2026-07-07 19:00 BJ = 11:00Z,HANDOFF ㉛;口径冻结不许改)。

机制:真股市开盘时刻,套利商必把夜盘 perp 漂移拽回真股价。
规则:开盘时刻(KR 00:00Z/US 13:30Z,工作日),|隔夜漂移|≥30bps 的股票perp,开盘反向隔夜漂移,
     持有30min平,成本12。隔夜=昨收(KR昨06:30Z/US昨20:00Z)→开盘前5min。
判决(㉛ 原文,按簇防独立性伪膨胀):每个(市场,开盘时刻)=1簇,簇内取净中位;
     封样后新簇 n≥10 且 全簇净中位>0 且 ≥7/10 簇方向=回吐 → 过检。
用法:python3 _stockperp_open_verdict.py [SINCE_ISO(默认封样 2026-07-07T11:00:00)]
"""
import json
import calendar
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T11:00:00"
COST = 12.0
MIN_DRIFT = 30.0
KR = ["SAMSUNG-USDT", "SKHYNIX-USDT"]
US = ["SNDK-USDT", "AAPL-USDT", "SOXL-USDT", "TSLA-USDT", "NVDA-USDT", "MSTR-USDT",
      "COIN-USDT", "CRCL-USDT", "HOOD-USDT", "META-USDT", "AMZN-USDT", "GOOGL-USDT", "ORCL-USDT"]


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
now = None
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
        now = t
        if t < t_since - 20 * 3600:  # 隔夜窗要回看昨收
            continue
        b = int(t // 300)
        if b in grid:
            continue
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


clusters = []  # (open_ts, market, [事件净bps])
day0 = int(t_since // 86400)
dayN = int((now or t_since) // 86400)
for d in range(day0, dayN + 1):
    base = d * 86400
    if datetime.utcfromtimestamp(base).weekday() >= 5:
        continue
    for mkt, syms, t_open, t_close in (
            ("KR", KR, base, base - 86400 + 6 * 3600 + 1800),
            ("US", US, base + 13 * 3600 + 1800, base - 86400 + 20 * 3600)):
        if t_open < t_since or (now and t_open > now - 1900):
            continue
        evs = []
        for sym in syms:
            pc, pp, po = px(sym, t_close), px(sym, t_open - 300), px(sym, t_open + 1800)
            if not (pc and pp and po):
                continue
            drift = (pp - pc) / pc * 1e4
            if abs(drift) < MIN_DRIFT:
                continue
            move30 = (po - pp) / pp * 1e4
            fade = (-move30 if drift > 0 else move30) - COST  # 反向隔夜,成本12
            evs.append(fade)
        if evs:
            clusters.append((t_open, mkt, evs))
clusters.sort()
print("开盘反隔夜 判决集 since %s | 簇=%d" % (SINCE, len(clusters)))
meds = []
for t_open, mkt, evs in clusters:
    m = statistics.median(evs)
    meds.append(m)
    print("  %s %s 事件=%d 簇净中位=%+7.1f %s" % (
        mkt, datetime.utcfromtimestamp(t_open).strftime("%m-%d %H:%M"), len(evs), m,
        "回吐" if m > 0 else "延续"))
if meds:
    ok_n = sum(1 for m in meds if m > 0)
    print("全簇净中位=%+.1f | 回吐簇=%d/%d" % (statistics.median(meds), ok_n, len(meds)))
    if len(meds) >= 10:
        ok = statistics.median(meds) > 0 and ok_n * 10 >= len(meds) * 7
        print("【到线】终审:%s" % ("过检 → 递用户裁探针" if ok else "不过 → 三问解剖"))
    else:
        print("进度 %d/10 簇" % len(meds))
