#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""跨所价差回归·多头腿 前向判决(预注册 2026-07-07 08:50 BJ = 00:50Z,口径冻结不许改)。

规则:OKX mid 比 Bybit 锚(新鲜度≤8s)便宜 ≥40bps → 买OKX;收敛≤10bps 或 600s 离场;-60 灾难;
     同币 1800s 去重。成本 12bps;刮痕(|净毛|≤5)从胜率中性化。
判决:SINCE 后新数据 n≥100 且 净中位>0 且 净PF≥1.3 且 非单币主导(top≤20%)→ 过;
     附加敏感性:成本16(错价时刻宽点差惩罚)下净均>0。
用法:python3 _xgap_reversion_verdict.py [SINCE_ISO(默认00:50Z)]
"""
import json
import bisect
import calendar
import collections
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T00:50:00"
COST = 12.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
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
                out = None
                for k in range(j + 1, n):
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
                if out is not None:
                    trades.append({"t": t_in, "sym": sym, "v": out})
                    last[sym] = t_in
        j += 1

v = [x["v"] - COST for x in trades]
print("判决集 since %s | n=%d" % (SINCE, len(v)))
if v:
    wins = [x["v"] - COST for x in trades if x["v"] - COST > 5]
    losses = [x["v"] - COST for x in trades if x["v"] - COST < -5]
    wr = 100 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    pf = pw / pl if pl > 0 else 99
    c = collections.Counter(x["sym"] for x in trades)
    top, tn = c.most_common(1)[0]
    med = statistics.median(v)
    mean16 = sum(x - 4 for x in v) / len(v)
    print("净中位=%+.1f 净均=%+.1f 胜(中性)=%.0f%% 净PF=%.2f top=%s:%d(%d%%) | 成本16敏感性净均=%+.1f" % (
        med, sum(v) / len(v), wr, pf, top.replace("-USDT", ""), tn, 100 * tn // len(v), mean16))
    if len(v) >= 100:
        ok = med > 0 and pf >= 1.3 and tn / len(v) <= 0.20 and mean16 > 0
        print("【到线】终审:%s" % ("过检 → 交用户裁探针" if ok else "不过"))
    else:
        print("进度 %d/100" % len(v))
