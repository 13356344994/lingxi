#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tape 确认漏斗特征组合扫描(只读;07-05 用户"所有相关维度排列组合"指令)。

⚠️ 性质=假设生成器,不是裁决器:布尔化切点取自本样本(in-sample)+组合遍历=多重比较,
好看的组合一律只配"预注册→新样本复验"(OI三重奏 231 组合选优翻车的教训)。
护栏:①n 门槛(单15/双15/三12);②单币主导>50% 丢弃;③trail 收益同币 120s 内可能重叠,
top币占比列出供人审;④输出底部自动打印"待预注册"格式。

特征(对确认 burst,trail-cap120 口径收益):
  磁带活跃 = 基线1s成交额 ≥ P67 | 磁带死 = ≤P33
  大额 = burst名义≥2kU | 高倍数 = ≥10x(=单子/近期成交额比,用户指定维度)
  大体量币 = OI名义 ≥ 中位(市值代理) | 占市值比高 = 名义/OI名义 ≥ 中位(用户指定维度)
  扫穿率高 = 名义/10bp深度 ≥ 中位 | 窄点差 = ≤中位
  向下 = 卖方burst | 方向一致 = 确认被爆方向对号
  高燃料 = market_liq_5m≥40 | OI瓦解 = 60s OI 降 ≥0.1%
