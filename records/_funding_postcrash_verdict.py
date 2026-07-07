#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""#8b「资费后崩」前向判决(预注册 2026-07-07 16:25 BJ = 08:25Z,HANDOFF ㉗;口径冻结不许改)。

机制:极端负资费(f≤-0.05%)币,吃资费的多头收款即卖=结算后被迫流。
规则:结算时刻 t0 做空(锚=xmeta 结算前最后快照 ≤30min),t0+300s 平,成本12。
判决(㉗ 原文):封样后新结算事件 n≥40 且 post5 漂移中位 ≤ -15bps 且 向下率 ≥60%
     且 跨日成立(≥2个交易日且各日中位<0)→ 过检,递用户裁探针配置。
用法:python3 _funding_postcrash_verdict.py [SINCE_ISO(默认封样 2026-07-07T08:25:00)]
"""
import json
import bisect
import calendar
import collections
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T08:25:00"
COST = 12.0
F_EXTREME = -0.0005


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)

# ---- xmeta:极端负资费的结算事件 ----
ev = {}  # (sym, settle) -> f(结算前最后快照)
for line in open(REC + "/fast_xmeta_snapshots.jsonl"):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    T = ep(d["recorded_at"])
    if T < t_since - 1800:
        continue
    for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
        if not (isinstance(v, list) and len(v) >= 3 and v[2] > 0):
            continue
        f = float(v[0])
        st = int(v[2]) / 1000.0
        if f <= F_EXTREME and st > T and st - T <= 1800 and st >= t_since:
            ev[(sym, st)] = f  # 靠后的快照覆盖靠前=结算前最后一个

# ---- market_mids 5min 网格(结算时刻整点对齐) ----
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
        if t < t_since - 600:
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


rows = []  # (settle, sym, post5漂移bps)
for (sym, st), f in ev.items():
    p0, p5 = px(sym, st), px(sym, st + 300)
    if not (p0 and p5):
        continue
    rows.append((st, sym, (p5 - p0) / p0 * 1e4))
rows.sort()
drift = [r[2] for r in rows]
net = [-x - COST for x in drift]  # 做空净
print("#8b 判决集 since %s | n=%d(极端负资费结算事件,路径完整)" % (SINCE, len(rows)))
if rows:
    med = statistics.median(drift)
    dn = 100.0 * sum(1 for x in drift if x < 0) / len(drift)
    nmed = statistics.median(net)
    pw = sum(x for x in net if x > 0)
    pl = -sum(x for x in net if x < 0)
    byday = collections.defaultdict(list)
    for st, sym, x in rows:
        byday[datetime.utcfromtimestamp(st).strftime("%m-%d")].append(x)
    days_ok = sum(1 for d in byday.values() if statistics.median(d) < 0)
    print("post5漂移中位=%+.1f 向下率=%.0f%% | 做空净中位=%+.1f 净PF=%.2f | 跨日:%d/%d 日中位<0" % (
        med, dn, nmed, pw / pl if pl > 0 else 99, days_ok, len(byday)))
    for day in sorted(byday):
        arr = byday[day]
        print("  %s n=%-3d post5中位=%+7.1f" % (day, len(arr), statistics.median(arr)))
    if len(rows) >= 40:
        ok = med <= -15 and dn >= 60 and len(byday) >= 2 and days_ok == len(byday)
        print("【到线】终审:%s" % ("过检 → 递用户裁探针配置" if ok else "不过 → 三问解剖"))
    else:
        print("进度 %d/40" % len(rows))
