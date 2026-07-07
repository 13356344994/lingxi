#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「司天监」(Skywatch) 两日诚实回放 + 战绩图(2026-07-07;record-only,零真钱)。

架构 = 高维分拣器(章程㉕)派班 + 两引擎 + ⓪歇业:
  慢状态: fuel_5m(官方爆仓/5min) × tide_30m(爆仓净方向) × breadth30(全市场mid 30min收益中位)
  值班表(冻结): fuel>=40 & ebb → fire_cascade | fuel<40 & breadth>-15 → xgap_rev | 其余 → none
  引擎E  (xgap_rev v2): 便宜>=40 + 新鲜度(回看1800s须现过>-20,点数<10 fail-closed) →买OKX;
                        收敛<=10 / 600s / -60灾难;同币冷却1800s;成本12。
  引擎F  (fire_cascade 程序S): 卖方burst + 官方确认(3s内多头爆多数票) → t0+1s 做空;
                        trail(激活10/回吐20) / 灾难-40 / 二波止错(t0+20s无同币新爆→离场) / cap120s;
                        同币冷却120s;成本12。
诚实声明(不许删):参数全部为冻结先验,本回放不调任何值;但新鲜度规则(07-07晨的失败教的)、
  trail/二波参数(07-05/06研究)的发现窗与本窗重叠 → 07-07段=规则出生地只看形状,
  07-06段=规则诞生前数据(半干净反向检验),真正成绩=各封样竖线右侧的前向。
输出: records/skywatch_replay_<ts>.png + stdout 统计。
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

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
NOW = time.time()
T0 = NOW - 48 * 3600
T0 = max(T0, calendar.timegm(time.strptime("2026-07-05T07:10:00", "%Y-%m-%dT%H:%M:%S")))
SEAL_V1 = calendar.timegm(time.strptime("2026-07-07T00:50:00", "%Y-%m-%dT%H:%M:%S"))
SEAL_V2 = calendar.timegm(time.strptime("2026-07-07T04:15:00", "%Y-%m-%dT%H:%M:%S"))


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ========== ① 官方爆仓流(燃料/潮汐/确认源) ==========
liq_ts, liq_long, liq_short = [], [], []
liq_by_sym = collections.defaultdict(list)  # sym -> [(t, pos_side)]
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


# ========== ② 全市场广度(5min 网格采样) ==========
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
    t_old, m_old = old
    rets = [(mids[s] - m_old[s]) / m_old[s] * 1e4 for s in mids if s in m_old and m_old[s] > 0]
    if len(rets) >= 100:
        breadth[b] = statistics.median(rets)


