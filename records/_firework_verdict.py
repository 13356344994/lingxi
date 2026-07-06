#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""烟花反手刀前向判决(预注册 2026-07-06 21:20 BJ,判决集=13:00Z(21:00 BJ)后新事件;防多重检验)。
主细胞:主动(过去5min液=0)×上冲≥60×力竭(ret15<0.25×ret60)×点差≤6 → 反手做空,固定+300s 出场,-40 灾难止错。
鲁棒锚:拉升≥60 单刀。过线=成本12下净中位>0 且 n≥30。冻结口径,不许改切点。
"""
import json
import os
import statistics
import sys
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
SINCE = sys.argv[1] if len(sys.argv) > 1 else "2026-07-06T13:00:00"
COST = 12.0


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
        if ret is None or ret < 40:
            continue
        if (r.get("sym_liq_5m", 0) or 0) + (r.get("xliq_n_5m", 0) or 0) > 0:
            continue
        events.append({"t0": tsu(r["recorded_at"]), "sym": r["symbol"], "mid": r["mid"],
                       "ret": ret, "ret15": r.get("ret_15s_bps"), "spread": r.get("spread_bps", 99)})
events.sort(key=lambda e: e["t0"])
last, dedup = {}, []
for e in events:
    lt = last.get(e["sym"])
    if lt and (e["t0"] - lt).total_seconds() < 300:
        continue
    last[e["sym"]] = e["t0"]
    dedup.append(e)
events = dedup

win = {}
for e in events:
    e["path"] = []
    win.setdefault(e["sym"], []).append(e)
with open(os.path.join(DIR, "fast_market_mids.jsonl")) as f:
    for line in f:
        if line[:35] < '{"recorded_at":"' + SINCE[:10]:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        t = tsu(r["recorded_at"])
        for sym, evs in win.items():
            m = r["mids"].get(sym)
            if not m:
                continue
            for e in evs:
                dt = (t - e["t0"]).total_seconds()
                if 0 < dt <= 300:
                    e["path"].append((dt, m))

ok = [e for e in events if len(e["path"]) >= 30]


def short_fixed(e):
    entry = e["mid"]
    for dt, m in e["path"]:
        v = (entry - m) / entry * 1e4
        if v <= -40:
            return v
    return (entry - e["path"][-1][1]) / entry * 1e4


def verdict(tag, sub, need=30):
    if not sub:
        print("%-30s n=0" % tag)
        return
    net = [short_fixed(e) - COST for e in sub]
    med = statistics.median(net)
    tot = sum(net)
    wins = len([v for v in net if v > 0])
    status = "样本不足(%d/%d)" % (len(net), need) if len(net) < need else \
        ("✅ 过线" if med > 0 and tot > 0 else "❌ 未过")
    print("%-30s n=%-4d 净中位%+7.1f 净合计%+8.1f 胜率%3.0f%% → %s" % (
        tag, len(net), med, tot, 100.0 * wins / len(net), status))


print("烟花反手刀前向判决(判决集 since %s,成本%.0f,固定300s出场/-40止错)" % (SINCE, COST))
main = [e for e in ok if e["ret"] >= 60 and e["spread"] <= 6
        and e["ret15"] is not None and e["ret15"] < 0.25 * e["ret"]]
verdict("主细胞(力竭×≥60×点差≤6)", main)
verdict("鲁棒锚(拉升≥60 单刀)", [e for e in ok if e["ret"] >= 60])
verdict("全体烟花(参考,不判决)", ok, need=10 ** 9)
