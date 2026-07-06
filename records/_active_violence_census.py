#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""主动暴力币池塘首研(2026-07-06 20:45,转向预案②③的弹药;只读研究,不动枪代码)。
问题:不靠爆仓驱动的暴力移动(主动大单/新闻/做市商撤退)占暴动多大比例?它们是延续还是回吐?
=烟花反手刀(§-8D 预注册)与"85%死水时段开工率"的原料。
口径:census 暴动事件(|ret_60s|≥25bps 触发)since 07-05(与 market_mids 覆盖对齐);
  爆仓系=sym_liq_5m>0 或 xliq_n_5m>0;主动系=双零。
前向:market_mids(3s 全市场)取 t0+60s/+300s mid → 同向延续(追单毛收益)与反向回吐(反手毛收益)。
毛口径无滑点=形状研究,不是判决。
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


# 1) 收 census 暴动事件
events = []
with open(os.path.join(DIR, "fast_mover_census.jsonl")) as f:
    for line in f:
        if line[:40] < '{"recorded_at":"' + SINCE[:10]:
            pass
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r["recorded_at"][:19] < SINCE:
            continue
        ret = r.get("ret_60s_bps")
        if ret is None or abs(ret) < 25:
            continue
        events.append({
            "t0": tsu(r["recorded_at"]), "sym": r["symbol"], "mid": r["mid"],
            "ret": ret, "dir": 1 if ret > 0 else -1,
            "liq": (r.get("sym_liq_5m", 0) or 0) + (r.get("xliq_n_5m", 0) or 0),
            "spread": r.get("spread_bps", 99),
        })
print("census 暴动事件(since %s, |ret60|≥25)=%d" % (SINCE[:10], len(events)))

# 同币 5min 去重(同一场暴动只记首事件)
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
print("同币5min去重后=%d" % len(events))

# 2) 前向查询表:sym → [(target_dt, event, slot)]
pending = {}
for e in events:
    e["f60"] = None
    e["f300"] = None
    pending.setdefault(e["sym"], []).append((e["t0"] + timedelta(seconds=60), e, "f60"))
    pending.setdefault(e["sym"], []).append((e["t0"] + timedelta(seconds=300), e, "f300"))
for v in pending.values():
    v.sort(key=lambda x: x[0], reverse=True)  # 尾部=最早目标,pop 高效

# 3) 流式扫 market_mids 回填(文件按时间序)
n_line = 0
with open(os.path.join(DIR, "fast_market_mids.jsonl")) as f:
    for line in f:
        n_line += 1
        try:
            r = json.loads(line)
        except Exception:
            continue
        t = tsu(r["recorded_at"])
        mids = r["mids"]
        for sym in list(pending.keys()):
            q = pending[sym]
            while q and q[-1][0] <= t:
                tgt, e, slot = q.pop()
                m = mids.get(sym)
                if m:
                    e[slot] = m
            if not q:
                del pending[sym]
print("mids 扫描 %d 行,未回填查询剩 %d 币" % (n_line, len(pending)))

# 4) 分桶出数
ok = [e for e in events if e["f60"] and e["f300"]]
act = [e for e in ok if e["liq"] == 0]
liqd = [e for e in ok if e["liq"] > 0]


def stats(tag, sub):
    if len(sub) < 10:
        print("%s n=%d(不足)" % (tag, len(sub)))
        return
    cont60 = [e["dir"] * (e["f60"] - e["mid"]) / e["mid"] * 1e4 for e in sub]
    cont300 = [e["dir"] * (e["f300"] - e["mid"]) / e["mid"] * 1e4 for e in sub]
    tight = [e for e in sub if e["spread"] <= 12]
    print("%-28s n=%-5d 追单前向: +60s中位%+7.1f / +300s中位%+7.1f  反手=取负  点差≤12占%3.0f%%" % (
        tag, len(sub), statistics.median(cont60), statistics.median(cont300),
        100.0 * len(tight) / len(sub)))
    # 尾部形状
    up = len([v for v in cont300 if v > 10])
    dn = len([v for v in cont300 if v < -10])
    print("%-28s   +300s: 续涨>10bps %3.0f%% | 回吐<-10bps %3.0f%% | 均值%+6.1f" % (
        "", 100.0 * up / len(sub), 100.0 * dn / len(sub), sum(cont300) / len(cont300)))


print("\n可评事件=%d  主动系(无爆仓)=%d(%.0f%%)  爆仓系=%d" % (
    len(ok), len(act), 100.0 * len(act) / max(1, len(ok)), len(liqd)))
print()
stats("主动暴力(liq=0)", act)
stats("爆仓驱动(liq>0)", liqd)
print()
# 方向分桶(烟花反手=只打上冲)
stats("主动·上冲(烟花)", [e for e in act if e["dir"] > 0])
stats("主动·下砸", [e for e in act if e["dir"] < 0])
# 强度分桶
stats("主动·强(|ret|≥40)", [e for e in act if abs(e["ret"]) >= 40])
stats("主动·上冲强(≥40,反手刀靶)", [e for e in act if e["dir"] > 0 and abs(e["ret"]) >= 40])
