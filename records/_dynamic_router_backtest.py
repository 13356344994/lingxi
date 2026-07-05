#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""状态驱动动态路由 · 走前回测(用户 07-05 23:57"落实脚本+回测成果"指令)。

设计(诚实性优先):
  校准窗 A = SPLIT 之前的确认事件:每个燃料状态独立选出该状态的最优组合(用户双正门:
             止错中性化胜率≥50%、净PF≥1.3、格内 n≥8;主排序=净中位;全不过门=该状态不开枪);
  测试窗 B = SPLIT 之后:映射表冻结,逐事件按当时燃料状态查表决定是否开枪(=盘中"选"的模拟);
  对照   = ①动态路由 ②静态最优单组合(A 窗全局最优,B 窗全时段应用)③无选择全打。
  口径   = trail-cap120 毛收益 − 12bps;刮痕(|毛|≤5)从胜率中性化。
输出:终端报告 + /tmp/router_backtest.json(看板数据)。
⚠️ 单日数据、A 窗选择本质是 in-sample 挖掘——本回测验证的是"路由机器"的形态,
   不替代冠军梯子的多日判决。
"""
import json
import sys
import collections
import statistics
import os
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
_loader = open(os.path.join(HERE, "_particle_pipeline.py")).read()
_loader = _loader.split('if __name__ ==')[0]
exec(_loader)  # rows / PARTICLES / STATES / metrics / dominated / COST / SCRATCH

SPLIT = ep(sys.argv[1]) if len(sys.argv) > 1 else ep("2026-07-05T09:00:00")  # 17:00 BJ
A = [e for e in rows if e["t0"] < SPLIT]
B = [e for e in rows if e["t0"] >= SPLIT]
print("校准窗A=%d 事件 | 测试窗B=%d 事件 | 分割=17:00 BJ" % (len(A), len(B)))

names = list(PARTICLES)
import itertools


def combos_on(evts, nmin):
    out = []
    for depth in (1, 2, 3):
        for combo in itertools.combinations(names, depth):
            if "火灾(跟空)" in combo and "烟花(跟多)" in combo:
                continue
            fs = [PARTICLES[c] for c in combo]
            sel = [e for e in evts if all(f(e) for f in fs)]
            if len(sel) < nmin:
                continue
            dom, _ = dominated(sel)
            if dom:
                continue
            m = metrics(sel)
            m["tag"] = "×".join(combo)
            out.append(m)
    return out


def pick_best(cands):
    ok = [m for m in cands if m["win_neu"] >= 50 and m["pf_net"] >= 1.3 and m["net_med"] > 0]
    ok.sort(key=lambda m: (-m["net_med"], m["tag"].count("×")))
    return ok[0] if ok else None


# ---- A 窗:分状态选最优 + 全局静态最优 ----
state_map = {}
for sname, sf in STATES.items():
    cell = [e for e in A if sf(e)]
    best = pick_best(combos_on(cell, 8))
    state_map[sname] = best["tag"] if best else None
    print("状态[%s] A窗n=%d → 选用: %s %s" % (
        sname, len(cell), best["tag"] if best else "(无过门组合→该状态不开枪)",
        ("(A窗 净中位%+.1f 胜%.0f%% PF%.2f n=%d)" % (best["net_med"], best["win_neu"], best["pf_net"], best["n"])) if best else ""))
static_best = pick_best(combos_on(A, 15))
print("静态对照(A窗全局最优): %s (净中位%+.1f)" % (static_best["tag"], static_best["net_med"]))


def match(e, tag):
    if not tag:
        return False
    return all(PARTICLES[c](e) for c in tag.split("×"))


def state_of(e):
    for sname, sf in STATES.items():
        if sf(e):
            return sname
    return None


# ---- B 窗:三方对照 ----
strategies = {
    "动态路由(按状态查表)": lambda e: match(e, state_map.get(state_of(e))),
    "静态最优(%s)" % static_best["tag"]: lambda e: match(e, static_best["tag"]),
    "无选择全打": lambda e: True,
}
report = {}
for sname_, pick in strategies.items():
    sel = sorted([e for e in B if pick(e)], key=lambda e: e["t0"])
    m = metrics(sel)
    trades = []
    cum = 0.0
    for e in sel:
        cum += e["v"] - COST
        bj = (datetime.utcfromtimestamp(e["t0"]) + timedelta(hours=8)).strftime("%H:%M:%S")
        trades.append({"t": bj, "ts": e["t0"], "sym": e["sym"].replace("-USDT", ""),
                       "side": "short" if e["dir"] < 0 else "long", "state": state_of(e),
                       "gross": round(e["v"], 1), "net": round(e["v"] - COST, 1), "cum": round(cum, 1)})
    report[sname_] = {"stats": {k: round(v, 2) if isinstance(v, float) else v for k, v in m.items()},
                      "trades": trades}
    print("\n== B窗 %s ==" % sname_)
    print("  n=%d 净合计=%+.0f 净/笔=%+.1f 胜(中性化)=%.0f%% 净PF=%.2f" % (
        m["n"], m["net_sum"], m["net_sum"] / max(m["n"], 1), m["win_neu"], m["pf_net"]))

out = {
    "generated_bj": (datetime.utcnow() + timedelta(hours=8)).strftime("%m-%d %H:%M"),
    "split_bj": "17:00", "a_n": len(A), "b_n": len(B),
    "state_map": state_map, "static_tag": static_best["tag"],
    "strategies": report,
}
json.dump(out, open("/tmp/router_backtest.json", "w"), ensure_ascii=False)
print("\n→ /tmp/router_backtest.json")
