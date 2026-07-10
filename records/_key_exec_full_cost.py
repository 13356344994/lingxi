#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""key 线执行成本全账(2026-07-10 用户令④;只读)。

三件套齐报才许谈"实际门票":
  ①入场滑点(成交价 vs 评估时刻 mid;settlements.entry_slip_bps)
  ②未成交赢单机会成本(maker_not_filled 的触发,若按挂单价成交、按同款出场跑完,错过多少;
    数据=fast_maker_attempts.jsonl(reason 含 key_)× market_mids 30s 网格反事实)
  ③止损穿线滑点(fast_key_stop 的实收 bps vs 理论 -150-费=穿线额)
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

# ---- ①③ 从结算 ----
settles = []
with open(f"{REC}/fast_v2_settlements.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if str(r.get("fast_candidate_source", "")).startswith("key_"):
            settles.append(r)
slips = [r.get("entry_slip_bps", 0.0) for r in settles]
print(f"① 入场滑点(n={len(slips)}): 中位 {statistics.median(slips):+.1f}bps 均 {sum(slips)/len(slips):+.1f} "
      f"P90 {sorted(slips)[int(len(slips)*0.9)]:+.1f}")

stops = [r for r in settles if r.get("reason") == "fast_key_stop"]
if stops:
    ex = []
    for r in stops:
        bps = r["realized_usdt"] / r["notional"] * 1e4
        theo = -150.0 - (r.get("fee_usdt", 0) / r["notional"] * 1e4)
        ex.append(bps - theo)  # 负=穿线损耗
    print(f"③ 止损穿线(n={len(stops)}): 实收vs理论 中位 {statistics.median(ex):+.1f}bps 最差 {min(ex):+.1f} "
          f"(实收中位 {statistics.median([r['realized_usdt']/r['notional']*1e4 for r in stops]):+.1f})")
else:
    print("③ 止损穿线: n=0")

# ---- ② 未成交机会成本 ----
attempts = []
with open(f"{REC}/fast_maker_attempts.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if str(r.get("score_reason", "")).startswith("key_") and not r.get("maker_filled"):
            attempts.append(r)
print(f"② 未成交尝试 n={len(attempts)}")
if attempts:
    # 30s 网格反事实:按挂单价成交,同款出场(止损-150/固定30m),taker 出场费 5+maker 进 2=7bps 计
    need = {}
    for a in attempts:
        need.setdefault(a["symbol"], []).append(ts(a["recorded_at"]))
    for v in need.values():
        v.sort()
    paths = {}
    with open(f"{REC}/fast_market_mids.jsonl") as f:
        for line in f:
            p = line.find('"recorded_at":"')
            if p < 0:
                continue
            try:
                t = ts(line[p + 15:p + 34])
            except Exception:
                continue
            row = None
            for sym, t0s in need.items():
                j = bisect.bisect_right(t0s, t) - 1
                if j >= 0 and t0s[j] <= t <= t0s[j] + 1900:
                    if row is None:
                        try:
                            row = json.loads(line).get("mids") or {}
                        except Exception:
                            row = {}
                    m = row.get(sym)
                    if m:
                        paths.setdefault((sym, t0s[j]), []).append((t, m))
    miss = []
    for a in attempts:
        key = (a["symbol"], None)
        t0s = need[a["symbol"]]
        t0 = min(t0s, key=lambda x: abs(x - ts(a["recorded_at"])))
        pts = sorted(paths.get((a["symbol"], t0), []))
        ent = a.get("maker_order_price", 0)
        if not pts or ent <= 0:
            continue
        sgn = 1 if a.get("side") == "long" else -1
        px = None
        for t, m in pts:
            u = sgn * (m - ent) / ent * 1e4
            if t - t0 >= 1800 or u <= -150:
                px = m
                break
            px = m
        if px is None:
            continue
        miss.append(max(sgn * (px - ent) / ent * 1e4, -180) - 7)
    if miss:
        print(f"  反事实可评 n={len(miss)}: 错过净中位 {statistics.median(miss):+.1f}bps 均 {sum(miss)/len(miss):+.1f} "
              f"合计 {sum(miss):+.0f}bps(正=漏掉的是赢单=机会成本;负=挂单未成反而躲亏)")
    else:
        print("  反事实路径覆盖不足")

# ---- 全账小结 ----
n = len(settles)
if n:
    tot = sum(r["realized_usdt"] for r in settles)
    fees = sum(r.get("fee_usdt", 0) for r in settles)
    print(f"\n全账:已结算 {n} 笔 净{tot:+.4f}U 其中费用 {fees:.4f}U(≈{fees/sum(r['notional'] for r in settles)*1e4:.1f}bps/笔名义)")
    print("『实际门票』按令④须三件齐报:费用+滑点为显性,穿线与未成交机会成本为隐性,单独引用任一项=违规。")
