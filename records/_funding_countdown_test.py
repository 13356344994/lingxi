#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""制度层假说#8 资费结算倒计时 首测(只读;候选③首个开测项)。

对手模型(预注册):极端负资费=空头每期给多头付钱,结算前空头有"不得不"的回补压力
→ 结算前买压(价格向上漂),结算一过压力释放(漂移停/回吐)。极端正资费镜像。
可预测性来自制度(结算时刻全网公开且固定),属"不得不"类。

数据与口径(如实):funding/nextFunding 取自 Bybit 富字段(xmeta 5min;OKX 自身 funding
未采集=数据缺口已登记),价格=OKX(hot_mids 3s 热币 + market_mids 3s 全市场[07-05 07:07Z起]);
路径覆盖有热币偏置,报覆盖率。
事件=每个 (币, 结算时刻);资费=结算前最后一个快照(≤30min);
  极端负 f≤-0.05% / 极端正 f≥+0.05% / 对照 |f|≤0.01%。
测量=pre15/pre5(结算前 15/5min → 结算)与 post5/post15 的 OKX mid 漂移(bps)。
预注册看点:极端桶 pre 漂移与对手模型同向且 |中位|≥8bps、胜率≥55%、对照≈0 → 值得深挖;
样本不足(桶 n<40)只报中间读数。
"""
import json
import bisect
import calendar
import collections
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- xmeta:Bybit funding 序列 + 结算事件 ----
fund = collections.defaultdict(list)   # sym -> [(t, f, next_ms)]
t_lo = t_hi = None
for line in open(REC + "/fast_xmeta_snapshots.jsonl"):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    T = ep(d["recorded_at"])
    t_lo = T if t_lo is None else min(t_lo, T)
    t_hi = T if t_hi is None else max(t_hi, T)
    for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
        if isinstance(v, list) and len(v) >= 3 and v[2] > 0:
            fund[sym].append((T, float(v[0]), int(v[2])))
for v in fund.values():
    v.sort()

events = []  # (sym, settle_epoch, f_last)
for sym, arr in fund.items():
    seen = set()
    for T, f, nx in arr:
        st = nx / 1000.0
        if st in seen or st < t_lo + 1800 or st > t_hi - 960:
            continue
        seen.add(st)
        # 结算前最后一个快照的 f(≤30min)
        best = None
        for T2, f2, nx2 in arr:
            if T2 <= st and st - T2 <= 1800 and nx2 / 1000.0 == st:
                best = f2
        if best is not None:
            events.append((sym, st, best))
print("xmeta 窗口(UTC): %s → %s | 结算事件=%d(币数=%d)" % (
    datetime.utcfromtimestamp(t_lo).strftime("%m-%d %H:%M"),
    datetime.utcfromtimestamp(t_hi).strftime("%m-%d %H:%M"),
    len(events), len({e[0] for e in events})))

# ---- 价格路径:hot_mids + market_mids 合并 ----
paths = collections.defaultdict(list)
for fn, hot in ((REC + "/fast_hot_mids.jsonl", True), (REC + "/fast_market_mids.jsonl", False)):
    try:
        f = open(fn)
    except IOError:
        continue
    with f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d.get("backlog_sym"):
                continue
            T = ep(d["recorded_at"])
            for sym, mid in d.get("mids", {}).items():
                paths[sym].append((T, mid))
for s in paths:
    paths[s] = sorted(set(paths[s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def px(sym, t, tol=90):
    arr = paths.get(sym)
    if not arr:
        return None
    keys = pk[sym]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


rows = []  # (f, pre15, pre5, post5, post15)
covered = 0
for sym, st, f in events:
    p_m15, p_m5, p0 = px(sym, st - 900), px(sym, st - 300), px(sym, st)
    p_p5, p_p15 = px(sym, st + 300), px(sym, st + 900)
    if not (p_m15 and p_m5 and p0 and p_p5 and p_p15):
        continue
    covered += 1
    rows.append((f,
                 (p0 - p_m15) / p_m15 * 1e4, (p0 - p_m5) / p_m5 * 1e4,
                 (p_p5 - p0) / p0 * 1e4, (p_p15 - p0) / p0 * 1e4))
print("有完整价格路径的结算事件=%d(覆盖率 %.0f%%;热币偏置存在)" % (covered, 100.0 * covered / max(1, len(events))))


def stat(sel, tag):
    if len(sel) == 0:
        print("  %-22s n=0" % tag)
        return
    cols = list(zip(*sel))
    out = ["  %-22s n=%-4d" % (tag, len(sel))]
    for name, arr in zip(("pre15", "pre5", "post5", "post15"), cols[1:]):
        med = statistics.median(arr)
        win = 100.0 * sum(1 for x in arr if x > 0) / len(arr)
        out.append("%s=%+6.1f(%3.0f%%↑)" % (name, med, win))
    print(" ".join(out))


print("\n== 按资费桶(bybit 每期费率;正=多头付) ==")
stat([r for r in rows if r[0] <= -0.0005], "极端负(f≤-0.05%)")
stat([r for r in rows if -0.0005 < r[0] <= -0.0001], "温和负")
stat([r for r in rows if abs(r[0]) < 0.0001], "对照(|f|<0.01%)")
stat([r for r in rows if 0.0001 <= r[0] < 0.0005], "温和正")
stat([r for r in rows if r[0] >= 0.0005], "极端正(f≥+0.05%)")
print("\n预注册看点:极端负桶 pre 漂移应为正(空头回补)、极端正桶 pre 应为负;|中位|≥8bps 且胜率≥55% 且对照≈0 → 值得深挖;否则制度层#8 首测不过。")
