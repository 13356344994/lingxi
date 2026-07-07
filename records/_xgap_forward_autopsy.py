#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E策略前向判决期逐笔解剖(只读;判决口径不动,仅加观测维度)。

目的:回答"离线 +4.6 → 前向 -7.4 的差距漏在哪"。
维度:①出场类型(收敛/到时/灾难) ②持有时长 ③入场价差深度 ④时段(小时桶)
     ⑤分币 ⑥判决期 vs 前一天同口径对照 ⑦影子线 gate_skip 比例(执行层证据)。
与 _xgap_reversion_verdict.py 完全同规则(40/10/600/-60/1800,成本12),只是多记出场原因。
"""
import json
import bisect
import calendar
import collections
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-06T00:50:00"  # 多回看一天做对照
VERDICT_CUT = "2026-07-07T00:50:00"
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
t_cut = ep(VERDICT_CUT)
paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and line[p + 15:p + 34] < SINCE and '"backlog' not in line:
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            v = d.get("backlog_venue")
            tgt = anch[d["backlog_sym"]] if v == "bybit" else (paths[d["backlog_sym"]] if not v else None)
            if tgt is not None:
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= t_since - 600:
                        tgt.append((pt[0] / 1000.0, pt[1]))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((T, mid))
        for sym, val in (d.get("anchors") or {}).get("bybit", {}).items():
            if isinstance(val, (list, tuple)) and len(val) >= 2:
                anch[sym].append((val[1] / 1000.0, val[0]))
for s in paths:
    paths[s] = sorted(set(paths[s]))
for s in anch:
    anch[s] = sorted(set(anch[s]))
ak = {s: [x[0] for x in arr] for s, arr in anch.items()}


def a_at(sym, t, tol=8):
    arr = anch.get(sym)
    keys = ak.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


trades = []
last = {}
for sym, arr in paths.items():
    if sym not in anch or len(anch[sym]) < 10:
        continue
    n = len(arr)
    j = 0
    while j < n:
        T, okx = arr[j]
        if T < t_since or (sym in last and T - last[sym] < 1800):
            j += 1
            continue
        by = a_at(sym, T)
        if by and by > 0:
            gap = (okx - by) / by * 1e4
            if gap <= -40:
                entry, t_in = okx, T
                out, exit_kind, t_out = None, "max_hold", None
                for k in range(j + 1, n):
                    T2, okx2 = arr[k]
                    if T2 - t_in > 600:
                        break
                    by2 = a_at(sym, T2)
                    ret = (okx2 - entry) / entry * 1e4
                    out, t_out = ret, T2
                    if ret <= -60:
                        exit_kind = "disaster"
                        break
                    if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
                        exit_kind = "converged"
                        break
                if out is not None:
                    trades.append({"t": t_in, "sym": sym, "v": out, "gap": gap,
                                   "exit": exit_kind, "hold": (t_out - t_in) if t_out else 0.0})
                    last[sym] = t_in
        j += 1


def stats(rows, label):
    if not rows:
        print("  %-28s n=0" % label)
        return
    v = [r["v"] - COST for r in rows]
    med = statistics.median(v)
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    pf = pw / pl if pl > 0 else 99
    print("  %-28s n=%-4d 净中位=%+7.1f 净均=%+7.1f 净合计=%+8.0f PF=%.2f 持有中位=%.0fs" % (
        label, len(v), med, sum(v) / len(v), sum(v), pf, statistics.median([r["hold"] for r in rows])))


fwd = [r for r in trades if r["t"] >= t_cut]
prior = [r for r in trades if r["t"] < t_cut]
print("===== 总体对照(同口径同数据源) =====")
stats(prior, "对照期(07-06 00:50Z→封样)")
stats(fwd, "判决期(07-07 00:50Z→now)")

print("\n===== 判决期按出场类型 =====")
for kind in ("converged", "max_hold", "disaster"):
    stats([r for r in fwd if r["exit"] == kind], kind)
print("对照期出场分布: %s" % dict(collections.Counter(r["exit"] for r in prior)))
print("判决期出场分布: %s" % dict(collections.Counter(r["exit"] for r in fwd)))

print("\n===== 判决期按入场价差深度 =====")
for lo, hi, lab in ((-50, -40, "40-50"), (-60, -50, "50-60"), (-99999, -60, ">=60")):
    stats([r for r in fwd if lo <= r["gap"] < hi], "折价 " + lab)

print("\n===== 判决期按小时(UTC) =====")
by_h = collections.defaultdict(list)
for r in fwd:
    by_h[datetime.utcfromtimestamp(r["t"]).hour].append(r)
for h in sorted(by_h):
    stats(by_h[h], "UTC %02d 时" % h)

print("\n===== 判决期分币(净合计绝对值 top10) =====")
by_s = collections.defaultdict(list)
for r in fwd:
    by_s[r["sym"]].append(r)
ranked = sorted(by_s.items(), key=lambda kv: -abs(sum(x["v"] - COST for x in kv[1])))
for sym, rows in ranked[:10]:
    stats(rows, sym.replace("-USDT", ""))

print("\n===== 影子线执行层证据(fast_executable_shadow_events, xgap_rev) =====")
skip = collections.Counter()
n_sh = 0
with open(REC + "/fast_executable_shadow_events.jsonl") as f:
    for line in f:
        if '"candidate_source":"xgap_rev"' not in line:
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        n_sh += 1
        skip[d.get("gate_skip") or "PASS"] += 1
print("影子事件=%d 分布=%s" % (n_sh, dict(skip)))
