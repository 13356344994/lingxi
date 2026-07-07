#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""E-v2「新鲜价差」前向判决(预注册 2026-07-07 12:15 BJ = 04:15Z,口径冻结不许改)。

与 E-v1 冻结版唯一差异 = 新鲜度条件(机制级过滤器,非黑名单):
  入场时刻回看该币过去 1800s 的价差序列,必须存在 ≥1 个时刻价差 > -20bps
  (即折价是窗口内"刚裂开的";摆了半小时没人搬的价差=搬砖通道已被市场证明堵塞,
   那不是错价是标价——LAB -328~-537 三小时不闭合、BLUR、LQTY 型自动剔除,无需名单)。
  fail-closed:回看窗内可算价差点 <10 个 → 视为信息不足,跳过(与实枪 fail-closed 哲学一致)。

其余规则与 E-v1 完全一致:便宜≥40 买OKX / 收敛≤10 或 600s 离场 / -60 灾难 / 同币 1800s 冷却 /
成本 12 / 刮痕|净|≤5 胜率中性化。
判决(与 E-v1 同门):SINCE 后新样本 n≥100 且 净中位>0 且 净PF≥1.3 且 top≤20% 且 成本16净均>0 → 过。
用法:python3 _xgap_v2_verdict.py [SINCE_ISO(默认预注册时刻 2026-07-07T04:15:00)]
"""
import json
import bisect
import calendar
import collections
import statistics
import sys
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-07T04:15:00"
COST = 12.0
FRESH_LOOKBACK = 1800.0  # s
FRESH_GAP_BPS = -20.0    # 回看窗内须出现过 > 此值的价差
FRESH_MIN_PTS = 10       # 回看窗内可算价差点下限(fail-closed)


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


t_since = ep(SINCE)
paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
# 数据加载:回看窗需要 SINCE 前 1800s 的路径,故放宽加载下限
load_floor = t_since - FRESH_LOOKBACK - 60
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 35].rstrip('"')) < load_floor:
                    continue
            except ValueError:
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
    keys = ak.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


def gap_fresh(sym, t_in):
    """回看 FRESH_LOOKBACK 内价差序列:出现过 > FRESH_GAP_BPS → fresh。
    可算点 < FRESH_MIN_PTS → (False, n) = fail-closed 跳过。"""
    arr = paths.get(sym)
    keys = pk.get(sym)
    if not arr:
        return False, 0
    lo = bisect.bisect_left(keys, t_in - FRESH_LOOKBACK)
    hi = bisect.bisect_left(keys, t_in)
    n_ok = 0
    seen_fresh = False
    for i in range(lo, hi):
        T, okx = arr[i]
        by = a_at(sym, T)
        if not by or by <= 0:
            continue
        n_ok += 1
        if (okx - by) / by * 1e4 > FRESH_GAP_BPS:
            seen_fresh = True
    if n_ok < FRESH_MIN_PTS:
        return False, n_ok
    return seen_fresh, n_ok


trades = []
skipped = collections.Counter()
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
                fresh, npts = gap_fresh(sym, T)
                if not fresh:
                    skipped["stale_gap" if npts >= FRESH_MIN_PTS else "fresh_data_short"] += 1
                    last[sym] = T  # 冷却照登记:与实枪一致,同一陈年价差不反复评
                    j += 1
                    continue
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
print("E-v2 判决集 since %s | n=%d | 新鲜度拦截=%s" % (SINCE, len(v), dict(skipped)))
if v:
    wins = [x for x in v if x > 5]
    losses = [x for x in v if x < -5]
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
