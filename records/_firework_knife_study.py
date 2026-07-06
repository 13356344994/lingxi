#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""烟花反手刀精磨(2026-07-06 21:05;首研 +8.5 毛中位未过 12 地板,试出场优化+选择层;只读研究)。
靶:主动·上冲强(60s拉升≥40bps,5min内零爆仓,同币5min去重,since 07-05)。
反手做空,进场=census 事件 mid(毛口径,实盘会有点差+滑点,此处出形状)。
出场对照:A=固定+300s;B=trail 棘轮(峰值≥10 回吐 20 走人,-40 止错,cap 300s)——与 tape 研究同刀型。
选择层试切:点差≤6 / 拉升≥60 / 15s仍在加速 vs 已减速 / 死水时段(海况差=爆仓世界的死水恰是烟花主场?)。
"""
import json
import os
import statistics
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
SINCE = "2026-07-05T00:00:00"


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


events = []
with open(os.path.join(DIR, "fast_mover_census.jsonl")) as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r["recorded_at"][:19] < SINCE:
            continue
        ret = r.get("ret_60s_bps")
        if ret is None or ret < 40:  # 上冲强
            continue
        if (r.get("sym_liq_5m", 0) or 0) + (r.get("xliq_n_5m", 0) or 0) > 0:
            continue
        events.append({
            "t0": tsu(r["recorded_at"]), "sym": r["symbol"], "mid": r["mid"],
            "ret": ret, "ret15": r.get("ret_15s_bps"), "spread": r.get("spread_bps", 99),
        })
events.sort(key=lambda e: e["t0"])
last = {}
dedup = []
for e in events:
    lt = last.get(e["sym"])
    if lt and (e["t0"] - lt).total_seconds() < 300:
        continue
    last[e["sym"]] = e["t0"]
    dedup.append(e)
events = dedup
print("烟花靶(上冲≥40,无爆仓,去重)=%d" % len(events))

# 路径采集:sym → 活动窗口列表
win = {}
for e in events:
    e["path"] = []
    win.setdefault(e["sym"], []).append(e)

with open(os.path.join(DIR, "fast_market_mids.jsonl")) as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        t = tsu(r["recorded_at"])
        mids = r["mids"]
        for sym, evs in win.items():
            m = mids.get(sym)
            if not m:
                continue
            for e in evs:
                dt = (t - e["t0"]).total_seconds()
                if 0 < dt <= 300:
                    e["path"].append((dt, m))

ok = [e for e in events if len(e["path"]) >= 30]
print("路径可评(≥30点)=%d" % len(ok))


def trail_short(e):
    """反手做空:ret=(entry-m)/entry;棘轮峰≥10回吐20,-40止错,cap300s。返回(净前bps, 持有秒)。"""
    entry = e["mid"]
    peak, lastv, lastt = 0.0, None, 0.0
    for dt, m in e["path"]:
        v = (entry - m) / entry * 1e4
        lastv, lastt = v, dt
        peak = max(peak, v)
        if v <= -40:
            return v, dt
        if peak >= 10 and peak - v >= 20:
            return v, dt
    return lastv, lastt


def fixed300(e):
    return (e["mid"] - e["path"][-1][1]) / e["mid"] * 1e4


def report(tag, sub):
    if len(sub) < 30:
        print("%-36s n=%d(不足)" % (tag, len(sub)))
        return
    A = [fixed300(e) for e in sub]
    B = [trail_short(e)[0] for e in sub]
    holds = [trail_short(e)[1] for e in sub]
    winsB = len([v for v in B if v > 0])
    for name, V in (("固定300s", A), ("trail棘轮", B)):
        med = statistics.median(V)
        mean = sum(V) / len(V)
        print("%-36s %-8s n=%-5d 毛中位%+7.1f 均%+7.1f 过12地板%s 过18门%s" % (
            tag, name, len(V), med, mean,
            "✓" if med > 12 else "✗", "✓" if med > 18 else "✗"))
    print("%-36s trail顺向=%.0f%% 持有中位=%.0fs" % ("", 100.0 * winsB / len(B), statistics.median(holds)))


report("全体烟花", ok)
print()
report("点差≤6", [e for e in ok if e["spread"] <= 6])
report("拉升≥60", [e for e in ok if e["ret"] >= 60])
report("拉升≥60×点差≤6", [e for e in ok if e["ret"] >= 60 and e["spread"] <= 6])
print()
# 15s 减速判定:15s 涨幅 < 25% 的 60s 涨幅 = 力竭(fade_exhaustion 家族)
dec = [e for e in ok if e["ret15"] is not None and e["ret15"] < 0.25 * e["ret"]]
acc = [e for e in ok if e["ret15"] is not None and e["ret15"] >= 0.5 * e["ret"]]
report("15s已减速(力竭烟花)", dec)
report("15s仍加速(别接飞刀)", acc)
print()
report("力竭×点差≤6", [e for e in dec if e["spread"] <= 6])
report("力竭×拉升≥60", [e for e in dec if e["ret"] >= 60])
report("力竭×拉升≥60×点差≤6", [e for e in dec if e["ret"] >= 60 and e["spread"] <= 6])
