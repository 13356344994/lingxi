#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""制度层假说#8 资费结算倒计时 消偏复测(预注册的一次性复测;07-05 首测判"测量有热币偏置")。

复测条件(07-05 16:30 登记):market_mids 全市场落盘攒≥24h 后消偏重跑;
判决(预注册,不许改):极端桶 n≥40 仍无边界拐点(pre 漂移与对手模型同向且 |中位|≥8bps
且胜率≥55% 且对照≈0)→ #8 判死。
与首测 `_funding_countdown_test.py` 唯一实现差异 = 价格路径按 60s 降采样加载
(测量容忍 tol=90s 不变,pre/post 全是 5-15min 尺度,语义无损;否则两天全市场点位撑爆内存)。
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


# ---- xmeta:Bybit funding 序列 + 结算事件(与首测同口径) ----
fund = collections.defaultdict(list)
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

events = []
for sym, arr in fund.items():
    seen = set()
    for T, f, nx in arr:
        st = nx / 1000.0
        if st in seen or st < t_lo + 1800 or st > t_hi - 960:
            continue
        seen.add(st)
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

# ---- 价格路径:hot_mids + market_mids 合并,60s 降采样(tol=90s 语义无损) ----
paths = collections.defaultdict(list)
seen_min = collections.defaultdict(set)  # sym -> set(minute) 双源去重
for fn in (REC + "/fast_market_mids.jsonl", REC + "/fast_hot_mids.jsonl"):
    try:
        f = open(fn)
    except IOError:
        continue
    last_min_all = -1
    with f:
        for line in f:
            p = line.find('"recorded_at":"')
            if p < 0 or '"backlog' in line:
                continue
            try:
                T = ep(line[p + 15:p + 45].split('"')[0])
            except (ValueError, IndexError):
                continue
            mn = int(T // 60)
            if fn.endswith("fast_market_mids.jsonl"):
                if mn == last_min_all:
                    continue  # 整行级降采样(每分钟第一行)
                last_min_all = mn
            try:
                d = json.loads(line)
            except ValueError:
                continue
            for sym, mid in d.get("mids", {}).items():
                if mn in seen_min[sym]:
                    continue
                seen_min[sym].add(mn)
                paths[sym].append((T, mid))
for s in paths:
    paths[s].sort()
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


rows = []
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
print("有完整价格路径的结算事件=%d(覆盖率 %.0f%%;首测仅20%%)" % (covered, 100.0 * covered / max(1, len(events))))


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
neg_x = [r for r in rows if r[0] <= -0.0005]
pos_x = [r for r in rows if r[0] >= 0.0005]
ctrl = [r for r in rows if abs(r[0]) < 0.0001]
stat(neg_x, "极端负(f≤-0.05%)")
stat([r for r in rows if -0.0005 < r[0] <= -0.0001], "温和负")
stat(ctrl, "对照(|f|<0.01%)")
stat([r for r in rows if 0.0001 <= r[0] < 0.0005], "温和正")
stat(pos_x, "极端正(f≥+0.05%)")

# ---- 预注册终审 ----
print("\n== 终审(预注册:极端桶 n≥40 无边界拐点 → #8 判死) ==")
verdicts = []
for tag, sel, want in (("极端负", neg_x, +1), ("极端正", pos_x, -1)):
    if len(sel) < 40:
        verdicts.append("%s n=%d<40 样本不足" % (tag, len(sel)))
        continue
    pre5 = [r[2] for r in sel]
    med = statistics.median(pre5)
    win = 100.0 * sum(1 for x in pre5 if x * want > 0) / len(pre5)
    ok = med * want >= 8 and win >= 55
    verdicts.append("%s n=%d pre5中位=%+.1f 同向率=%.0f%% → %s" % (tag, len(sel), med, win, "有信号" if ok else "无信号"))
ctrl_note = ""
if ctrl:
    cm = statistics.median([r[2] for r in ctrl])
    ctrl_note = " | 对照 pre5 中位=%+.1f(应≈0)" % cm
for v in verdicts:
    print("  " + v)
print("  " + ("对照桶热币偏置检查:" + ctrl_note if ctrl_note else ""))
if all(("无信号" in v or "样本不足" in v) for v in verdicts) and all("样本不足" not in v for v in verdicts):
    print("  【终审】两极端桶均无边界拐点 → 制度层假说#8 判死(预注册条款)")