def duty_at(t):
    fuel = cnt(liq_ts, t - 300, t)
    ebb = cnt(liq_long, t - 1800, t)
    flood = cnt(liq_short, t - 1800, t)
    if fuel >= 40:
        if ebb >= 2 * max(flood, 1) and ebb >= 5:
            return "fire_cascade", fuel
        return "none", fuel
    br = breadth.get(int(t // 300)) or breadth.get(int(t // 300) - 1)
    if br is None:
        return "none", fuel  # fail-closed
    return ("xgap_rev" if br > -15 else "none"), fuel


# ========== ③ 热币路径 + Bybit 锚(引擎E/F 的价格数据) ==========
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


# ========== 引擎E 回放(gated / 及 E-v1 ungated 对照) ==========
def run_E(gated, fresh_on):
    trades, last = [], {}
    skip_duty = 0
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
                if fresh_on and not gap_fresh(sym, T):
                    last[sym] = T
                    j += 1
                    continue
                if gated:
                    d, _ = duty_at(T)
                    if d != "xgap_rev":
                        skip_duty += 1
                        last[sym] = T
                        j += 1
                        continue
                entry, t_in = okx, T
                out, t_out = None, None
                for k in range(j + 1, n):
                    T2, okx2 = arr[k]
                    if T2 - t_in > 600:
                        break
                    by2 = a_at(sym, T2)
                    ret = (okx2 - entry) / entry * 1e4
                    out, t_out = ret, T2
                    if ret <= -60:
                        break
                    if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
                        break
                if out is not None:
                    trades.append((t_in, sym, out - COST))
                    last[sym] = t_in
            j += 1
    trades.sort()
    return trades, skip_duty


# ========== 引擎F 回放(fire_cascade 程序S) ==========
def run_F(gated):
    trades, last = [], {}
    skip_duty = 0
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
                continue  # 无官方确认 / 方向不对号
            if gated:
                dt, _ = duty_at(t0)
                if dt != "fire_cascade":
                    skip_duty += 1
                    last[sym] = t0
                    continue
            arr = paths.get(sym)
            keys = pk.get(sym)
            if not arr:
                continue
            i = bisect.bisect_left(keys, t0 + 1.0)
            if i >= len(arr) or arr[i][0] - t0 > 5:
                continue  # 1s 内无路径点
            entry, t_in = arr[i][1], arr[i][0]
            wave2 = any(t0 + 3 < tt <= t0 + 20 for (tt, _) in liq_by_sym.get(sym, []))
            peak, out = 0.0, None
            for k in range(i + 1, len(arr)):
                T2, px = arr[k]
                if T2 - t_in > 120:
                    break
                ret = (entry - px) / entry * 1e4  # short
                out = ret
                if ret > peak:
                    peak = ret
                if ret <= -40:
                    break  # 灾难
                if peak >= 10 and ret <= peak - 20:
                    break  # trail
                if not wave2 and T2 - t0 >= 20:
                    break  # 二波止错
            if out is not None:
                trades.append((t_in, sym, out - COST))
                last[sym] = t0
    trades.sort()
    return trades, skip_duty


def stat(trades, label):
    if not trades:
        print("%-26s n=0" % label)
        return
    v = [x[2] for x in trades]
    pw = sum(x for x in v if x > 0); pl = -sum(x for x in v if x < 0)
    wins = [x for x in v if x > 5]; losses = [x for x in v if x < -5]
    wr = 100 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    print("%-26s n=%-4d net_med=%+7.1f net_sum=%+8.0f PF=%5.2f win=%3.0f%%" % (
        label, len(v), statistics.median(v), sum(v), pw / pl if pl > 0 else 99, wr))


print("窗口 %s → %s (UTC)" % (datetime.utcfromtimestamp(T0).strftime("%m-%d %H:%M"),
                              datetime.utcfromtimestamp(NOW).strftime("%m-%d %H:%M")))
e_gated, e_skip = run_E(True, True)
f_gated, f_skip = run_F(True)
e_v1, _ = run_E(False, False)   # 旧E对照:无新鲜度无派班
f_all, _ = run_F(False)         # 火刀无派班对照
combo = sorted(e_gated + f_gated)
print()
stat(e_v1, "old E-v1 (no gate)")
stat(f_all, "fire (no gate)")
stat(e_gated, "SKYWATCH E (fresh+duty)")
stat(f_gated, "SKYWATCH fire (duty)")
stat(combo, "SKYWATCH combined")
print("duty 拦截: E=%d fire=%d" % (e_skip, f_skip))
for lab, cut in (("07-06半干净段(规则诞生前)", SEAL_V1), ("07-07规则出生地", None)):
    pass
print()
print("--- 分段(封样线切) ---")
stat([x for x in combo if x[0] < SEAL_V1], "combined <00:50Z(发现窗)")
stat([x for x in combo if x[0] >= SEAL_V1], "combined >=00:50Z")
stat([x for x in combo if x[0] >= SEAL_V2], "combined >=04:15Z(最干净)")

# ========== 图 ==========
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(14, 8), sharex=True,
                               gridspec_kw={"height_ratios": [3, 1]})
def curve(ax, trades, label, color, lw=1.5, ls="-"):
    if not trades:
        return
    xs = [datetime.utcfromtimestamp(x[0]) for x in trades]
    ys = []
    s = 0.0
    for x in trades:
        s += x[2]
        ys.append(s)
    ax.plot(xs, ys, label="%s (n=%d, sum=%+.0f)" % (label, len(trades), s),
            color=color, lw=lw, ls=ls)

curve(ax1, e_v1, "old E-v1 ungated", "#999999", 1.2, "--")
curve(ax1, f_all, "fire ungated", "#dd9999", 1.0, "--")
curve(ax1, e_gated, "SKYWATCH E (fresh+duty)", "#2a9d2a", 1.8)
curve(ax1, f_gated, "SKYWATCH fire (duty)", "#d62728", 1.8)
curve(ax1, combo, "SKYWATCH combined", "#000000", 2.6)
for cut, lab in ((SEAL_V1, "E-v1 seal 00:50Z"), (SEAL_V2, "E-v2 seal 04:15Z")):
    ax1.axvline(datetime.utcfromtimestamp(cut), color="#3355cc", ls=":", lw=1.5)
    ax1.text(datetime.utcfromtimestamp(cut), ax1.get_ylim()[0], " " + lab,
             rotation=90, va="bottom", fontsize=8, color="#3355cc")
ax1.axhline(0, color="black", lw=0.5)
ax1.set_ylabel("cumulative net bps (cost 12)")
ax1.set_title("SKYWATCH replay %s -> %s UTC | frozen priors, NO tuning in window | "
              "right of seal lines = clean; earlier = rule-discovery window (shape only)" % (
                  datetime.utcfromtimestamp(T0).strftime("%m-%d %H:%M"),
                  datetime.utcfromtimestamp(NOW).strftime("%m-%d %H:%M")), fontsize=10)
ax1.legend(loc="upper left", fontsize=8)
ax1.grid(alpha=0.3)

# duty 时间带
ts = T0
band_x, band_y = [], []
while ts <= NOW:
    d, fuel = duty_at(ts)
    band_x.append(datetime.utcfromtimestamp(ts))
    band_y.append({"none": 0, "xgap_rev": 1, "fire_cascade": 2}[d])
    ts += 300
ax2.fill_between(band_x, band_y, step="mid", alpha=0.6, color="#888888")
ax2.set_yticks([0, 1, 2])
ax2.set_yticklabels(["none (idle)", "xgap duty", "fire duty"])
ax2.set_ylabel("duty")
ax2.grid(alpha=0.3)
ax2.xaxis.set_major_formatter(mdates.DateFormatter("%m-%d %H:%M"))
fig.autofmt_xdate()
out_png = "/root/lingxi/records/skywatch_replay_%s.png" % datetime.utcfromtimestamp(NOW).strftime("%m%d_%H%M")
plt.tight_layout()
plt.savefig(out_png, dpi=110)
print("\n图已存:", out_png)
