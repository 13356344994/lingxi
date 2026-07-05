#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""组合挖掘(只读):从"每时每刻的结果"倒推——全特征向量 × 组合命中率。

样本 = hot_micro 的每个活跃秒(有成交且有方向读数的时刻);
方向 = burst 方向,否则动量方向(|r15|≥5bps);
标签 = 顺方向 60s 收益(fwd60_side),赢=≥+15bps(≈过费后有肉);
特征 = 全部图层的布尔化(该秒实时可算,信息集对齐);
输出 = 单特征 lift 表 + 两两组合 top 表(n≥80,按中位收益排序)。
防呆:组合是"倒向生成",报告即用同数据前向收益验证;周一须在新数据上复测防过拟合。
"""
import json
import datetime
import bisect
import collections
import itertools
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
SINCE = "2026-07-04T16:00"


def ts(s):
    return datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


def tsf(s):
    return datetime.datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f")


# ---- 价格路径 + 锚 ----
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


# ---- 爆仓(两所)与燃料 ----
liq = collections.defaultdict(list)
liq_all = []
with open(os.path.join(DIR, "fast_liquidation_events.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d["recorded_at"] < SINCE:
            continue
        t = tsf(d["recorded_at"])
        liq[d["symbol"]].append(t)
        liq_all.append(t)
liq_all.sort()
xliq = collections.defaultdict(list)
with open(os.path.join(DIR, "fast_xvenue_liq_events.jsonl")) as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d["recorded_at"] < SINCE:
            continue
        xliq[d["symbol"]].append(tsf(d["recorded_at"]))
for m in (liq, xliq):
    for s in m:
        m[s].sort()


def recent(arr, t, sec):
    i = bisect.bisect_left(arr, t - datetime.timedelta(seconds=sec))
    return i < len(arr) and arr[i] <= t


def fuel(t):
    lo = bisect.bisect_left(liq_all, t - datetime.timedelta(seconds=300))
    return bisect.bisect_right(liq_all, t) - lo


# ---- 年轻币 ----
meta = json.load(open(os.path.join(DIR, "instrument_meta.json")))["instruments"]
now_ms = datetime.datetime.utcnow().timestamp() * 1000
young = {s for s, v in meta.items() if v.get("list_time_ms", 0) > 0 and (now_ms - v["list_time_ms"]) < 30 * 86400000}

# ---- 主扫描:hot_micro 活跃秒 ----
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
            if i < 0 or line[i + 15:i + 34] < SINCE:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            nscan += 1
            if nscan % 3 != 0:  # 1/3 采样控制计算量(60s 去重后偏差可忽略)
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
            r15 = d.get("mid_ret_1s", 0) * 0  # 占位
            # 动量:用 paths 15s 回看
            m15 = series_at(paths[sym], pk[sym], t - datetime.timedelta(seconds=15), 30) if sym in paths else None
            r15 = (mid - m15) / m15 * 10000 if m15 else 0
            burst = med > 0 and tot >= med * 5 and tot * mid >= 500
            if burst:
                side = 1 if b > s_ else -1
            elif abs(r15) >= 5:
                side = 1 if r15 > 0 else -1
            else:
                continue  # 无方向读数的时刻不进样本
            if d.get("trade_count_1s", 0) == 0 and not burst:
                continue
            f60 = fwd(sym, t, mid, 60)
            if f60 is None:
                continue
            oi_d = d.get("oi_delta_60s", 0)
            oi_now = d.get("oi_now", 0)
            imb = d.get("book_imbalance_now", 0)
            spread = d.get("spread_bps", 99)
            # 锚价差(该秒,ts 对齐 ≤10s)
            gaps = []
            for v in ("bybit", "binance", "gate"):
                a = anch[v].get(sym)
                if a:
                    av = series_at(a, [x[0] for x in a], t, 10)
                    if av:
                        gaps.append((mid - av) / av * 10000)
            gap = sorted(gaps)[len(gaps) // 2] if gaps else None
            feats = {
                "burst5x": burst,
                "burst大额(≥2kU)": burst and tot * mid >= 2000,
                "本币OKX爆仓15s": recent(liq.get(sym, []), t, 15),
                "本币Bybit爆仓15s": recent(xliq.get(sym, []), t, 15),
                "高燃料(≥40)": fuel(t) >= 40,
                "动量强(|r15|≥15)": abs(r15) >= 15,
                "盘口失衡同向": imb * side > 0.5,
                "OI骤降(60s≤-1%)": oi_now > 0 and oi_d < -0.01 * oi_now,
                "点差可执行(≤12)": spread <= 12,
                "年轻币(<30d)": sym in young,
                "锚价差同向(≥10bps)": gap is not None and gap * side >= 10,
            }
            rows.append((side * f60, feats, sym))

print(f"扫描 {nscan} 行 → 有方向读数的决策时刻 {len(rows)} 个")
base_v = sorted(x[0] for x in rows)
base_med = base_v[len(base_v) // 2]
base_win = sum(1 for x in base_v if x >= 15) / len(base_v)
print(f"基线: 顺向60s 中位={base_med:+.1f}bps  赢率(≥15bps)={base_win*100:.0f}%\n")

names = list(rows[0][1].keys())


def stat(sel, tag):
    v = sorted(r[0] for r in sel)
    if len(v) < 80:
        return None
    med = v[len(v) // 2]
    win = sum(1 for x in v if x >= 15) / len(v)
    syms = collections.Counter(r[2] for r in sel)
    top_sym, top_n = syms.most_common(1)[0]
    if top_n / len(v) > 0.5:
        return None  # 单币主导的组合丢弃
    return (med, win, len(v), tag, dict(syms.most_common(2)))


print("== 单特征 lift ==")
singles = []
for nm in names:
    r = stat([x for x in rows if x[1][nm]], nm)
    if r:
        singles.append(r)
for med, win, n, tag, tops in sorted(singles, reverse=True):
    print(f"  {tag:24s} n={n:6d} 中位={med:+6.1f} 赢率={win*100:3.0f}%  {tops}")

print("\n== 两两组合 top10(按中位,n≥80,非单币主导) ==")
combos = []
for a, b in itertools.combinations(names, 2):
    r = stat([x for x in rows if x[1][a] and x[1][b]], f"{a} × {b}")
    if r:
        combos.append(r)
for med, win, n, tag, tops in sorted(combos, reverse=True)[:10]:
    print(f"  {tag:44s} n={n:5d} 中位={med:+6.1f} 赢率={win*100:3.0f}%  {tops}")

print("\n== 三特征组合 top8(n≥80) ==")
tris = []
for a, b, c in itertools.combinations(names, 3):
    r = stat([x for x in rows if x[1][a] and x[1][b] and x[1][c]], f"{a}×{b}×{c}")
    if r:
        tris.append(r)
for med, win, n, tag, tops in sorted(tris, reverse=True)[:8]:
    print(f"  {tag:58s} n={n:5d} 中位={med:+6.1f} 赢率={win*100:3.0f}%  {tops}")
