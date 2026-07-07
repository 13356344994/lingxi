#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""司天监扩编研究会战(07-07 晚,用户"给你充足时间全面研究"授权;全部研究口径,零真钱)。

四项:①股票perp开盘收敛首测(定时州新候选;真股市开盘时刻套利商必把夜盘漂移拽回)
     ②E-Binance锚变体(lead-lag旧证据:Binance=最佳领先源;共识锚已死,单锚未考)
     ③E-现货基差变体(perp比自家现货便宜=纯杠杆流错位)
     ④#8b资费后崩 跨日一致性(预注册开考前的加固检查)
诚实口径:②③为挖掘(in-sample形状),④为已见数据加固,①样本极小(2-3天×几只股)——
全部只产"是否值得排队预注册"的判断,不产判决。
"""
import json
import bisect
import calendar
import collections
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
COST = 12.0

# ===== 复用 skywatch 计算(paths/grid/duty/liq) =====
src = open("/root/lingxi/records/_skywatch_report.py").read().split("# ---------- 文档式报告图 ----------")[0]
g = {"__name__": "research"}
exec(src, g)
ep, paths, pk, grid = g["ep"], g["paths"], g["pk"], g["grid"]
T0, NOW = g["T0"], g["NOW"]


def grid_px(sym, t):
    for b in (int(t // 300), int(t // 300) - 1):
        if b in grid:
            m = grid[b][1].get(sym)
            if m:
                return m
    return None


# ===== ① 股票perp开盘收敛首测 =====
print("=" * 72)
print("① 股票perp开盘收敛首测(隔夜漂移在开盘后延续还是回吐?)")
all_syms = set()
for b in grid:
    all_syms.update(grid[b][1].keys())
KR = [s for s in ("SAMSUNG-USDT", "SKHYNIX-USDT") if s in all_syms]
US = [s for s in ("SNDK-USDT", "AAPL-USDT", "SOXL-USDT", "TSLA-USDT", "NVDA-USDT",
                  "MSTR-USDT", "COIN-USDT", "CRCL-USDT", "HOOD-USDT", "META-USDT",
                  "AMZN-USDT", "GOOGL-USDT", "MCD-USDT", "ORCL-USDT") if s in all_syms]
print("  盘上股票perp: KR=%s US=%s" % ([s[:-5] for s in KR], [s[:-5] for s in US]))
# 开盘时刻(UTC):KR 00:00(收06:30) US 13:30(收20:00);窗口内的开盘日
day0 = int(T0 // 86400)
dayN = int(NOW // 86400)
events = []
for d in range(day0, dayN + 1):
    base = d * 86400
    wd = datetime.utcfromtimestamp(base).weekday()  # 0=Mon
    if wd < 5:
        for sym in KR:
            events.append((sym, base, base - 86400 + 6 * 3600 + 1800, "KR"))  # 开=00:00,昨收=昨06:30
        for sym in US:
            events.append((sym, base + 13 * 3600 + 1800, base - 86400 + 20 * 3600, "US"))  # 开=13:30,昨收=昨20:00
rows = []
for sym, t_open, t_close, mkt in events:
    if t_open < T0 + 300 or t_open > NOW - 3700:
        continue
    p_close = grid_px(sym, t_close)
    p_pre = grid_px(sym, t_open - 300)
    outs = [grid_px(sym, t_open + h) for h in (900, 1800, 3600)]
    if not (p_close and p_pre and all(outs)):
        continue
    drift = (p_pre - p_close) / p_close * 1e4  # 隔夜(收→开)漂移
    moves = [(o - p_pre) / p_pre * 1e4 for o in outs]  # 开盘后15/30/60m
    rows.append((sym, mkt, t_open, drift, moves))
print("  可测开盘事件=%d" % len(rows))
for sym, mkt, t_open, drift, mv in sorted(rows, key=lambda r: r[2]):
    print("  %s %-8s %s 隔夜=%+7.1f → 开后15m=%+7.1f 30m=%+7.1f 60m=%+7.1f  %s" % (
        mkt, sym[:-5], datetime.utcfromtimestamp(t_open).strftime("%m-%d %H:%M"),
        drift, mv[0], mv[1], mv[2],
        "回吐" if drift * mv[1] < 0 else "延续"))
if rows:
    big = [r for r in rows if abs(r[3]) >= 30]
    rev = sum(1 for r in big if r[3] * r[4][1] < 0)
    print("  |隔夜|≥30bps 的事件: n=%d 开后30m回吐=%d 延续=%d" % (len(big), rev, len(big) - rev))
    if big:
        fade = [-(r[4][1] if r[3] > 0 else -r[4][1]) - COST for r in big]  # 反向隔夜漂移,taker成本
        print("  「开盘反隔夜」净bps: 中位=%+.1f 均=%+.1f (n=%d,极小样本,只看形状)" % (
            statistics.median(fade), sum(fade) / len(fade), len(fade)))

# ===== ②③ E 变体:重新扫 hot_mids 取 binance/okxspot 锚 =====
print("=" * 72)
print("②③ E锚变体回放(48h,挖掘口径=in-sample形状;规则=E-v2同款含新鲜度)")
anchB = collections.defaultdict(list)   # binance
anchS = collections.defaultdict(list)   # okxspot
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        if '"anchors"' not in line and '"backlog_venue"' not in line:
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            v = d.get("backlog_venue")
            tgt = anchB if v == "binance" else (anchS if v == "okxspot" else None)
            if tgt is not None:
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= T0 - 2500:
                        tgt[d["backlog_sym"]].append((pt[0] / 1000.0, pt[1]))
            continue
        an = d.get("anchors") or {}
        for venue, tgt in (("binance", anchB), ("okxspot", anchS)):
            for sym, val in (an.get(venue) or {}).items():
                if isinstance(val, (list, tuple)) and len(val) >= 2:
                    tgt[sym].append((val[1] / 1000.0, val[0]))
for m in (anchB, anchS):
    for s in m:
        m[s] = sorted(set(m[s]))


def make_lookup(anch):
    keys = {s: [x[0] for x in arr] for s, arr in anch.items()}

    def a_at(sym, t, tol=8):
        arr = anch.get(sym)
        if not arr:
            return None
        i = bisect.bisect_right(keys[sym], t) - 1
        if i < 0 or t - arr[i][0] > tol:
            return None
        return arr[i][1]
    return a_at


def run_E_variant(anch, a_at, label):
    def fresh(sym, t_in):
        arr = paths.get(sym)
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
    trades, last = [], {}
    for sym, arr in paths.items():
        if sym not in anch or len(anch[sym]) < 10:
            continue
        n = len(arr)
        j = 0
        while j < n:
            T, okx = arr[j]
            if T < T0 or (sym in last and T - last[sym] < 1800):
                j += 1
                continue
            by = a_at(sym, T)
            if by and by > 0 and (okx - by) / by * 1e4 <= -40:
                if not fresh(sym, T):
                    last[sym] = T
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
                    trades.append((t_in, sym, out - COST))
                    last[sym] = t_in
            j += 1
    trades.sort()
    v = [x[2] for x in trades]
    if not v:
        print("  %-24s n=0" % label)
        return
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    c = collections.Counter(x[1] for x in trades)
    top, tn = c.most_common(1)[0]
    print("  %-24s n=%-4d 净中位=%+6.1f 净合计=%+7.0f PF=%.2f top=%s:%d(%d%%)" % (
        label, len(v), statistics.median(v), sum(v), pw / pl if pl > 0 else 99,
        top[:-5], tn, 100 * tn // len(v)))


run_E_variant(g["anch"], make_lookup(g["anch"]), "E×Bybit锚(现役,对照)")
run_E_variant(anchB, make_lookup(anchB), "E×Binance锚(变体)")
run_E_variant(anchS, make_lookup(anchS), "E×OKX现货锚(基差变体)")

# ===== ④ #8b 跨日一致性 =====
print("=" * 72)
print("④ #8b 资费后崩:极端负资费币 结算后5min,按日拆")
fund = collections.defaultdict(list)
for line in open(REC + "/fast_xmeta_snapshots.jsonl"):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    T = ep(d["recorded_at"])
    for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
        if isinstance(v, list) and len(v) >= 3 and v[2] > 0 and float(v[0]) <= -0.0005:
            fund[sym].append((T, float(v[0]), int(v[2])))
ev8 = {}
for sym, arr in fund.items():
    for T, f, nx in sorted(arr):
        st = nx / 1000.0
        if st - T <= 1800 and st > T:
            ev8[(sym, st)] = f
byday = collections.defaultdict(list)
for (sym, st), f in ev8.items():
    p0, p5, p15 = grid_px(sym, st), grid_px(sym, st + 300), grid_px(sym, st + 900)
    if not (p0 and p5 and p15):
        continue
    byday[datetime.utcfromtimestamp(st).strftime("%m-%d")].append(
        ((p5 - p0) / p0 * 1e4, (p15 - p0) / p0 * 1e4))
for day in sorted(byday):
    arr = byday[day]
    p5s = [x[0] for x in arr]
    p15s = [x[1] for x in arr]
    dn = 100 * sum(1 for x in p5s if x < 0) / len(p5s)
    print("  %s n=%-3d post5中位=%+7.1f(向下%3.0f%%) post15中位=%+7.1f" % (
        day, len(arr), statistics.median(p5s), dn, statistics.median(p15s)))
print("  注:grid 数据 07-05 07:06 起,故仅覆盖 05/06/07 三日;做空口径=post为负则赚。")
