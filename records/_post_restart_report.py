#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""v6.4 重启后首报(2026-07-06 19:35 BJ,用户"重启到现在数据怎么样"问询)。
对比窗:部署 10:06:56Z 前的 07-05/06 存量 vs 部署后新枪。
读数:①新枪数+闸口分布 ②排队脂肪(事件驱动唤醒刀①效果)③eval_rest_ms(REST并发刀②)④EventAge。
"""
import json
import os
import bisect
import statistics
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
DEPLOY = datetime(2026, 7, 6, 10, 6, 56)


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


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

before, after = [], []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst" or not r.get("event_age_ms"):
            continue
        et = tsu(r["recorded_at"])
        t0 = et - timedelta(milliseconds=r["event_age_ms"])
        arr = liq.get(r["symbol"], [])
        i = bisect.bisect_right(arr, t0)
        conf = None
        if i < len(arr) and (arr[i] - t0).total_seconds() <= (r["event_age_ms"] / 1000.0) + 0.2:
            conf = (arr[i] - t0).total_seconds() * 1000.0
        rec = {"t": et, "sym": r["symbol"], "age": r["event_age_ms"], "conf": conf,
               "fat": max(0.0, r["event_age_ms"] - conf) if conf is not None else None,
               "rest": r.get("eval_rest_ms"), "skip": r.get("gate_skip", "")}
        (after if et >= DEPLOY else before).append(rec)

med = lambda v: statistics.median(v) if v else float("nan")
b_fat = [x["fat"] for x in before if x["fat"] is not None]
a_fat = [x["fat"] for x in after if x["fat"] is not None]
a_rest = [x["rest"] for x in after if x["rest"]]
b_age = [x["age"] for x in before]
a_age = [x["age"] for x in after]

print("窗口: 部署后 %.0f 分钟" % ((datetime.utcnow() - DEPLOY).total_seconds() / 60))
print("新枪 n=%d" % len(after))
skips = {}
for x in after:
    skips[x["skip"] or "通过"] = skips.get(x["skip"] or "通过", 0) + 1
print("闸口:", skips)
print("排队脂肪 中位: 改前 %.0f → 改后 %.0f ms" % (med(b_fat), med(a_fat)))
print("REST组装 中位(改后): %.0f ms (n=%d)" % (med(a_rest), len(a_rest)))
print("EventAge 中位: 改前 %.0f → 改后 %.0f ms" % (med(b_age), med(a_age)))
print("\n改后逐枪:")
for x in after:
    print("  %s %-14s age=%-5d fat=%-4s rest=%-4s %s" % (
        x["t"].strftime("%H:%M:%S"), x["sym"], x["age"],
        ("%.0f" % x["fat"]) if x["fat"] is not None else "-",
        x["rest"] or "-", x["skip"] or "→过闸"))
