#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""爆仓风暴狙击·最终版策略两日全量回放(07-07 用户"按这个策略跑一遍"指令)。

策略(第一性原理重构版,零挖掘切点):
  进场 = 官方≤3s确认的火灾 burst × 被爆方向对号 → 跟空(不加任何聪明条件)
  闸   = 海况(滚动30笔确认流影子净值中位 > -10;T1 前向验证的"坏海不下水")
  快档S = trail(激活10/回吐20) + 二波止错20s + 灾难-40,cap 120s
  慢档M = trail(激活30/回吐40) + 灾难-60,cap 1800s 【草案参数,标定中——形状定,刻度未定】
  成本 = 12bps 全价。输出 /tmp/storm_sniper.json
"""
import json
import bisect
import calendar
import collections
import statistics
import os
from datetime import datetime, timedelta

REC = "/root/lingxi/records/okx_directional"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- burst + 官方确认 + 方向对号 ----
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
                   "dir": 1.0 if r.get("side") == "buy" else -1.0})
events.sort(key=lambda e: e["t0"])
liqd = {}
for line in open(REC + "/fast_liquidation_events.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    liqd.setdefault(r["symbol"], []).append((ep(r["recorded_at"]), r.get("pos_side", "")))
for v in liqd.values():
    v.sort()

conf = []
for e in events:
    arr = liqd.get(e["sym"])
    if not arr:
        continue
    keys = [t for t, _ in arr]
    i = bisect.bisect_right(keys, e["t0"])
    sides = [s for t, s in arr[i:] if t - e["t0"] <= 3.0]
    if not sides:
        continue
    want = "long" if e["dir"] < 0 else "short"
    e["sm"] = sum(1 for s in sides if s == want) * 2 >= len(sides)
    e["wave2"] = None
    later = [t for t, _ in arr[i:] if t > e["t0"] + 3]
    if later:
        e["wave2"] = later[0]
    conf.append(e)

# ---- 路径(hot_mids)----
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
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2:
                        paths[d["backlog_sym"]].append((pt[0] / 1000.0, pt[1]))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            if sym in syms:
                paths[sym].append((T, mid))
for s in paths:
    paths[s] = sorted(set(paths[s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def entry_px(e):
    arr = paths.get(e["sym"])
    keys = pk.get(e["sym"])
    if not arr:
        return None, None
    i = bisect.bisect_left(keys, e["t0"] + 1.0)
    if i < len(arr) and arr[i][0] - e["t0"] <= 9.0:
        return arr[i][1], arr[i][0]
    return None, None


def hold(e, px, te, act, give, dis, cap, wavecut):
    arr = paths.get(e["sym"])
    keys = pk.get(e["sym"])
    i = bisect.bisect_left(keys, te)
    peak, last = 0.0, None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - e["t0"] > cap:
            break
        ret = e["dir"] * (m - px) / px * 1e4
        if wavecut and T - e["t0"] >= 20 and (e["wave2"] is None or e["wave2"] > e["t0"] + 20):
            return ret, T - e["t0"]
        last = ret
        peak = max(peak, ret)
        if ret <= dis:
            return ret, T - e["t0"]
        if peak >= act and peak - ret >= give:
            return ret, T - e["t0"]
    return last, cap


# ---- 海况(全确认流滚动30) ----
seq = sorted(conf, key=lambda e: e["t0"])
hist = []
for e in seq:
    e["sea"] = statistics.median(hist[-30:]) if len(hist) >= 30 else 0.0
    px, te = entry_px(e)
    e["px"], e["te"] = px, te
    if px:
        v, _ = hold(e, px, te, 10, 20, -40, 120, True)
        hist.append((v if v is not None else 0) - 12)
    else:
        hist.append(0)

# ---- 策略执行 ----
COST = 12.0
trades = []
blocked = collections.Counter()
for e in seq:
    if e["dir"] > 0:
        blocked["烟花(只打火灾)"] += 1
        continue
    if not e["sm"]:
        blocked["方向错号"] += 1
        continue
    if e["sea"] <= -10:
        blocked["海况差(不玩)"] += 1
        continue
    if not e["px"]:
        blocked["路径缺失"] += 1
        continue
    vS, hS = hold(e, e["px"], e["te"], 10, 20, -40, 120, True)
    vM, hM = hold(e, e["px"], e["te"], 30, 40, -60, 1800, False)
    if vS is None or vM is None:
        blocked["路径缺失"] += 1
        continue
    bj = (datetime.utcfromtimestamp(e["t0"]) + timedelta(hours=8)).strftime("%m-%d %H:%M:%S")
    trades.append({"t": bj, "sym": e["sym"].replace("-USDT", ""),
                   "S": round(vS - COST, 1), "hS": int(hS),
                   "M": round(vM - COST, 1), "hM": int(hM)})

print("确认事件=%d | 开枪=%d | 拦截=%s" % (len(seq), len(trades), dict(blocked)))
for prog in ("S", "M"):
    v = [t[prog] for t in trades]
    if not v:
        continue
    wins = [x for x in v if x > 5]
    losses = [x for x in v if x < -5]
    wr = 100 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    print("程序%s: n=%d 净合计=%+.1f 净中位=%+.1f 胜(中性)=%.0f%% 净PF=%.2f" % (
        prog, len(v), sum(v), statistics.median(v), wr, pw / pl if pl > 0 else 99))
cumS = cumM = 0.0
for t in trades:
    cumS += t["S"]
    cumM += t["M"]
    t["cumS"] = round(cumS, 1)
    t["cumM"] = round(cumM, 1)
json.dump({"gen": (datetime.utcnow() + timedelta(hours=8)).strftime("%m-%d %H:%M"),
           "total": len(seq), "blocked": dict(blocked), "trades": trades},
          open("/tmp/storm_sniper.json", "w"), ensure_ascii=False)
print("→ /tmp/storm_sniper.json")