"""
import json
import bisect
import calendar
import collections
import itertools
import os
import statistics
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ---- burst + 确认 + 方向一致 ----
events = []
for line in open(REC + "/fast_burst_detect.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    events.append({"t0": ep(r["recorded_at"]), "sym": r["symbol"],
                   "dir": 1.0 if r.get("side") == "buy" else -1.0,
                   "ntl": float(r.get("notional_1s", 0) or 0),
                   "mult": float(r.get("multiple", 0) or 0),
                   "base": float(r.get("baseline_med_1s", 0) or 0),
                   "fuel": float(r.get("market_liq_5m", 0) or 0)})
events.sort(key=lambda e: e["t0"])
liqd = {}
for line in open(REC + "/fast_liquidation_events.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    liqd.setdefault(r["symbol"], []).append((ep(r["recorded_at"]), r.get("pos_side", "")))
for v in liqd.values():
    v.sort()
conf = []
for e in events:
    arr = liqd.get(e["sym"])
    if not arr:
        continue
    keys = [t for t, _ in arr]
    i = bisect.bisect_right(keys, e["t0"])
    sides = [s for t, s in arr[i:] if t - e["t0"] <= 3.0]
    if not sides:
        continue
    want = "long" if e["dir"] < 0 else "short"
    e["sm"] = sum(1 for s in sides if s == want) * 2 >= len(sides)
    conf.append(e)

# ---- hot_micro:进场价 + t0 点差/OI + t0-60 OI ----
want_sec = {}
for k, e in enumerate(conf):
    for s in range(1, 4):
        want_sec.setdefault(int(e["t0"]) + s, []).append((k, "en"))
    for s in range(-4, 1):
        want_sec.setdefault(int(e["t0"]) + s, []).append((k, "now"))
    for s in range(-64, -56):
        want_sec.setdefault(int(e["t0"]) + s, []).append((k, "old"))
want = {}
for sec, lst in want_sec.items():
    want[datetime.utcfromtimestamp(sec).strftime("%Y-%m-%dT%H:%M:%S")] = lst
for e in conf:
    e.update(entry=None, entry_t=None, spread=None, oi=None, oi60=None)
for fn in (REC + "/fast_hot_micro.jsonl.1", REC + "/fast_hot_micro.jsonl"):
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            p = line.find('"recorded_at":"')
            if p < 0:
                continue
            ks = want.get(line[p + 15:p + 34])
            if not ks:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            sym = d.get("symbol")
            T = ep(d["recorded_at"])
            for k, kind in ks:
                e = conf[k]
                if e["sym"] != sym:
                    continue
                dt = T - e["t0"]
                if kind == "en" and 1.0 <= dt <= 4.0 and d.get("mid", 0) > 0:
                    if e["entry"] is None or T < e["entry_t"]:
                        e["entry"], e["entry_t"] = d["mid"], T
                elif kind == "now" and -5 <= dt <= 0.5:
                    if e["spread"] is None:
                        e["spread"] = d.get("spread_bps")
                    if d.get("oi_now"):
                        e["oi"] = d["oi_now"] * (d.get("mid") or 0)
                elif kind == "old" and -65 <= dt <= -55:
                    if d.get("oi_now"):
                        e["oi60"] = d["oi_now"] * (d.get("mid") or 0)

# ---- shadow 深度 ----
depth = {}
for line in open(REC + "/fast_executable_shadow_events.jsonl"):
    if '"tape_burst"' not in line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    depth[(r["symbol"], int(ep(r["recorded_at"])))] = r.get("depth_notional_10bps", 0)

# ---- 路径 + trail ----
syms = {e["sym"] for e in conf}
paths = collections.defaultdict(list)
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            if d["backlog_sym"] in syms and not d.get("backlog_venue"):
                for ms, mid in d.get("backlog", []):
                    paths[d["backlog_sym"]].append((ms / 1000.0, mid))
            continue
        T = ep(d["recorded_at"])
        for sym, mid in d.get("mids", {}).items():
            if sym in syms:
                paths[sym].append((T, mid))
for s in paths:
    paths[s] = sorted(set(paths[s]))
pk = {s: [x[0] for x in arr] for s, arr in paths.items()}


def trail(e, mh=120):
    arr = paths.get(e["sym"])
    if not arr or not e["entry"]:
        return None
    keys = pk[e["sym"]]
    i = bisect.bisect_left(keys, e["entry_t"])
    peak, last = 0.0, None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - e["t0"] > mh:
            break
        ret = e["dir"] * (m - e["entry"]) / e["entry"] * 1e4
        last = ret
        peak = max(peak, ret)
        if ret <= -40:
            return ret
        if peak >= 10 and peak - ret >= 20:
            return ret
    return last


rows = []
for e in conf:
    v = trail(e)
    if v is None:
        continue
    d10 = None
    for dt in range(0, 6):
        d10 = depth.get((e["sym"], int(e["t0"]) + dt)) or d10
    e["v"], e["d10"] = v, d10
    rows.append(e)
print("确认=%d 可评=%d" % (len(conf), len(rows)))


def perc(vals, p):
    vals = sorted(vals)
    return vals[int(len(vals) * p)]


bases = [e["base"] for e in rows if e["base"] > 0]
ois = [e["oi"] for e in rows if e["oi"]]
ratios = [e["ntl"] / e["oi"] for e in rows if e["oi"]]
sweeps = [e["ntl"] / e["d10"] for e in rows if e["d10"]]
sprs = [e["spread"] for e in rows if e["spread"] is not None]
b_hi, b_lo = perc(bases, 0.67), perc(bases, 0.33)
oi_med = statistics.median(ois) if ois else 0
ratio_med = statistics.median(ratios) if ratios else 0
sweep_med = statistics.median(sweeps) if sweeps else 0
spr_med = statistics.median(sprs) if sprs else 0

FEATS = collections.OrderedDict([
    ("磁带活跃", lambda e: e["base"] >= b_hi),
    ("磁带死", lambda e: 0 < e["base"] <= b_lo),
    ("大额2kU", lambda e: e["ntl"] >= 2000),
    ("高倍数10x", lambda e: e["mult"] >= 10),
    ("大体量币", lambda e: e["oi"] and e["oi"] >= oi_med),
    ("占市值比高", lambda e: e["oi"] and e["ntl"] / e["oi"] >= ratio_med),
    ("扫穿率高", lambda e: e["d10"] and e["ntl"] / e["d10"] >= sweep_med),
    ("窄点差", lambda e: e["spread"] is not None and e["spread"] <= spr_med),
    ("向下", lambda e: e["dir"] < 0),
    ("方向一致", lambda e: e["sm"]),
    ("高燃料", lambda e: e["fuel"] >= 40),
    ("OI瓦解", lambda e: e["oi"] and e["oi60"] and (e["oi"] - e["oi60"]) / e["oi60"] <= -0.001),
])


def stat(sel, tag, nmin):
    vals = [e["v"] for e in sel]
    if len(vals) < nmin:
        return None
    syms_c = collections.Counter(e["sym"] for e in sel)
    top, topn = syms_c.most_common(1)[0]
    if topn / len(vals) > 0.5:
        return None  # 单币主导丢弃
    w = sum(v for v in vals if v > 0)
    l = -sum(v for v in vals if v < 0)
    return (statistics.median(vals), 100.0 * sum(1 for v in vals if v > 0) / len(vals),
            (w / l if l > 0 else 99.0), len(vals), sum(vals) / len(vals), tag,
            "%s:%d" % (top, topn))


print("\n== 单特征(trail 口径;中位/胜率/PF/n/均值/top币)==")
res = []
for name, fn in FEATS.items():
    r = stat([e for e in rows if fn(e)], name, 15)
    if r:
        res.append(r)
for med, win, pf, n, mean, tag, top in sorted(res, reverse=True):
    print("  %-14s 中位=%+7.1f 胜=%3.0f%% PF=%5.2f n=%-3d 均=%+7.1f %s" % (tag, med, win, pf, n, mean, top))

print("\n== 两特征组合 top12(n≥15,非单币主导)==")
res2 = []
for (a, fa), (b, fb) in itertools.combinations(FEATS.items(), 2):
    r = stat([e for e in rows if fa(e) and fb(e)], a + "×" + b, 15)
    if r:
        res2.append(r)
for med, win, pf, n, mean, tag, top in sorted(res2, reverse=True)[:12]:
    print("  %-24s 中位=%+7.1f 胜=%3.0f%% PF=%5.2f n=%-3d 均=%+7.1f %s" % (tag, med, win, pf, n, mean, top))
print("  --- 垫底3(避雷图) ---")
for med, win, pf, n, mean, tag, top in sorted(res2)[:3]:
    print("  %-24s 中位=%+7.1f 胜=%3.0f%% PF=%5.2f n=%-3d 均=%+7.1f %s" % (tag, med, win, pf, n, mean, top))

print("\n== 三特征组合 top8(n≥12)==")
res3 = []
for (a, fa), (b, fb), (c, fc) in itertools.combinations(FEATS.items(), 3):
    r = stat([e for e in rows if fa(e) and fb(e) and fc(e)], "×".join((a, b, c)), 12)
    if r:
        res3.append(r)
for med, win, pf, n, mean, tag, top in sorted(res3, reverse=True)[:8]:
    print("  %-32s 中位=%+7.1f 胜=%3.0f%% PF=%5.2f n=%-3d 均=%+7.1f %s" % (tag, med, win, pf, n, mean, top))

print("\n⚠️ 全部 in-sample 假设生成;切点:base_hi=%.0f oi_med=%.0f ratio_med=%.5f sweep_med=%.3f spr_med=%.1f" % (
    b_hi, oi_med, ratio_med, sweep_med, spr_med))
print("好看的组合 → 预注册(切点冻结)→ 新样本 n≥30 复验过门才算数。")
