#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tape 终审锚监视器(只读,循环):每 10min 数一次两个预注册锚的样本量,
到样打 ANCHOR_READY(值守 agent 据此立即跑 _tape_verdict_1s.py 出终审)。
锚㋐ 大额确认桶:官方≤3s确认 且 burst 1s名义≥2000U,n≥30;
锚㋑ 活跃确认桶:官方≤3s确认 且 market_liq_5m≥40,n≥30。
"""
import json
import bisect
import calendar
import time
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


def counts():
    evs = []
    for line in open(REC + "/fast_burst_detect.jsonl"):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        evs.append((ep(r["recorded_at"]), r["symbol"],
                    float(r.get("notional_1s", 0) or 0), float(r.get("market_liq_5m", 0) or 0)))
    liq = {}
    for line in open(REC + "/fast_liquidation_events.jsonl"):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        liq.setdefault(r["symbol"], []).append(ep(r["recorded_at"]))
    for v in liq.values():
        v.sort()
    big = act = tot = 0
    for t0, sym, ntl, fuel in evs:
        ts = liq.get(sym)
        if not ts:
            continue
        i = bisect.bisect_right(ts, t0)
        if not (i < len(ts) and ts[i] - t0 <= 3.0):
            continue
        tot += 1
        if ntl >= 2000:
            big += 1
        if fuel >= 40:
            act += 1
    return tot, big, act


while True:
    try:
        tot, big, act = counts()
        now = datetime.utcnow().strftime("%H:%M")
        print("[%sZ] 确认=%d 锚㋐大额=%d/30 锚㋑活跃=%d/30" % (now, tot, big, act), flush=True)
        if big >= 30 or act >= 30:
            print("ANCHOR_READY big=%d act=%d" % (big, act), flush=True)
    except Exception as e:  # 数据文件轮换瞬间等,下一轮自愈
        print("watch error: %s" % e, flush=True)
    time.sleep(600)
