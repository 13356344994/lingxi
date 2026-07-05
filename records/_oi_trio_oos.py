#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""OI 三重奏样本外检验(只读;口径与 _combo_miner.py 逐行对齐,防"换尺子"污染)。

预注册(2026-07-05 01:44 UTC 挖掘完成即封样):
  组合 = 盘口失衡同向 × OI骤降(60s≤-1%) × 锚价差同向(≥10bps)
  过检 = 新数据上 顺向60s 中位 ≥ +8bps 且 赢率(≥15bps) ≥ 40%
  挖掘期读数 = n=112, +9.5bps, 47%(基线 -1.0 / 30%)
样本外窗 = SINCE(默认 2026-07-05T01:45,即挖掘完成之后)起的全部新数据。
样本充分性:挖掘期事件率 ≈12/h;n<80 只报中间读数不裁决(与 miner 的 stat 门一致)。
用法:python3 _oi_trio_oos.py [SINCE_ISO] [UNTIL_ISO]
v2(07-05 14:30,用户质询"NES单边行情"后加固):行带时间戳;新增**episode 去重口径**
(同币命中间隔<300s 视为同一事件,只记首笔)——防伪重复:趋势持续期条件逐秒连发,
26 个"命中"可能只是一段单边行情被采样 26 次。裁决一律以 episode 口径为准。
"""
import json
import sys
import datetime
import bisect
import collections
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-05T01:45"
UNTIL = sys.argv[2] if len(sys.argv) > 2 else "9999"


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


def tsf(s):
    return datetime.datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f")


# ---- 价格路径 + 锚(与 miner 相同;backlog 行不受 SINCE 限制,同 miner)----
paths = collections.defaultdict(list)
anch = collections.defaultdict(lambda: collections.defaultdict(list))
with open(os.path.join(DIR, "fast_hot_mids.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d["recorded_at"] < SINCE and not d.get("backlog_sym"):
            continue
        if d.get("backlog_sym"):
            tgt = anch[d["backlog_venue"]][d["backlog_sym"]] if d.get("backlog_venue") else paths[d["backlog_sym"]]
            for ms, mid in d.get("backlog", []):
                tgt.append((datetime.datetime.utcfromtimestamp(ms / 1000), mid))
            continue
        t = ts(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            paths[sym].append((t, mid))
        for v, m in (d.get("anchors") or {}).items():
            for sym, val in m.items():
                anch[v][sym].append((datetime.datetime.utcfromtimestamp(val[1] / 1000), val[0]))
for s in paths:
    paths[s] = sorted(set(paths[s]))
for v in anch:
    for s in anch[v]:
        anch[v][s] = sorted(set(anch[v][s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def fwd(sym, t, m0, secs):
    arr = paths.get(sym)
    if not arr or not m0:
        return None
    keys = pk[sym]
    i = bisect.bisect_left(keys, t + datetime.timedelta(seconds=secs))
    if i >= len(arr) or (arr[i][0] - t).total_seconds() > secs + 40:
        return None
    return (arr[i][1] - m0) / m0 * 10000


def series_at(arr, keys, t, tol):
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or (t - arr[i][0]).total_seconds() > tol:
        return None
    return arr[i][1]


# ---- 主扫描:hot_micro 活跃秒(样本定义与 miner 逐行一致,含 1/3 采样)----
state_vol = collections.defaultdict(lambda: collections.deque(maxlen=60))
rows = []
files = [os.path.join(DIR, "fast_hot_micro.jsonl.1"), os.path.join(DIR, "fast_hot_micro.jsonl")]
nscan = 0
for fn in files:
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            i = line.find('"recorded_at":"')
            if i < 0 or line[i + 15:i + 34] < SINCE or line[i + 15:i + 34] >= UNTIL:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            nscan += 1
            if nscan % 3 != 0:
                continue
            sym = d["symbol"]
            t = tsf(d["recorded_at"])
            mid = d.get("mid", 0)
            if mid <= 0:
                continue
            b, s_ = d.get("trade_buy_qty_1s", 0), d.get("trade_sell_qty_1s", 0)
            tot = b + s_
            hist = state_vol[sym]
            base = sorted(hist) if hist else []
            med = base[len(base) // 2] if base else 0
            hist.append(tot)
            m15 = series_at(paths[sym], pk[sym], t - datetime.timedelta(seconds=15), 30) if sym in paths else None
            r15 = (mid - m15) / m15 * 10000 if m15 else 0
            burst = med > 0 and tot >= med * 5 and tot * mid >= 500
            if burst:
                side = 1 if b > s_ else -1
            elif abs(r15) >= 5:
                side = 1 if r15 > 0 else -1
            else:
                continue
            if d.get("trade_count_1s", 0) == 0 and not burst:
                continue
            f60 = fwd(sym, t, mid, 60)
            if f60 is None:
                continue
            oi_d = d.get("oi_delta_60s", 0)
            oi_now = d.get("oi_now", 0)
            imb = d.get("book_imbalance_now", 0)
            gaps = []
            for v in ("bybit", "binance", "gate"):
                a = anch[v].get(sym)
                if a:
                    av = series_at(a, [x[0] for x in a], t, 10)
                    if av:
                        gaps.append((mid - av) / av * 10000)
            gap = sorted(gaps)[len(gaps) // 2] if gaps else None
            rows.append((
                side * f60,
                imb * side > 0.5,                       # 盘口失衡同向
                oi_now > 0 and oi_d < -0.01 * oi_now,   # OI骤降
                gap is not None and gap * side >= 10,   # 锚价差同向
                sym,
                t,
            ))

print("样本外窗起点(UTC):", SINCE, "| 扫描", nscan, "行 → 决策时刻", len(rows), "个")
if not rows:
    sys.exit("无样本")


def stat(sel, tag, top_check=True):
    v = sorted(r[0] for r in sel)
    if not v:
        print("  %-36s n=0" % tag)
        return
    med = v[len(v) // 2]
    win = 100.0 * sum(1 for x in v if x >= 15) / len(v)
    syms = collections.Counter(r[4] for r in sel)
    top_sym, top_n = syms.most_common(1)[0] if syms else ("-", 0)
    flag = ""
    if top_check and len(v) > 0 and top_n / len(v) > 0.5:
        flag = "  ⚠单币主导:" + top_sym
    print("  %-36s n=%-5d 中位=%+6.1fbps 赢率(>=15)=%3.0f%%%s" % (tag, len(v), med, win, flag))


def dedup_episodes(sel, gap_sec=300):
    """同币命中间隔<gap_sec 归并为一个 episode,只保留首笔(信息集最早时刻)。"""
    sel = sorted(sel, key=lambda r: r[5])
    last = {}
    out = []
    for r in sel:
        t0 = last.get(r[4])
        if t0 is None or (r[5] - t0).total_seconds() >= gap_sec:
            out.append(r)
            last[r[4]] = r[5]
    return out


print("\n== 基线(全部决策时刻)==")
stat(rows, "基线", top_check=False)
print("\n== 单成分 ==")
stat([r for r in rows if r[1]], "盘口失衡同向")
stat([r for r in rows if r[2]], "OI骤降(60s<=-1%)")
stat([r for r in rows if r[3]], "锚价差同向(>=10bps)")
trio = [r for r in rows if r[1] and r[2] and r[3]]
print("\n== 预注册三重奏(逐秒原始口径,含伪重复,仅对照用)==")
stat(trio, "失衡×OI骤降×锚同向(raw)")
print("\n== 三重奏 episode 口径(同币300s归并;裁决口径:中位>=+8 且 赢率>=40% 且非单币主导 且 n>=30 episode)==")
epi = dedup_episodes(trio)
stat(epi, "失衡×OI骤降×锚同向(episode)")
print("\nepisode 明细(时刻 币 顺向60s):")
for r in epi:
    print("  %s  %-14s %+7.1fbps" % (r[5].strftime("%m-%d %H:%M:%S"), r[4], r[0]))
