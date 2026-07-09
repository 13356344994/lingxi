#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""books5 蒸发速度矿·首挖(2026-07-09;用户死线"重新出分,从长尾池塘咬肉,PF≥1.5";只读)。

对手模型:做市商的撤单比价格先动——最优档口流动性被单侧撤空(蒸发)= 知情者先跑,
价格随后追上。这是 1s 私有测量(不在任何公共推送里),长尾币做市商少、撤退签名更裸露。

冻结签名(运行前登记,p95 阈值来自分布侦察[未看收益],不扫描挑优):
  空头:liquidity_pull_bid ≥ 0.95(买侧档口被撤空)且 trade_imbalance_5s ≤ -0.3(卖流确认)
  多头:liquidity_pull_ask ≥ 0.95 且 trade_imbalance_5s ≥ +0.3
  可执行闸:spread_bps ≤ 12;长尾过滤:剔除主流币;同币 600s 去重。
  出场:能量刻度原样移植(unit=clip(|r60|,40,200),灾难1×/trail 0.8×激活 0.4×回吐/cap600),
  成本 12bps;路径=hot_micro 自身 1s 序列(比 3s 标尺更准)。
圣日纪律:挖掘集=07-09(唯一非圣日的 hot_micro 覆盖日);圣日 07-08(.1 文件)只有当
挖掘集 PF≥1.3 且分片同号时才碰,一次性终验,PF<1.3 就地处决(纪律 14h)。
"""
import json, sys, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
PULL = 0.95
FLOW = 0.30
FILE = sys.argv[1] if len(sys.argv) > 1 else "fast_hot_micro.jsonl"
TAG = "圣日(07-08)" if FILE.endswith(".1") else "挖掘集(07-09)"

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

# ---- pass1:逐行扫,同时攒每币 (t,mid) 稀疏环(仅保最近 70s 供 r60)+ 触发点 ----
entries = []
last = {}
pre = {}  # sym -> [(t,mid)] 最近70s
with open(f"{REC}/{FILE}") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        sym = r.get("symbol", "")
        mid = r.get("mid", 0)
        if not sym or mid <= 0:
            continue
        t = ts(r["recorded_at"])
        h = pre.setdefault(sym, [])
        h.append((t, mid))
        while h and t - h[0][0] > 70:
            h.pop(0)
        if sym in MAJORS:
            continue
        sp = r.get("spread_bps", 99)
        if sp > 12:
            continue
        pb, pa = r.get("liquidity_pull_bid", 0), r.get("liquidity_pull_ask", 0)
        ti = r.get("trade_imbalance_5s", 0)
        side = ""
        if pb >= PULL and ti <= -FLOW:
            side = "short"
        elif pa >= PULL and ti >= FLOW:
            side = "long"
        if not side:
            continue
        if sym in last and t - last[sym] < 600:
            continue
        last[sym] = t
        p60 = None
        for tt, mm in h:
            if t - tt >= 55:
                p60 = mm
        if p60 is None:
            continue
        r60 = (mid - p60) / p60 * 1e4
        entries.append(dict(sym=sym, t0=t, side=side, r60=r60, pull=max(pb, pa), flow=abs(ti),
                            unit=min(max(abs(r60), 40.0), 200.0)))
print(f"[{TAG}] 触发={len(entries)}(币数={len(set(e['sym'] for e in entries))})")
if not entries:
    sys.exit(0)

# ---- pass2:抽出场路径(1s 序列)----
idx = {}
for e in entries:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in entries}
with open(f"{REC}/{FILE}") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        s1 = line.find('"symbol":"')
        if p < 0 or s1 < 0:
            continue
        sym = line[s1 + 10:line.find('"', s1 + 10)]
        t0s = idx.get(sym)
        if not t0s:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        j = bisect.bisect_right(t0s, t) - 1
        if j < 0 or t > t0s[j] + 610:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("mid", 0) > 0:
            paths[(sym, t0s[j])].append((t, r["mid"]))

rows = []
for e in entries:
    pts = sorted(set(paths[(e["sym"], e["t0"])]))
    pts = [(t - e["t0"], m) for t, m in pts if t >= e["t0"]]
    if len(pts) < 5:
        continue
    ent = pts[0][1]
    sgn = 1 if e["side"] == "long" else -1
    unit = e["unit"]
    peak, px = 0.0, None
    for t, m in pts:
        u = sgn * (m - ent) / ent * 1e4
        if t >= 600 or u <= -unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px is None:
        continue
    rows.append(dict(e, net=sgn * (px - ent) / ent * 1e4 - COST))
print(f"[{TAG}] 可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    n = [r["net"] for r in sub]
    gp = sum(x for x in n if x > 0)
    gl = -sum(x for x in n if x < 0)
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    print(f"  {tag}: n={len(n):4d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} 合计{sum(n):+8.0f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

rep(rows, "全体            ")
rep([r for r in rows if r["side"] == "short"], "空头(bid蒸发)  ")
rep([r for r in rows if r["side"] == "long"], "多头(ask蒸发)  ")

print("\n== 死因诊断:早 vs 晚(做市商先跑的价值在'早';|r60|≥20=价格已动=动量税区)==")
rep([r for r in rows if abs(r["r60"]) < 20], "早(|r60|<20)   ")
rep([r for r in rows if abs(r["r60"]) >= 20], "晚(|r60|≥20)   ")
print("== 蒸发强度(p99=0.99;知情出逃应更极端)==")
rep([r for r in rows if r["pull"] >= 0.99], "极端蒸发(≥.99) ")
rep([r for r in rows if r["pull"] < 0.99], "普通蒸发(<.99) ")
print("== 先验组合:早×极端蒸发(冻结的精炼签名,唯一一个)==")
ref = [r for r in rows if abs(r["r60"]) < 20 and r["pull"] >= 0.99]
rep(ref, "早×极端         ")
rep([r for r in ref if r["side"] == "short"], "  ├空头         ")
rep([r for r in ref if r["side"] == "long"], "  └多头         ")

print("6h 分片(全体):")
t0min = rows[0]["t0"]
i = 0
while t0min + i * 21600 <= rows[-1]["t0"]:
    sl = [r["net"] for r in rows if t0min + i * 21600 <= r["t0"] < t0min + (i + 1) * 21600]
    if len(sl) >= 8:
        print(f"  片{i}: n={len(sl):3d} 中位{statistics.median(sl):+7.1f} {'✓' if statistics.median(sl)>0 else '✗'}")
    i += 1
