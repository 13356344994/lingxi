#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""烟花刀 K 折分片研究(2026-07-09 用户"不要再等"授权的三枪会战之一)。
问题:烟花(暴力拉升力竭反手做空,固定300s出场)整体 PF~0.7 不过门;惯犯否决(躲毒)
已过 holdout 但非惯犯中位仍 -0.3——缺一个"正向粒子"把中位拧过 0。
设计:census 暴动流(07-05~09,4天)重建烟花细胞事件 → market_mids 回放 300s 前向(short
口径,trail 无效已证,固定 300s+(-40)灾难),净=毛-12;特征全部入场时刻可知;
K 折=按时间 8 等片,特征在 7 片挖、1 片验,≥6/8 片同号才过检。
候选特征(各带对手模型):
  F1 拉升幅度档(ret60:60-100/100-200/200+)——越大越可能是真轧空(危险)还是越竭尽?
  F2 力竭深度(ret15/ret60:<0.05 完全停 vs 0.05-0.25 仍在动)——完全停=买力真枯竭
  F3 本币爆仓伴随(sym_liq_5m>0)——有空头爆=轧空驱动(危险,回补完才跌)vs 无爆=纯 FOMO(接得)
  F4 燃料州(market_liq_5m)
  F5 盘口不平衡(top_ask_qty/top_bid_qty)——卖墙厚=上方有人出货,反手安全
  F6 惯犯(24h 同币烟花细胞次数,走前)——已过 holdout 的躲毒刀,作为基线叠加
  F7 跨所价差 xgap(拉升后 OKX 比锚贵=本地过冲,回落更确定)
