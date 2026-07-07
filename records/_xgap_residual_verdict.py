#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「残余带闸」前向判决(预注册 2026-07-07 20:10 BJ = 12:10Z,HANDOFF ㉝;口径冻结不许改)。

背景:执行层证据包发现回放(+11/PF1.78)与可执行(-5.6/PF0.73)相反,肉只在 REST 残余 20-40bps 带。
考题:影子线 PASS 事件(真实 REST mid 入场) × 格子口径(新鲜度+派班=xgap) × REST残余∈[-40,-20]
     → 模拟 E 出场(收敛10/600s/-60),成本12。
判决:封样后新影子事件 n≥30 且 净中位>0 且 净PF≥1.3 → 过检 → 探针前置工程(实枪补三闸)递用户;
     不过 → E 模块回炉,第一证候选让位 #8b。
用法:python3 _xgap_residual_verdict.py [SINCE_ISO(默认封样 2026-07-07T12:10:00)]
"""
import json
import re
import bisect
import calendar
import collections
import statistics
import subprocess
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T12:10:00"
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
load_floor = t_since - 2100

# ---- 影子事件(grep 预筛) ----
out = subprocess.run(["grep", '"candidate_source":"xgap_rev"', REC + "/fast_executable_shadow_events.jsonl"],
                     stdout=subprocess.PIPE, universal_newlines=True).stdout
shadow = []
for line in out.splitlines():
    try:
        d = json.loads(line)
    except ValueError:
        continue
    if ep(d["recorded_at"]) >= t_since:
        shadow.append(d)

# ---- 燃料/广度(派班口径,与格子阅卷同实现) ----
liq_ts = []
with open(REC + "/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
            t = ep(d["recorded_at"])
        except (ValueError, KeyError):
            continue
        if t >= load_floor - 1800:
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
        if t < load_floor - 1900:
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


# ---- 路径/锚(出场模拟与新鲜度) ----
paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 45].split('"')[0]) < load_floor:
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
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= load_floor - 600:
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


trades = []
skipped = collections.Counter()
for d in shadow:
    if (d.get("gate_skip") or "PASS") != "PASS":
        skipped[d.get("gate_skip")] += 1
        continue
    m = re.search(r"now(-?\d+)", d.get("fast_candidate_reason", "") or "")
    if not m:
        skipped["no_residual"] += 1
        continue
    residual = float(m.group(1))
    if not (-40 <= residual <= -20):
        skipped["residual_out_of_band"] += 1
        continue
    t_in = ep(d["recorded_at"])
    sym = d["symbol"]
    entry = d.get("mid", 0)
    if entry <= 0:
        continue
    if not gap_fresh(sym, t_in):
        skipped["stale_gap"] += 1
        continue
    if not duty_is_xgap(t_in):
        skipped["off_duty"] += 1
        continue
    out = sim_exit(sym, t_in, entry)
    if out is None:
        skipped["no_path"] += 1
        continue
    trades.append((t_in, sym, out - COST))

trades.sort()
v = [x[2] for x in trades]
print("残余带闸 判决集 since %s | n=%d | 拦截=%s" % (SINCE, len(v), dict(skipped)))
if v:
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    pf = pw / pl if pl > 0 else 99
    c = collections.Counter(x[1] for x in trades)
    top, tn = c.most_common(1)[0]
    med = statistics.median(v)
    print("净中位=%+.1f 净均=%+.1f 净PF=%.2f top=%s:%d(%d%%)" % (
        med, sum(v) / len(v), pf, top.replace("-USDT", ""), tn, 100 * tn // len(v)))
    if len(v) >= 30:
        ok = med > 0 and pf >= 1.3
        print("【到线】终审:%s" % ("过检 → 探针前置工程(实枪补三闸)递用户" if ok else "不过 → E模块回炉,第一证让位#8b"))
    else:
        print("进度 %d/30" % len(v))
