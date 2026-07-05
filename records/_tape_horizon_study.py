#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tape 持有期扫描(只读;07-05 用户指示"刀本身要硬/可适当延长持仓"的直接执行)。

问题:tape 全部既有裁决数都是 +60s 定点口径;肥尾(均值≫中位)+ 实枪 trail 出场
意味着"亚秒进场 × 更长持有"是未测格子。本脚本对官方≤3s确认的 burst:
  ① 定点收益 60/120/180/300/600s(hot_micro +1s 进场,hot_mids 3s 路径出场);
  ② trail 模拟(实枪同族规则,预注册写死:激活+10bps/回吐20bps/灾难-40/max_hold 120|300|600s);
  分桶:全部 / 大额≥2kU / 大额×向下。
锚(不考虑辅助杠杆,硬门):中位≥12=飞轮门,≥18=利润门;trail口径按毛均对成本 12。
"""
import json
import bisect
import calendar
import collections
import statistics
import os
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- 确认事件 ----
events = []
for line in open(REC + "/fast_burst_detect.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    events.append({"t0": ep(r["recorded_at"]), "sym": r["symbol"],
                   "dir": 1.0 if r.get("side") == "buy" else -1.0,
                   "ntl": float(r.get("notional_1s", 0) or 0)})
events.sort(key=lambda e: e["t0"])
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
conf = []
for e in events:
    ts = liq.get(e["sym"])
    if not ts:
        continue
    i = bisect.bisect_right(ts, e["t0"])
    if i < len(ts) and ts[i] - e["t0"] <= 3.0:
        conf.append(e)
print("确认事件=%d" % len(conf))

# ---- 进场价:hot_micro @ t0+1s(1s 精度) ----
want = {}
for k, e in enumerate(conf):
    for s in range(1, 4):
        want.setdefault(datetime.utcfromtimestamp(int(e["t0"]) + s).strftime("%Y-%m-%dT%H:%M:%S"), []).append(k)
for e in conf:
    e["entry"] = None
    e["entry_t"] = None
for fn in (REC + "/fast_hot_micro.jsonl.1", REC + "/fast_hot_micro.jsonl"):
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            p = line.find('"recorded_at":"')
            if p < 0:
                continue
            ks = want.get(line[p + 15:p + 34])
            if not ks:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            mid = d.get("mid", 0)
            if mid <= 0:
                continue
            T = ep(d["recorded_at"])
            for k in ks:
                e = conf[k]
                if e["sym"] == d.get("symbol") and 1.0 <= T - e["t0"] <= 4.0:
                    if e["entry"] is None or T < e["entry_t"]:
                        e["entry"], e["entry_t"] = mid, T

# ---- 出场路径:hot_mids(3s),只留确认币 ----
syms = {e["sym"] for e in conf}
paths = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            if d["backlog_sym"] in syms and not d.get("backlog_venue"):
                for ms, mid in d.get("backlog", []):
                    paths[d["backlog_sym"]].append((ms / 1000.0, mid))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            if sym in syms:
                paths[sym].append((T, mid))
for s in paths:
    paths[s] = sorted(set(paths[s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def px(sym, t, tol=10):
    arr = paths.get(sym)
    if not arr:
        return None
    keys = pk[sym]
    i = bisect.bisect_left(keys, t)
    if i < len(arr) and arr[i][0] - t <= tol:
        return arr[i][1]
    return None


HORIZONS = (60, 120, 180, 300, 600)
usable = [e for e in conf if e["entry"]]
print("有进场价=%d" % len(usable))


def fixed_ret(e, h):
    m = px(e["sym"], e["t0"] + h)
    if m is None:
        return None
    return e["dir"] * (m - e["entry"]) / e["entry"] * 1e4


def trail_ret(e, max_hold):
    arr = paths.get(e["sym"])
    if not arr:
        return None
    keys = pk[e["sym"]]
    i = bisect.bisect_left(keys, e["entry_t"])
    peak = 0.0
    last = None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - e["t0"] > max_hold:
            break
        ret = e["dir"] * (m - e["entry"]) / e["entry"] * 1e4
        last = ret
        peak = max(peak, ret)
        if ret <= -40:
            return ret
        if peak >= 10 and peak - ret >= 20:
            return ret
    return last


def stat(vals, tag):
    vals = [v for v in vals if v is not None]
    if not vals:
        print("  %-20s n=0" % tag)
        return
    print("  %-20s n=%-3d 中位=%+7.1f 均值=%+7.1f 顺向=%3.0f%%" % (
        tag, len(vals), statistics.median(vals), sum(vals) / len(vals),
        100.0 * sum(1 for v in vals if v > 0) / len(vals)))


BUCKETS = (
    ("全部确认", lambda e: True),
    ("大额>=2kU", lambda e: e["ntl"] >= 2000),
    ("大额×向下", lambda e: e["ntl"] >= 2000 and e["dir"] < 0),
)
for bt, bf in BUCKETS:
    rows = [e for e in usable if bf(e)]
    print("\n===== %s(n=%d)=====" % (bt, len(rows)))
    for h in HORIZONS:
        stat([fixed_ret(e, h) for e in rows], "定点 +%ds" % h)
    for mh in (120, 300, 600):
        stat([trail_ret(e, mh) for e in rows], "trail cap%ds" % mh)
print("\n硬门(不计辅助杠杆):中位/trail毛均 ≥12=飞轮门 ≥18=利润门;3s路径对trail峰值有低估(保守方向)。")
