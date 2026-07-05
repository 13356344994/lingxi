#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""分拣器影子全流程回放(用户 07-06 01:23 指令;章程=HANDOFF ⑮分拣器章程v1)。

流程:确认爆仓事件 → 场景仪表(燃料水位×潮汐×海况刹车)→ 事件卡片匹配通道 → 通道包处理
  通道① 风暴×跌潮·顺火包: 火灾∧(大额≥2kU∨占市值比≥0.0017)∧磁带活跃∧方向对号 → trail+二波止错20s ×1.0
  通道② 死水/常态·孤瀑包: 火灾∧磁带活跃∧爆仓减速∧方向对号 → trail+二波止错20s ×0.5
  通道③ 风暴×涨潮·轧空包: 烟花∧簇爆∧爆仓加速 → trail ×0.5(变体A;反手变体B另测)
  通道⓪ 其余一切+海况差(roll30≤-10 刹车凌驾) → 不玩,只记录
⚠️ 诚实标注:v1 表由这两天同一批数据的发现装配=in-sample 演示,收益数字是"机器形态验证+上界参考",
   不是判决;判决走冠军梯子。输出 /tmp/sorter_run.json。
"""
import json
import statistics
import bisect
import collections
import os
from datetime import datetime, timedelta

HERE = os.path.dirname(os.path.abspath(__file__))
_loader = open(os.path.join(HERE, "_particle_pipeline.py")).read().split("if __name__ ==")[0]
exec(_loader)  # rows / PARTICLES / metrics / STATES / COST / SCRATCH / ep / REC

# ---- 爆仓时间线(潮汐30min + 二波) ----
liq_t, liq_long = [], []
liq_by_sym = {}
for line in open(REC + "/fast_liquidation_events.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    t = ep(r["recorded_at"])
    liq_t.append(t)
    liq_long.append(1 if r.get("pos_side") == "long" else 0)
    liq_by_sym.setdefault(r["symbol"], []).append(t)
for v in liq_by_sym.values():
    v.sort()
prefL = [0]
for s in liq_long:
    prefL.append(prefL[-1] + s)


def tide30(t):
    lo = bisect.bisect_left(liq_t, t - 1800)
    hi = bisect.bisect_right(liq_t, t)
    n = hi - lo
    if n < 20:
        return "混沌"
    share = (prefL[hi] - prefL[lo]) / n
    return "跌潮" if share >= 0.65 else ("涨潮" if share <= 0.35 else "混沌")


def px_at(sym, t, tol=8):
    arr = paths.get(sym)
    keys = pk.get(sym)
    if not arr:
        return None
    i = bisect.bisect_left(keys, t)
    if i < len(arr) and arr[i][0] - t <= tol:
        return arr[i][1]
    return None


def ret_with_wavecut(e, cutX=20, cap=120):
    """通道①②持有包·联合模拟(v2 修正):同一条路径上 trail/灾难/二波止错谁先触发谁生效。
    v1 曾把二波止错独立近似(忽略 cutX 前 trail 已离场的情形)= 模拟器 bug,已修。"""
    if not e["entry"]:
        return None
    arr = paths.get(e["sym"])
    keys = pk.get(e["sym"])
    if not arr:
        return None
    ts = liq_by_sym.get(e["sym"], [])
    i2 = bisect.bisect_right(ts, e["t0"] + 3)
    wave2_at = ts[i2] if i2 < len(ts) else None  # 第二波到达时刻(None=没来)
    i = bisect.bisect_left(keys, e["entry_t"])
    peak, last = 0.0, None
    for j in range(i, len(arr)):
        T, m = arr[j]
        if T - e["t0"] > cap:
            break
        ret = e["dir"] * (m - e["entry"]) / e["entry"] * 1e4
        # 二波止错:过了 cutX 且二波未在 cutX 内到达 → 当刻离场(路径点粒度)
        if T - e["t0"] >= cutX and (wave2_at is None or wave2_at > e["t0"] + cutX):
            return ret
        last = ret
        peak = max(peak, ret)
        if ret <= -40:
            return ret
        if peak >= 10 and peak - ret >= 20:
            return ret
    return last


F = PARTICLES


def assign(e):
    """返回 (通道名, 权重, 收益函数) 或 通道⓪原因。"""
    if e.get("sea", 0) <= -10:
        return None, "海况差(刹车)"
    fuel_storm = e["fuel"] >= 40
    td = tide30(e["t0"])
    fire = e["dir"] < 0
    sm = bool(e.get("sm"))
    active = e["base"] >= 50
    big = e["ntl"] >= 2000 or (e["oi"] and e["ntl"] / e["oi"] >= 0.0017)
    if fuel_storm and td == "跌潮" and fire and sm and active and big:
        return ("① 风暴跌潮·顺火包", 1.0, ret_with_wavecut), None
    if (not fuel_storm) and fire and sm and active and F["爆仓减速"](e):
        return ("② 死水·孤瀑包", 0.5, ret_with_wavecut), None
    if fuel_storm and td == "涨潮" and (not fire) and F["簇爆"](e) and F["爆仓加速"](e):
        return ("③ 风暴涨潮·轧空包", 0.5, lambda x: x["v"]), None
    if td == "混沌" and not fuel_storm:
        return None, "混沌剧本"
    return None, "无通道匹配"


import sys
SINCE = ep(sys.argv[1]) if len(sys.argv) > 1 else 0
lanes = collections.defaultdict(list)
zero = collections.Counter()
trades = []
for e in sorted(rows, key=lambda x: x["t0"]):
    if e["t0"] < SINCE:
        continue
    lane, why = assign(e)
    if lane is None:
        zero[why] += 1
        continue
    name, w, fn = lane
    v = fn(e)
    if v is None:
        zero["路径缺失"] += 1
        continue
    net = (v - COST) * w
    lanes[name].append((e, v, net))
    bj = (datetime.utcfromtimestamp(e["t0"]) + timedelta(hours=8)).strftime("%m-%d %H:%M:%S")
    trades.append({"t": bj, "lane": name[:1], "sym": e["sym"].replace("-USDT", ""),
                   "side": "short" if e["dir"] < 0 else "long", "w": w,
                   "gross": round(v, 1), "net": round(net, 1)})

print("事件总数=%d | 开枪=%d | 不玩=%d" % (len(rows), len(trades), sum(zero.values())))
print("不玩原因:", dict(zero))
summary = {}
cum = 0.0
for tr in trades:
    cum += tr["net"]
    tr["cum"] = round(cum, 1)
for name, arr in sorted(lanes.items()):
    nets = [x[2] for x in arr]
    gross = [x[1] for x in arr]
    wins = [g for g in gross if g > SCRATCH]
    losses = [g for g in gross if g < -SCRATCH]
    wr = 100.0 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0
    pw = sum(x for x in nets if x > 0)
    pl = -sum(x for x in nets if x < 0)
    summary[name] = {"n": len(arr), "net_sum": round(sum(nets), 1),
                     "net_med": round(statistics.median(nets), 1), "win": round(wr),
                     "pf": round(pw / pl, 2) if pl > 0 else 99}
    print("%-18s n=%-3d 净合计=%+8.1f 净中位=%+6.1f 胜(中性)=%3.0f%% 净PF=%s" % (
        name, len(arr), sum(nets), statistics.median(nets), wr, summary[name]["pf"]))
tot = [t["net"] for t in trades]
if tot:
    print("\n分拣器组合总计: n=%d 净合计=%+.1f 净/笔=%+.1f" % (len(tot), sum(tot), sum(tot) / len(tot)))
json.dump({"gen": (datetime.utcnow() + timedelta(hours=8)).strftime("%m-%d %H:%M"),
           "total_events": len(rows), "zero": dict(zero), "lanes": summary, "trades": trades},
          open("/tmp/sorter_run.json", "w"), ensure_ascii=False)
print("→ /tmp/sorter_run.json")
