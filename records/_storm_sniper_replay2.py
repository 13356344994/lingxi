#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""爆仓风暴狙击·两日回放 v2(数据源=枪自己的 shadow 流,07-07 事故后最高保真方案)。

每条 candidate_source=tape_burst 的 shadow 行 = 枪当时真实走完"探测→确认→评估"的确认事件,
mid = 枪当刻会成交的进场价。策略照最终版:火灾 × 方向对号(_sm1,v6.2 前旧行无标记→放行并计数)
× 资格闸通过(gate_skip 空) × 海况>-10。
S = trail(10/20)+二波止错20s(代理=Bybit同币爆仓,OKX流该窗已失)+灾难-40,cap120;
M = trail(30/40)+灾难-60,cap1800【草案刻度】。成本12bps。输出 /tmp/storm2.json
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


# ---- 确认事件=shadow tape_burst 行 ----
evts = []
with open(REC + "/fast_executable_shadow_events.jsonl") as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get("candidate_source") != "tape_burst":
            continue
        reason = r.get("fast_candidate_reason", "")
        evts.append({
            "t0": ep(r["recorded_at"]), "sym": r["symbol"], "mid": r.get("mid", 0),
            "side": r.get("fast_candidate_side", ""), "skip": r.get("gate_skip", "") or "",
            "sm": ("_sm0" not in reason),  # v6.2 前无标记=放行
            "age": r.get("event_age_ms", 0),
        })
evts.sort(key=lambda e: e["t0"])
print("shadow确认事件=%d(%s → %s)" % (len(evts),
      datetime.utcfromtimestamp(evts[0]["t0"]).strftime("%m-%d %H:%M") if evts else "-",
      datetime.utcfromtimestamp(evts[-1]["t0"]).strftime("%m-%d %H:%M") if evts else "-"))

# ---- Bybit 爆仓流(二波代理;OKX 流 07-04~06 段丢失) ----
xliq = {}
with open(REC + "/fast_xvenue_liq_events.jsonl") as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        xliq.setdefault(r["symbol"], []).append(ep(r["recorded_at"]))
for v in xliq.values():
    v.sort()


def wave2_at(sym, t0):
    arr = xliq.get(sym, [])
    i = bisect.bisect_right(arr, t0 + 1)
    return arr[i] if i < len(arr) else None


# ---- 路径 ----
syms = {e["sym"] for e in evts}
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


def hold(e, act, give, dis, cap, wavecut):
    arr = paths.get(e["sym"])
    if not arr or e["mid"] <= 0:
        return None
    keys = pk[e["sym"]]
    d = -1.0  # 火灾=short
    w2 = wave2_at(e["sym"], e["t0"]) if wavecut else None
    i = bisect.bisect_left(keys, e["t0"])
    peak, last = 0.0, None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - e["t0"] > cap:
            break
        ret = d * (m - e["mid"]) / e["mid"] * 1e4
        if wavecut and T - e["t0"] >= 20 and (w2 is None or w2 > e["t0"] + 20):
            return ret
        last = ret
        peak = max(peak, ret)
        if ret <= dis:
            return ret
        if peak >= act and peak - ret >= give:
            return ret
    return last


COST = 12.0
# 海况:全确认流(双向)S口径滚动30
hist = []
for e in evts:
    e["sea"] = statistics.median(hist[-30:]) if len(hist) >= 30 else 0.0
    d = -1.0 if e["side"] == "short" else 1.0
    arr = paths.get(e["sym"])
    v = None
    if arr and e["mid"] > 0:
        keys = pk[e["sym"]]
        i = bisect.bisect_left(keys, e["t0"])
        peak = 0.0
        for j in range(i, len(arr)):
            T, m = arr[j]
            if T - e["t0"] > 120:
                break
            ret = d * (m - e["mid"]) / e["mid"] * 1e4
            v = ret
            peak = max(peak, ret)
            if ret <= -40 or (peak >= 10 and peak - ret >= 20):
                v = ret
                break
    hist.append((v if v is not None else 0) - COST)

trades = []
blocked = collections.Counter()
for e in evts:
    if e["side"] != "short":
        blocked["烟花(只打火灾)"] += 1
        continue
    if not e["sm"]:
        blocked["方向错号"] += 1
        continue
    if e["skip"]:
        blocked["资格闸(" + e["skip"] + ")"] += 1
        continue
    if e["sea"] <= -10:
        blocked["海况差(不玩)"] += 1
        continue
    vS = hold(e, 10, 20, -40, 120, True)
    vM = hold(e, 30, 40, -60, 1800, False)
    if vS is None or vM is None:
        blocked["路径缺失"] += 1
        continue
    bj = (datetime.utcfromtimestamp(e["t0"]) + timedelta(hours=8)).strftime("%m-%d %H:%M")
    trades.append({"t": bj, "sym": e["sym"].replace("-USDT", ""),
                   "S": round(vS - COST, 1), "M": round(vM - COST, 1)})

print("开枪=%d 拦截=%s" % (len(trades), dict(blocked)))
cumS = cumM = 0.0
for t in trades:
    cumS += t["S"]
    cumM += t["M"]
    t["cS"] = round(cumS, 1)
    t["cM"] = round(cumM, 1)
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
json.dump({"gen": (datetime.utcnow() + timedelta(hours=8)).strftime("%m-%d %H:%M"),
           "total": len(evts), "blocked": dict(blocked), "trades": trades},
          open("/tmp/storm2.json", "w"), ensure_ascii=False)
print("→ /tmp/storm2.json")