"""
import json, calendar, time as _t, statistics, collections

BASE = "/root/lingxi/records/okx_directional/"
COST = 12.0
def tsec(s):
    return calendar.timegm(_t.strptime(s[:19], "%Y-%m-%dT%H:%M:%S"))

# ---- 1. 烟花细胞事件(census 重建,与引擎口径同:ret60>=60, ret15<0.25*ret60, spread<=6)----
events = []
seen = {}  # sym -> last event ts(600s 去重,比引擎宽,防重复采样)
fwhist = collections.defaultdict(list)
for line in open(BASE + "fast_mover_census.jsonl"):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    ret60 = r.get("ret_60s_bps")
    ret15 = r.get("ret_15s_bps")
    sp = r.get("spread_bps", 99)
    if ret60 is None or ret15 is None:
        continue
    if ret60 < 60 or ret15 >= 0.25 * ret60 or sp <= 0 or sp > 6:
        continue
    ts = tsec(r["recorded_at"])
    sym = r["symbol"]
    if sym in seen and ts - seen[sym] < 600:
        continue
    seen[sym] = ts
    prior = len([t for t in fwhist[sym] if ts - t <= 86400])
    fwhist[sym].append(ts)
    events.append({
        "sym": sym, "ts": ts, "mid": r["mid"], "ret60": ret60, "ret15": ret15,
        "spread": sp, "sym_liq": r.get("sym_liq_5m", 0), "fuel": r.get("market_liq_5m", 0),
        "imb": (r.get("top_ask_qty", 0) + 1e-9) / (r.get("top_bid_qty", 0) + 1e-9),
        "xgap": r.get("xgap_bps"), "prior": prior,
    })
print("烟花细胞事件 n=%d  跨度 %s → %s" % (len(events),
      _t.strftime("%m-%d %H:%M", _t.gmtime(events[0]["ts"])),
      _t.strftime("%m-%d %H:%M", _t.gmtime(events[-1]["ts"]))))

# ---- 2. market_mids 两遍法回放 300s(short:入场后价格跌=赚)----
pending = collections.defaultdict(list)  # sym -> [event 引用]
for ev in events:
    pending[ev["sym"]].append(ev)
for line in open(BASE + "fast_market_mids.jsonl"):
    try:
        r = json.loads(line)
    except ValueError:
        continue
    ts = tsec(r["recorded_at"])
    mids = r.get("mids", {})
    for sym, evs in pending.items():
        px = mids.get(sym)
        if px is None:
            continue
        for ev in evs:
            dt = ts - ev["ts"]
            if dt < 0 or dt > 310:
                continue
            if "entry" not in ev and dt >= 0:
                ev["entry"] = px  # 入场=事件后首个快照(含 ~3s 延迟,保守)
                ev["min"] = px; ev["last"] = px; ev["disaster"] = False
            if "entry" in ev:
                ret = (ev["entry"] - px) / ev["entry"] * 1e4  # short 口径
                if ret <= -40 and not ev["disaster"]:
                    ev["disaster"] = True
                    ev["dis_ret"] = ret
                if not ev["disaster"]:
                    ev["last"] = px
# 结算:净 = (固定300s 或灾难)- 成本
sample = []
for ev in events:
    if "entry" not in ev:
        continue
    if ev.get("disaster"):
        gross = ev["dis_ret"]
    else:
        gross = (ev["entry"] - ev["last"]) / ev["entry"] * 1e4
    ev["net"] = gross - COST
    sample.append(ev)
print("可回放样本 n=%d" % len(sample))

def agg(xs):
    if not xs:
        return (0, 0.0, 0.0)
    nets = [e["net"] for e in xs]
    gp = sum(x for x in nets if x > 0); gl = -sum(x for x in nets if x < 0)
    return (len(xs), statistics.median(nets), gp / gl if gl > 0 else 99.0)

n, med, pf = agg(sample)
print("基线(全体): n=%d 净中位=%+.1f PF=%.2f" % (n, med, pf))

# ---- 3. K 折框架 ----
sample.sort(key=lambda e: e["ts"])
K = 8
folds = [sample[i * len(sample) // K:(i + 1) * len(sample) // K] for i in range(K)]

def kfold(name, pred):
    """pred(ev)->bool 好组判定;返回过检与汇总。"""
    signs = []
    for i in range(K):
        hold = folds[i]
        a = [e["net"] for e in hold if pred(e)]
        b = [e["net"] for e in hold if not pred(e)]
        if len(a) < 3 or len(b) < 3:
            signs.append(None)
            continue
        signs.append(1 if statistics.median(a) > statistics.median(b) else -1)
    ok = sum(1 for s in signs if s == 1)
    valid = sum(1 for s in signs if s is not None)
    ga, gb = [e for e in sample if pred(e)], [e for e in sample if not pred(e)]
    na, ma, pa = agg(ga); nb, mb, pb = agg(gb)
    # 单币主导警告
    top = collections.Counter(e["sym"] for e in ga).most_common(1)
    warn = " ⚠单币%s占%d%%" % (top[0][0], 100 * top[0][1] / max(na, 1)) if na and top[0][1] / na > 0.3 else ""
    verdict = "过检" if valid >= 6 and ok >= 6 else ("弱(%d/%d)" % (ok, valid))
    print("%-30s %s | 好组 n=%d 中位%+7.1f PF%5.2f | 其余 n=%d 中位%+7.1f PF%5.2f%s"
          % (name, verdict, na, ma, pa, nb, mb, pb, warn))
    return valid >= 6 and ok >= 6

print("\n---- 单特征 K 折(8片,≥6 同号=过)----")
res = {}
res["F1a 拉升60-100"] = kfold("F1a 拉升适中(60-100)", lambda e: e["ret60"] < 100)
res["F1b 拉升≥200"] = kfold("F1b 拉升极端(≥200)", lambda e: e["ret60"] >= 200)
res["F2 完全力竭(ret15/60<0.05)"] = kfold("F2 完全力竭(<0.05)", lambda e: e["ret15"] < 0.05 * e["ret60"])
res["F3 无本币爆仓(纯FOMO)"] = kfold("F3 无本币爆仓(纯FOMO)", lambda e: e["sym_liq"] == 0)
res["F4 死水州(fuel<20)"] = kfold("F4 死水州(fuel<20)", lambda e: e["fuel"] < 20)
res["F5 卖墙厚(ask/bid>2)"] = kfold("F5 卖墙厚(ask/bid>2)", lambda e: e["imb"] > 2)
res["F6 非惯犯(<3)"] = kfold("F6 非惯犯(prior<3)", lambda e: e["prior"] < 3)
res["F7 本地过冲(xgap>+10)"] = kfold("F7 本地过冲(xgap>+10)", lambda e: (e["xgap"] or 0) > 10)

print("\n---- 组合(非惯犯为底,叠过检特征)----")
base = lambda e: e["prior"] < 3
for name, pred in [
    ("非惯犯×完全力竭", lambda e: base(e) and e["ret15"] < 0.05 * e["ret60"]),
    ("非惯犯×无本币爆仓", lambda e: base(e) and e["sym_liq"] == 0),
    ("非惯犯×卖墙厚", lambda e: base(e) and e["imb"] > 2),
    ("非惯犯×本地过冲", lambda e: base(e) and (e["xgap"] or 0) > 10),
    ("非惯犯×力竭×无爆", lambda e: base(e) and e["ret15"] < 0.05 * e["ret60"] and e["sym_liq"] == 0),
]:
    kfold(name, pred)
