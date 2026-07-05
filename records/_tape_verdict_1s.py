#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tape 确认漏斗秒级裁决(只读;n≥50 到门后的正式口径)。

背景:滚动 harness 用 hot_mids(3s 拍)只能量到 +2~3s 进场,落在 edge 衰减区,
读数 +0.5 与离线曲线(+1s=+13.6 / +3s=-13)一致但无裁决力。本脚本改用 hot_micro
(热币 1s 拍)重量两个进场点:
  A. +1.0s = 离线研究锚点(edge 是否存在);
  B. +1.4s = 整枪实测评估延迟中位(shadow event_age_ms 中位 1403ms;枪够不够得着)。
出场 = +60s 首个 mid。确认口径与滚动 harness 相同(burst 后 ≤3s 官方爆仓)。
"""
import json
import sys
import calendar
import statistics
import bisect
import os
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"


def ts_epoch(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    frac = 0.0
    if len(s) > 20 and s[19] == ".":
        try:
            frac = float("0." + s[20:].rstrip("Z"))
        except ValueError:
            pass
    return base + frac


# ---- 确认事件(与滚动 harness 同口径)----
events = []
for line in open(REC + "/fast_burst_detect.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    events.append({
        "t0": ts_epoch(r["recorded_at"]), "sym": r["symbol"],
        "dir": 1.0 if r.get("side") == "buy" else -1.0,
        "fuel": float(r.get("market_liq_5m", 0) or 0),
    })
events.sort(key=lambda e: e["t0"])

liq = {}
for line in open(REC + "/fast_liquidation_events.jsonl"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except ValueError:
        continue
    liq.setdefault(r["symbol"], []).append(ts_epoch(r["recorded_at"]))
for v in liq.values():
    v.sort()
confirmed = []
for e in events:
    ts_ = liq.get(e["sym"])
    if not ts_:
        continue
    i = bisect.bisect_right(ts_, e["t0"])
    if i < len(ts_) and ts_[i] - e["t0"] <= 3.0:
        confirmed.append(e)
print("burst=%d 确认(≤3s官方爆仓)=%d" % (len(events), len(confirmed)))

# ---- hot_micro 秒级 mid:只捞需要的 (sym, 秒) ----
want = {}  # "sym|iso19秒" 不够快;用 秒字符串 -> [(idx, kind)],kind: a=+1s b=+1.4s x=+60s
def secstr(ep):
    return datetime.utcfromtimestamp(ep).strftime("%Y-%m-%dT%H:%M:%S")


for k, e in enumerate(confirmed):
    for s in range(1, 4):       # 进场候选秒(+1~+3;取窗口内最早)
        want.setdefault(secstr(int(e["t0"]) + s), []).append(k)
    for s in range(60, 66):     # 出场候选秒
        want.setdefault(secstr(int(e["t0"]) + s), []).append(k)
for e in confirmed:
    e["pts"] = []               # [(t, mid)]

files = [REC + "/fast_hot_micro.jsonl.1", REC + "/fast_hot_micro.jsonl"]
for fn in files:
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            p = line.find('"recorded_at":"')
            if p < 0:
                continue
            tstr = line[p + 15: p + 34]
            ks = want.get(tstr)
            if not ks:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            sym = d.get("symbol")
            mid = d.get("mid", 0)
            if mid <= 0:
                continue
            T = ts_epoch(d["recorded_at"])
            for k in ks:
                if confirmed[k]["sym"] == sym:
                    confirmed[k]["pts"].append((T, mid))


def px_at(e, lo, hi):
    best = None
    for T, m in e["pts"]:
        if lo <= T - e["t0"] <= hi and (best is None or T < best[0]):
            best = (T, m)
    return best[1] if best else None


def stat(rows, tag):
    if not rows:
        print("  %-24s n=0" % tag)
        return
    med = statistics.median(rows)
    win = 100.0 * sum(1 for x in rows if x > 0) / len(rows)
    print("  %-24s n=%-3d 中位=%+7.1fbps 均值=%+7.1f 顺向=%3.0f%%" % (
        tag, len(rows), med, sum(rows) / len(rows), win))


for lo, hi, tag in ((1.0, 2.0, "A. 进场@+1s(离线锚)"), (1.4, 2.4, "B. 进场@+1.4s(整枪中位延迟)")):
    rets = []
    by_fuel = {"死水(<20)": [], "中间(20-40)": [], "活跃(>=40)": []}
    for e in confirmed:
        en = px_at(e, lo, hi)
        ex = px_at(e, 60.0, 66.0)
        if not en or not ex:
            continue
        ret = e["dir"] * (ex - en) / en * 1e4
        rets.append(ret)
        b = "死水(<20)" if e["fuel"] < 20 else ("中间(20-40)" if e["fuel"] < 40 else "活跃(>=40)")
        by_fuel[b].append(ret)
    print("\n== %s → +60s 毛收益 ==" % tag)
    stat(rets, "全部")
    for b in ("死水(<20)", "中间(20-40)", "活跃(>=40)"):
        stat(by_fuel[b], b)
print("\n裁决锚(预注册):A 口径中位站住 +13.6 量级(至少过飞轮门12)→ 提探针;A 过而 B 不过 → 病在延迟,先修枪再谈探针;A 不过 → tape 判死。")
