#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""延迟解剖(2026-07-06 耳朵工程改造前基线):EventAgeMs = 官方确认到达延迟 + 自身排队脂肪。
官方到达延迟不可减(除非抢跑=已证伪);自身脂肪(评估轮询150ms+串行REST~200ms)是刀口。
输出改造前基线,改造后同口径复测对比。
"""
import json
import os
import statistics
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")


def ts(s):
    s = s.replace("Z", "+00:00")
    return datetime.fromisoformat(s).replace(tzinfo=None) if "+" not in s else \
        datetime.fromisoformat(s).astimezone().replace(tzinfo=None)


def tsu(s):  # UTC naive
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


# 官方爆仓本地到达时刻(recorded_at=我们收到推送的时刻)
liq = {}
with open(os.path.join(DIR, "fast_liquidation_events.jsonl")) as f:
    for line in f:
        try:
            r = json.loads(line)
            liq.setdefault(r["symbol"], []).append(tsu(r["recorded_at"]))
        except Exception:
            continue
for v in liq.values():
    v.sort()

import bisect

rows = []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst":
            continue
        age = r.get("event_age_ms")
        if not age:
            continue
        eval_t = tsu(r["recorded_at"])
        t0 = eval_t - timedelta(milliseconds=age)
        arr = liq.get(r["symbol"], [])
        i = bisect.bisect_right(arr, t0)
        # 确认那笔=burst 后第一笔官方爆仓到达
        if i < len(arr) and (arr[i] - t0).total_seconds() <= (age / 1000.0) + 0.2:
            confirm_ms = (arr[i] - t0).total_seconds() * 1000.0
            rows.append((r["recorded_at"][:10], age, confirm_ms, max(0.0, age - confirm_ms)))

print("tape_burst 确认事件可解剖=%d" % len(rows))
ages = [x[1] for x in rows]
confs = [x[2] for x in rows]
fats = [x[3] for x in rows]


def q(v, p):
    s = sorted(v)
    return s[min(len(s) - 1, int(p * len(s)))]


print("EventAgeMs(burst→评估) 中位=%.0f P90=%.0f" % (statistics.median(ages), q(ages, 0.9)))
print("官方确认到达(burst→推送落地,不可减) 中位=%.0f P90=%.0f" % (statistics.median(confs), q(confs, 0.9)))
print("自身排队脂肪(150ms轮询等待) 中位=%.0f P90=%.0f" % (statistics.median(fats), q(fats, 0.9)))
print("评估后还有:串行REST盘口+深度 ~200ms(实测95+100ms)+ 下单RTT ~100ms")
print("\n== 分日 ==")
days = sorted(set(x[0] for x in rows))
for d in days:
    sub = [x for x in rows if x[0] == d]
    print("%s n=%-4d age中位=%-6.0f 确认中位=%-6.0f 脂肪中位=%.0f" % (
        d, len(sub), statistics.median([x[1] for x in sub]),
        statistics.median([x[2] for x in sub]), statistics.median([x[3] for x in sub])))
