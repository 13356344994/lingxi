#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「司天监」v0.1 两日战绩报告(文档式图;record-only,零真钱)。

编制(值班表 v0.1 冻结):
  风暴跌潮(fuel>=40,ebb)   → 火灾瀑布刀(程序S:确认火burst→t0+1s空,trail10/20,-40灾难,二波止错,cap120)
  风暴涨潮(fuel>=40,flood) → 烟花反手刀(力竭×拉升>=60×点差<=6→反手空,固定300s,-40灾难)
  平静海(fuel<40,广度>-15) → E捡便宜货v2(便宜>=40+新鲜度→买OKX,收敛10/600s/-60)
  其余                     → ⓪歇业(fail-closed)
成本一律 12bps。参数全部冻结先验,本回放零调参。
诚实口径:窗口与规则发现窗重叠,发现窗段只看形状;封样线右侧才是干净段。
"""
import json
import bisect
import calendar
import collections
import statistics
import time
from datetime import datetime

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.dates as mdates
import matplotlib.font_manager as fm

_font = "/root/lingxi/records/fonts/NotoSansCJKsc-Regular.otf"
fm.fontManager.addfont(_font)
plt.rcParams["font.family"] = fm.FontProperties(fname=_font).get_name()
plt.rcParams["axes.unicode_minus"] = False

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
NOW = time.time()
T0 = max(NOW - 48 * 3600,
         calendar.timegm(time.strptime("2026-07-05T07:10:00", "%Y-%m-%dT%H:%M:%S")))
SEAL_V1 = calendar.timegm(time.strptime("2026-07-07T00:50:00", "%Y-%m-%dT%H:%M:%S"))
SEAL_V2 = calendar.timegm(time.strptime("2026-07-07T04:15:00", "%Y-%m-%dT%H:%M:%S"))


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---------- 数据装载 ----------
liq_ts, liq_long, liq_short = [], [], []
liq_by_sym = collections.defaultdict(list)
with open(REC + "/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
            t = ep(d["recorded_at"])
        except (ValueError, KeyError):
            continue
        if t < T0 - 1900:
            continue
        liq_ts.append(t)
        ps = d.get("pos_side")
        if ps == "long":
            liq_long.append(t)
        elif ps == "short":
            liq_short.append(t)
        liq_by_sym[d.get("symbol", "")].append((t, ps))
liq_ts.sort(); liq_long.sort(); liq_short.sort()
for s in liq_by_sym:
    liq_by_sym[s].sort()


def cnt(arr, lo, hi):
    return bisect.bisect_right(arr, hi) - bisect.bisect_left(arr, lo)


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
        if t < T0 - 1900:
            continue
        b = int(t // 300)
        if b in grid:
            continue
        try:
            grid[b] = (t, json.loads(line).get("mids", {}))
        except ValueError:
            continue
breadth = {}
for b in sorted(grid):
    t, mids = grid[b]
    old = grid.get(b - 6)
    if not old:
        continue
    m_old = old[1]
    rets = [(mids[s] - m_old[s]) / m_old[s] * 1e4 for s in mids if s in m_old and m_old[s] > 0]
    if len(rets) >= 100:
        breadth[b] = statistics.median(rets)


def duty_at(t):
    fuel = cnt(liq_ts, t - 300, t)
    ebb = cnt(liq_long, t - 1800, t)
    flood = cnt(liq_short, t - 1800, t)
    if fuel >= 40:
        if ebb >= 2 * max(flood, 1) and ebb >= 5:
            return "fire"
        if flood >= 2 * max(ebb, 1) and flood >= 5:
            return "firework"
        return "none"
    br = breadth.get(int(t // 300)) or breadth.get(int(t // 300) - 1)
    if br is None:
        return "none"
    return "xgap" if br > -15 else "none"


paths = collections.defaultdict(list)
anch = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0 and '"backlog' not in line:
            try:
                if ep(line[p + 15:p + 45].split('"')[0]) < T0 - 1900:
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
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2 and pt[0] / 1000.0 >= T0 - 2500:
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
    arr = anch.get(sym); keys = ak.get(sym)
    if not arr:
        return None
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > tol:
        return None
    return arr[i][1]


def gap_fresh(sym, t_in):
    arr = paths.get(sym); keys = pk.get(sym)
    if not arr:
        return False
    lo = bisect.bisect_left(keys, t_in - 1800)
    hi = bisect.bisect_left(keys, t_in)
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


# ---------- 引擎E:捡便宜货 v2 ----------
def run_E(gated):
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
                if not gap_fresh(sym, T) or (gated and duty_at(T) != "xgap"):
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
    return trades


# ---------- 引擎F:火灾瀑布刀(程序S) ----------
def run_fire(gated):
    trades, last = [], {}
    with open(REC + "/fast_burst_detect.jsonl") as f:
        for line in f:
            try:
                d = json.loads(line)
                t0 = ep(d["recorded_at"])
            except (ValueError, KeyError):
                continue
            if t0 < T0 or d.get("side") != "sell":
                continue
            sym = d["symbol"]
            if sym in last and t0 - last[sym] < 120:
                continue
            conf = [ps for (tt, ps) in liq_by_sym.get(sym, []) if t0 < tt <= t0 + 3]
            if not conf or sum(1 for x in conf if x == "long") * 2 < len(conf):
                continue
            if gated and duty_at(t0) != "fire":
                last[sym] = t0
                continue
            arr = paths.get(sym); keys = pk.get(sym)
            if not arr:
                continue
            i = bisect.bisect_left(keys, t0 + 1.0)
            if i >= len(arr) or arr[i][0] - t0 > 5:
                continue
            entry, t_in = arr[i][1], arr[i][0]
            wave2 = any(t0 + 3 < tt <= t0 + 20 for (tt, _) in liq_by_sym.get(sym, []))
            peak, out = 0.0, None
            for k in range(i + 1, len(arr)):
                T2, px = arr[k]
                if T2 - t_in > 120:
                    break
                ret = (entry - px) / entry * 1e4
                out = ret
                peak = max(peak, ret)
                if ret <= -40 or (peak >= 10 and ret <= peak - 20) or (not wave2 and T2 - t0 >= 20):
                    break
            if out is not None:
                trades.append((t_in, sym, out - COST))
                last[sym] = t0
    trades.sort()
    return trades


# ---------- 引擎W:烟花反手刀(力竭×拉升>=60×点差<=6,固定300s,-40灾难) ----------
def run_firework(gated):
    trades, last = [], {}
    with open(REC + "/fast_mover_census.jsonl") as f:
        for line in f:
            try:
                d = json.loads(line)
                t0 = ep(d["recorded_at"])
            except (ValueError, KeyError):
                continue
            if t0 < T0:
                continue
            r60 = d.get("ret_60s_bps", 0)
            r15 = d.get("ret_15s_bps", 0)
            sp = d.get("spread_bps", 99)
            if r60 < 60 or r15 >= 0.25 * r60 or sp > 6:
                continue  # 需上冲>=60 且 15s力竭 且 点差<=6
            sym = d["symbol"]
            if sym in last and t0 - last[sym] < 300:
                continue
            if gated and duty_at(t0) != "firework":
                last[sym] = t0
                continue
            arr = paths.get(sym); keys = pk.get(sym)
            if not arr:
                continue
            i = bisect.bisect_left(keys, t0)
            if i >= len(arr) or arr[i][0] - t0 > 5:
                continue
            entry, t_in = arr[i][1], arr[i][0]
            out, cov = None, 0.0
            for k in range(i + 1, len(arr)):
                T2, px = arr[k]
                if T2 - t_in > 300:
                    break
                ret = (entry - px) / entry * 1e4  # short
                out, cov = ret, T2 - t_in
                if ret <= -40:
                    break
            if out is not None and (cov >= 240 or out <= -40):
                trades.append((t_in, sym, out - COST))
                last[sym] = t0
    trades.sort()
    return trades


def S(trades):
    if not trades:
        return dict(n=0, med=0, tot=0, pf=0, wr=0)
    v = [x[2] for x in trades]
    pw = sum(x for x in v if x > 0); pl = -sum(x for x in v if x < 0)
    wins = [x for x in v if x > 5]; losses = [x for x in v if x < -5]
    wr = 100 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    return dict(n=len(v), med=statistics.median(v), tot=sum(v),
                pf=(pw / pl if pl > 0 else 99), wr=wr)


e_g = run_E(True); f_g = run_fire(True); w_g = run_firework(True)
e_u = run_E(False); f_u = run_fire(False); w_u = run_firework(False)
combo = sorted(e_g + f_g + w_g)
rows = [
    ("E 捡便宜货(值班内)", e_g, "#2a9d2a"),
    ("火灾瀑布刀(值班内)", f_g, "#d62728"),
    ("烟花反手刀(值班内)", w_g, "#e8850c"),
    ("司天监 合计", combo, "#000000"),
]
base_rows = [("E(无派班对照)", e_u), ("火刀(无派班对照)", f_u), ("烟花(无派班对照)", w_u)]
for lab, tr, _ in rows:
    st = S(tr)
    print("%-18s n=%-4d med=%+7.1f tot=%+8.0f PF=%5.2f wr=%3.0f%%" % (lab, st["n"], st["med"], st["tot"], st["pf"], st["wr"]))
for lab, tr in base_rows:
    st = S(tr)
    print("%-18s n=%-4d med=%+7.1f tot=%+8.0f PF=%5.2f wr=%3.0f%%" % (lab, st["n"], st["med"], st["tot"], st["pf"], st["wr"]))

seg = [("发现窗(<00:50Z)", [x for x in combo if x[0] < SEAL_V1]),
       ("血窗段(00:50-04:15Z)", [x for x in combo if SEAL_V1 <= x[0] < SEAL_V2]),
       ("干净段(>=04:15Z)", [x for x in combo if x[0] >= SEAL_V2])]

# ---------- 文档式报告图 ----------
fig = plt.figure(figsize=(12.5, 15.5), facecolor="white")
gs = fig.add_gridspec(5, 1, height_ratios=[1.15, 1.35, 3.2, 0.9, 1.15],
                      hspace=0.42, left=0.07, right=0.95, top=0.955, bottom=0.045)

# — 头部 —
axh = fig.add_subplot(gs[0]); axh.axis("off")
axh.text(0, 0.95, "「司天监」两日战绩回放", fontsize=23, weight="bold", va="top")
axh.text(0, 0.52, "窗口 %s → %s UTC   ·   成本 12bps   ·   全参数冻结先验、窗内零调参   ·   record-only 零真钱" % (
    datetime.utcfromtimestamp(T0).strftime("%m-%d %H:%M"),
    datetime.utcfromtimestamp(NOW).strftime("%m-%d %H:%M")), fontsize=11, va="top", color="#333333")
axh.text(0, 0.22, "架构:读天(燃料×潮汐×广度) → 派班(风暴跌潮=火刀 / 风暴涨潮=烟花刀 / 平静海=E捡便宜货 / 其余=⓪歇业) → 刹车凌驾一切",
         fontsize=10.5, va="top", color="#555555")

# — 战绩总表 —
axt = fig.add_subplot(gs[1]); axt.axis("off")
axt.set_title("一、战绩总表(值班内 vs 无派班对照)", fontsize=13, weight="bold", loc="left", pad=8)
tbl_rows = []
for lab, tr, _ in rows:
    st = S(tr)
    tbl_rows.append([lab, "%d" % st["n"], "%+.1f" % st["med"], "%+.0f" % st["tot"], "%.2f" % st["pf"], "%.0f%%" % st["wr"]])
for lab, tr in base_rows:
    st = S(tr)
    tbl_rows.append([lab, "%d" % st["n"], "%+.1f" % st["med"], "%+.0f" % st["tot"], "%.2f" % st["pf"], "%.0f%%" % st["wr"]])
table = axt.table(cellText=tbl_rows,
                  colLabels=["引擎", "笔数", "净中位(bps)", "净合计(bps)", "净PF", "胜率"],
                  colWidths=[0.30, 0.10, 0.15, 0.15, 0.12, 0.10],
                  loc="upper left", cellLoc="center")
table.auto_set_font_size(False)
table.set_fontsize(10.5)
table.scale(1, 1.55)
for (r, c), cell in table.get_celld().items():
    cell.set_edgecolor("#cccccc")
    if r == 0:
        cell.set_facecolor("#2c3e50"); cell.set_text_props(color="white", weight="bold")
    elif r == 4:
        cell.set_facecolor("#f5e9c8"); cell.set_text_props(weight="bold")
    elif r > 4:
        cell.set_text_props(color="#999999")

# — 累计曲线 —
axc = fig.add_subplot(gs[2])
axc.set_title("二、累计净值曲线(bps)", fontsize=13, weight="bold", loc="left", pad=8)
def curve(ax, trades, label, color, lw=1.6, ls="-", alpha=1.0):
    if not trades:
        return
    xs = [datetime.utcfromtimestamp(x[0]) for x in trades]
    s, ys = 0.0, []
    for x in trades:
        s += x[2]; ys.append(s)
    ax.plot(xs, ys, label=label, color=color, lw=lw, ls=ls, alpha=alpha)
curve(axc, e_u, "旧E(无派班无新鲜度)对照", "#aaaaaa", 1.2, "--")
for lab, tr, col in rows:
    curve(axc, tr, lab, col, 2.6 if "合计" in lab else 1.7)
for cut, lab in ((SEAL_V1, "E-v1 封样 00:50Z"), (SEAL_V2, "E-v2 封样 04:15Z")):
    axc.axvline(datetime.utcfromtimestamp(cut), color="#3355cc", ls=":", lw=1.4)
    axc.text(datetime.utcfromtimestamp(cut), axc.get_ylim()[1], " " + lab, rotation=90,
             va="top", fontsize=8.5, color="#3355cc")
axc.axhline(0, color="black", lw=0.6)
axc.legend(loc="upper left", fontsize=9.5, framealpha=0.9)
axc.grid(alpha=0.25)
axc.set_ylabel("累计净 bps")
axc.xaxis.set_major_formatter(mdates.DateFormatter("%m-%d %H:%M"))

# — 派班时间带 —
axd = fig.add_subplot(gs[3], sharex=axc)
axd.set_title("三、派班时间带(读天结果)", fontsize=13, weight="bold", loc="left", pad=8)
ts = T0
xs, ys = [], []
while ts <= NOW:
    xs.append(datetime.utcfromtimestamp(ts))
    ys.append({"none": 0, "xgap": 1, "firework": 2, "fire": 3}[duty_at(ts)])
    ts += 300
colors = {0: "#dddddd", 1: "#2a9d2a", 2: "#e8850c", 3: "#d62728"}
for i in range(len(xs) - 1):
    axd.axvspan(xs[i], xs[i + 1], ymin=0, ymax=1, color=colors[ys[i]], alpha=0.75)
axd.set_yticks([])
axd.set_xlim(xs[0], xs[-1])
import matplotlib.patches as mpatches
axd.legend(handles=[mpatches.Patch(color=colors[0], label="⓪歇业"),
                    mpatches.Patch(color=colors[1], label="E值班"),
                    mpatches.Patch(color=colors[2], label="烟花刀值班"),
                    mpatches.Patch(color=colors[3], label="火刀值班")],
           loc="upper left", fontsize=8.5, ncol=4, framealpha=0.9)
axd.xaxis.set_major_formatter(mdates.DateFormatter("%m-%d %H:%M"))

# — 分段诚实核对 —
axs = fig.add_subplot(gs[4]); axs.axis("off")
axs.set_title("四、分段诚实核对(哪段算数)", fontsize=13, weight="bold", loc="left", pad=8)
seg_rows = []
notes = ["规则发现窗:新鲜度/广度闸的灵感来源,只看形状不算成绩",
         "今晨血窗:记住答案浓度最高,仅证明\"没在别人的班上流血\"",
         "封样后前向:唯一有效力的段,样本刚开始攒"]
for (lab, tr), note in zip(seg, notes):
    st = S(tr)
    seg_rows.append([lab, "%d" % st["n"], "%+.1f" % st["med"], "%+.0f" % st["tot"],
                     ("%.2f" % st["pf"]) if st["n"] else "-", note])
t2 = axs.table(cellText=seg_rows,
               colLabels=["时段", "笔数", "净中位", "净合计", "PF", "诚实标注"],
               colWidths=[0.16, 0.06, 0.09, 0.09, 0.07, 0.53],
               loc="upper left", cellLoc="center")
t2.auto_set_font_size(False)
t2.set_fontsize(10)
t2.scale(1, 1.6)
for (r, c), cell in t2.get_celld().items():
    cell.set_edgecolor("#cccccc")
    if r == 0:
        cell.set_facecolor("#2c3e50"); cell.set_text_props(color="white", weight="bold")
    if c == 5:
        cell.set_text_props(fontsize=9, color="#666666")
        cell.get_text().set_ha("left")
fig.text(0.07, 0.012, "生成 %s UTC · 数据源=13流盘上原始记录 · 真正的成绩单=E-v2前向判决(04:15Z封样)与元分拣器状态×族格子表 · 实盘开关永远等用户拍板" % (
    datetime.utcfromtimestamp(NOW).strftime("%Y-%m-%d %H:%M")), fontsize=9, color="#888888")

out_png = "/root/lingxi/records/skywatch_report_%s.png" % datetime.utcfromtimestamp(NOW).strftime("%m%d_%H%M")
plt.savefig(out_png, dpi=115, facecolor="white")
print("\n报告图已存:", out_png)
