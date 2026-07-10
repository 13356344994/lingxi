#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""规则洞 #2 量化:结算收租(2026-07-10;OKX 规则审计;只读)。

规则事实(API 实测):资费按【结算时刻持仓者】收付,cap=±1%,结算钟公开到毫秒。
洞的机制:极端资费币,结算前 5min 进场站在收钱侧,结算后 5min 出——收租是规则确定的,
风险=前后 10min 的价格漂移(市场对这件事的定价)。
被强迫者=另一侧付租的存量持仓(不平仓就必须付);确定性收钱者=结算时刻站对侧的人。

量化:OKX 资费史(~3个月×401币)× okx_5m K线(60天×前200币,重叠期):
  事件=|settFundingRate|≥阈值的结算;进场=T-5m 收盘价,出场=T+5m 收盘价;
  方向=收租侧(f<0 做多收租/f>0 做空收租);净=|f|×1e4 + 漂移 − 12bps 票。
  按 |f| 分桶 × train/holdout(最后14天)报;容量=事件频率×可上名义。
"""
import json, os, bisect, statistics
from datetime import datetime, timezone

H = "/root/lingxi/records/history"
COST = 12.0

def load_klines(d):
    out = {}
    for fn in sorted(os.listdir(d)):
        if not fn.endswith(".jsonl"):
            continue
        sym = fn[:-6].replace("-USDT-SWAP", "")
        rows = []
        with open(f"{d}/{fn}") as f:
            for line in f:
                try:
                    x = json.loads(line)["data"]
                    rows.append((int(x[0]) / 1000.0, float(x[4])))
                except Exception:
                    continue
        rows = sorted(set(rows))
        if rows:
            out[sym] = rows
    return out

coins = load_klines(f"{H}/okx_5m")
tsidx = {s: [x[0] for x in c] for s, c in coins.items()}
print(f"5m 币数={len(coins)}")

# OKX 资费史:文件名形如 BTC-USDT-SWAP.jsonl,行 data=[instId,fundingRate,realizedRate,fundingTime,...]或数组
events = []
fd = f"{H}/okx_funding"
for fn in os.listdir(fd):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    if sym not in coins:
        continue
    with open(f"{fd}/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
            except Exception:
                continue
            # 兼容 dict 或数组两种落盘格式
            if isinstance(x, dict):
                fr = float(x.get("fundingRate", 0) or 0)
                ft = int(x.get("fundingTime", 0) or 0) / 1000.0
            elif isinstance(x, list) and len(x) >= 4:
                try:
                    fr = float(x[1])
                    ft = int(x[3]) / 1000.0
                except Exception:
                    continue
            else:
                continue
            if ft > 0 and abs(fr) >= 0.0005:
                events.append(dict(sym=sym, ft=ft, f=fr))
print(f"|f|≥0.05% 结算事件(有5m价格的币)={len(events)}")

t_hi = max(max(x[0] for x in c) for c in coins.values())
split = t_hi - 14 * 86400
rows = []
for e in events:
    c = coins[e["sym"]]
    tsa = tsidx[e["sym"]]
    i_pre = bisect.bisect_right(tsa, e["ft"] - 300) - 1
    i_post = bisect.bisect_right(tsa, e["ft"] + 300) - 1
    if i_pre < 0 or i_post <= i_pre:
        continue
    if e["ft"] - tsa[i_pre] > 900 or tsa[i_post] - e["ft"] > 900:
        continue
    p0, p1 = c[i_pre][1], c[i_post][1]
    if p0 <= 0:
        continue
    sgn = 1 if e["f"] < 0 else -1  # 收租侧
    drift = sgn * (p1 / p0 - 1) * 1e4
    net = abs(e["f"]) * 1e4 + drift - COST
    rows.append(dict(e, net=net, drift=drift, part="ho" if e["ft"] >= split else "tr"))
print(f"可评={len(rows)}(tr={sum(1 for r in rows if r['part']=='tr')} ho={sum(1 for r in rows if r['part']=='ho')})")

def rep(sub, tag):
    if len(sub) < 10:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    n = [r["net"] for r in sub]
    d = [r["drift"] for r in sub]
    gp = sum(x for x in n if x > 0)
    gl = -sum(x for x in n if x < 0)
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    print(f"  {tag}: n={len(n):4d} 净中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% "
          f"[漂移中位{statistics.median(d):+.1f}] 顶{tsym}({tn/len(sub)*100:.0f}%)")

for lo, hi, tag in ((0.0005, 0.001, "0.05-0.1%"), (0.001, 0.003, "0.1-0.3%"), (0.003, 0.0075, "0.3-0.75%"), (0.0075, 1, "≥0.75%(贴cap)")):
    for pt in ("tr", "ho"):
        rep([r for r in rows if lo <= abs(r["f"]) < hi and r["part"] == pt], f"|f|∈{tag:12s} [{pt}]")

# 供给与容量
from collections import Counter
days = (max(r["ft"] for r in rows) - min(r["ft"] for r in rows)) / 86400
big = [r for r in rows if abs(r["f"]) >= 0.003]
print(f"\n供给:|f|≥0.3% 事件 ≈{len(big)/max(days,1):.1f} 次/日(200币窗);"
      f"币分布 top:{Counter(r['sym'] for r in big).most_common(5)}")
