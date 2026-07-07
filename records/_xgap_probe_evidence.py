#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E格子探针申请·执行层证据包(影子线真实 REST 数据;㉙承诺随证递交的材料)。

回答四个问题:
  ① 影子线漏斗:探测→REST 建档的 gap_gone/spread_wide 损耗率(逆选择的直接测量)
  ② 延迟:探测→REST 建档 EventAgeMs 分布(实枪的真实时滞)
  ③ 可执行子集成绩:影子 PASS 事件(真实 REST mid 入场)按 E 出场规则的前向结果,
     再叠加 新鲜度/派班(格子口径)——这才是探针真正会打的那批枪的离线预估
  ④ 代码缺口审计:实枪 v6.5 只有 gap_gone/spread_wide 守卫,【无新鲜度/无派班闸】——
     若按格子口径开探针,fast_xgap_line.go 须先补这两闸(探针前置工程项)
"""
import json
import bisect
import calendar
import collections
import statistics
import subprocess
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- ①② 影子事件(grep 预筛,全文件仅百余行) ----
out = subprocess.run(["grep", '"candidate_source":"xgap_rev"', REC + "/fast_executable_shadow_events.jsonl"],
                     stdout=subprocess.PIPE, universal_newlines=True).stdout
shadow = []
for line in out.splitlines():
    try:
        shadow.append(json.loads(line))
    except ValueError:
        continue
skip = collections.Counter(d.get("gate_skip") or "PASS" for d in shadow)
ages = sorted(d.get("event_age_ms", 0) for d in shadow if d.get("event_age_ms"))
spreads = sorted(d.get("spread_bps", 0) for d in shadow)
print("① 影子漏斗: 事件=%d %s" % (len(shadow), dict(skip)))
print("   gap_gone率=%.0f%%(探测到REST间机会消失=被别人抢走) spread_wide率=%.0f%%" % (
    100.0 * skip.get("gap_gone", 0) / max(len(shadow), 1),
    100.0 * skip.get("spread_wide", 0) / max(len(shadow), 1)))
if ages:
    print("② 探测→REST延迟: 中位=%dms P90=%dms | REST时刻点差中位=%.1fbps" % (
        ages[len(ages) // 2], ages[int(len(ages) * 0.9)], spreads[len(spreads) // 2]))

# ---- 数据装载(影子首事件起) ----
t_lo = min(ep(d["recorded_at"]) for d in shadow) - 2100 if shadow else 0
liq_ts = []
with open(REC + "/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
            t = ep(d["recorded_at"])
        except (ValueError, KeyError):
            continue
        if t >= t_lo:
            liq_ts.append(t)
liq_ts.sort()
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
        if t < t_lo - 1900:
            continue
        b = int(t // 300)
        if b not in grid:
            try:
                grid[b] = json.loads(line).get("mids", {})
            except ValueError:
                continue
breadth = {}
for b in sorted(grid):
    old = grid.get(b - 6)
    if not old:
        continue
    m = grid[b]
    rets = [(m[s] - old[s]) / old[s] * 1e4 for s in m if s in old and old[s] > 0]
    if len(rets) >= 100:
        breadth[b] = statistics.median(rets)


def duty_is_xgap(t):
    n = bisect.bisect_right(liq_ts, t) - bisect.bisect_left(liq_ts, t - 300)
    if n >= 40:
        return False
    br = breadth.get(int(t // 300)) or breadth.get(int(t // 300) - 1)
    return br is not None and br > -15


paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 45].split('"')[0]) < t_lo:
                    continue
            except (ValueError, IndexError):
                pass
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            v = d.get("backlog_venue")
            tgt = anch[d["backlog_sym"]] if v == "bybit" else (paths[d["backlog_sym"]] if not v else None)
            if tgt is not None:
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= t_lo - 600:
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
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def a_at(sym, t, tol=8):
    arr = anch.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(ak[sym], t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


def gap_fresh(sym, t_in):
    arr = paths.get(sym)
    if not arr:
        return False
    lo = bisect.bisect_left(pk[sym], t_in - 1800)
    hi = bisect.bisect_left(pk[sym], t_in)
    n_ok, seen = 0, False
    for i in range(lo, hi):
        T, okx = arr[i]
        by = a_at(sym, T)
        if not by or by <= 0:
            continue
        n_ok += 1
        if (okx - by) / by * 1e4 > -20:
            seen = True
    return n_ok >= 10 and seen


# ---- ③ 可执行子集:影子 PASS 事件按 E 出场规则前向 ----
def sim_exit(sym, t_in, entry):
    arr = paths.get(sym)
    if not arr:
        return None
    j = bisect.bisect_left(pk[sym], t_in)
    out = None
    for k in range(j, len(arr)):
        T2, okx2 = arr[k]
        if T2 - t_in > 600:
            break
        by2 = a_at(sym, T2)
        ret = (okx2 - entry) / entry * 1e4
        out = ret
        if ret <= -60:
            break
        if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
            break
    return out


def stat(vals, lab):
    if not vals:
        print("   %-34s n=0" % lab)
        return
    pw = sum(x for x in vals if x > 0)
    pl = -sum(x for x in vals if x < 0)
    print("   %-34s n=%-3d 净中位=%+6.1f 净均=%+6.1f PF=%.2f" % (
        lab, len(vals), statistics.median(vals), sum(vals) / len(vals),
        pw / pl if pl > 0 else 99))


print("③ 可执行子集(影子PASS=真实REST mid 入场,模拟E出场):")
res_pass, res_cell = [], []
for d in shadow:
    if (d.get("gate_skip") or "PASS") != "PASS":
        continue
    t_in = ep(d["recorded_at"])
    sym = d["symbol"]
    entry = d.get("mid", 0)
    if entry <= 0:
        continue
    out = sim_exit(sym, t_in, entry)
    if out is None:
        continue
    net = out - COST
    res_pass.append(net)
    if gap_fresh(sym, t_in) and duty_is_xgap(t_in):
        res_cell.append(net)
stat(res_pass, "影子PASS全部(v6.5现有守卫口径)")
stat(res_cell, "影子PASS×新鲜度×派班(格子口径)")
print("④ 代码缺口审计: 实枪 fast_xgap_line.go 现有守卫=gap_gone+spread_wide;")
print("   【无新鲜度闸】【无派班闸】——若按格子口径开探针,须先补两闸+单测+护栏(探针前置工程项)。")
