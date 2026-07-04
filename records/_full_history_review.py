#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""全史横评(只读):v1 交易所账本 + v2/v3/v4 fast 结算 + 慢线全史,找跨代持续为正的口袋。

问题意识:每一代都有"当代看起来正"的桶,但多数是单代幸存者。真正值得重建的方向,
必须在 v1(6304往返)和 v2/v3/v4(655+笔)两个独立世代里都出现同号证据。
"""
import json
import datetime
import collections
import os

HERE = os.path.dirname(os.path.abspath(__file__))

# 股票/ETF perp(小池塘/低效率假设的载体)
STOCK_PERPS = {"SKHYNIX", "SNDK", "MU", "MRVL", "DRAM", "KLAC", "ASML", "CRCL", "MSTR",
               "SOXL", "IREN", "HIMS", "KIOXIA", "EWY", "AAPL", "TSLA", "NVDA", "COIN",
               "HOOD", "GME", "SBET", "CRWD", "PLTR", "QQQ", "SPY", "GLD", "AMD", "INTC",
               "SMCI", "ARM", "AVGO", "TSM", "BABA", "META", "AMZN", "GOOGL", "MSFT", "NFLX"}


def sym_class(sym):
    base = sym.replace("-USDT", "")
    return "stock_perp" if base in STOCK_PERPS else "crypto"


# ============ A. v1 交易所账本(fills → 往返重建) ============
def load_v1():
    fills = []
    with open(os.path.join(HERE, "fast_exchange_fills_cache.jsonl")) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            cl = d.get("clOrdId", "")
            if "fast" not in cl:  # 只要 fast 线(fastopen/fastmaker/fastclose)
                continue
            fills.append(dict(
                sym=d["instId"].replace("-SWAP", ""),
                side=d["side"], px=float(d["fillPx"]), sz=float(d["fillSz"]),
                fee=abs(float(d.get("fee", 0) or 0)), ts=int(d["fillTime"]),
                open=("open" in cl or "maker" in cl), pnl=float(d.get("fillPnl", 0) or 0)))
    fills.sort(key=lambda x: x["ts"])
    # 按 symbol 配对:open fill(s) → close fill(s) 聚合为往返(fast 单仓不加仓,简单配对足够)
    rts = []
    pos = {}
    for f in fills:
        k = f["sym"]
        if f["open"]:
            if k in pos:  # 前一笔未闭合(部分成交/异常),丢弃旧的
                pos.pop(k)
            pos[k] = dict(sym=k, side=f["side"], entry=f["px"], sz=f["sz"], fee=f["fee"], ts=f["ts"])
        else:
            p = pos.pop(k, None)
            if p is None:
                continue
            hold = (f["ts"] - p["ts"]) / 1000.0
            if hold < 0 or hold > 3600:
                continue
            sgn = 1 if p["side"] == "buy" else -1
            gross_bps = (f["px"] - p["entry"]) / p["entry"] * 10000 * sgn
            notional = p["entry"] * p["sz"]
            net_usdt = gross_bps / 10000 * notional - p["fee"] - f["fee"]
            rts.append(dict(sym=k, hold=hold, gross_bps=gross_bps, net=net_usdt,
                            fee=p["fee"] + f["fee"], notional=notional,
                            hour=datetime.datetime.utcfromtimestamp(p["ts"] / 1000).hour))
    return rts


# ============ B. v2/v3/v4 fast 结算 ============
def load_v2():
    out = []
    with open(os.path.join(HERE, "okx_directional", "fast_v2_settlements.jsonl")) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            n = d.get("notional", 5) or 5
            out.append(dict(sym=d["symbol"], hold=d.get("hold_seconds", 0),
                            net=d.get("realized_usdt", 0), fee=d.get("fee_usdt", 0),
                            gross_bps=(d.get("realized_usdt", 0) + d.get("fee_usdt", 0)) / n * 10000,
                            reason=d.get("reason", ""), src=d.get("fast_candidate_source") or "scorer",
                            maker=d.get("maker_entry", False),
                            hour=int(d["time"][11:13]), t=d["time"]))
    return out


# ============ C. 慢线全史 ============
def load_slow(venue):
    out = []
    path = os.path.join(HERE, f"{venue}_directional", "settlements.jsonl")
    if not os.path.exists(path):
        return out
    with open(path) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d.get("signal_layer") == "fast_v1":
                continue
            out.append(dict(sym=d["symbol"], net=d.get("realized_usdt", 0),
                            hold_m=d.get("hold_minutes", 0), reason=d.get("reason", ""), t=d["time"]))
    return out


def agg(rows, key, order=None):
    g = collections.defaultdict(lambda: [0, 0.0, 0.0, 0])
    for r in rows:
        k = key(r)
        g[k][0] += 1
        g[k][1] += r["net"]
        g[k][2] += r.get("gross_bps", 0)
        g[k][3] += 1 if r["net"] > 0 else 0
    keys = order if order else sorted(g)
    lines = []
    for k in keys:
        if k not in g:
            continue
        n, net, gb, w = g[k]
        lines.append(f"    {str(k):22s} n={n:5d} 净={net:+8.3f}U 毛均={gb/n:+6.1f}bps 胜率={w/n*100:3.0f}%")
    return "\n".join(lines)


def hold_bucket(h):
    for lo, hi, tag in [(0, 10, "00-10s"), (10, 30, "10-30s"), (30, 60, "30-60s"),
                        (60, 180, "60-180s"), (180, 1e9, "180s+")]:
        if lo <= h < hi:
            return tag
    return "?"


HOLD_ORDER = ["00-10s", "10-30s", "30-60s", "60-180s", "180s+"]

v1 = load_v1()
v2 = load_v2()
print(f"=== A. v1 交易所账本重建: {len(v1)} 往返, 净={sum(r['net'] for r in v1):+.2f}U, 费={sum(r['fee'] for r in v1):.2f}U ===")
print("  按持有时长:")
print(agg(v1, lambda r: hold_bucket(r["hold"]), HOLD_ORDER))
print("  按标的类型:")
print(agg(v1, lambda r: sym_class(r["sym"])))
print("  股票perp×持有:")
print(agg([r for r in v1 if sym_class(r["sym"]) == "stock_perp"], lambda r: hold_bucket(r["hold"]), HOLD_ORDER))
print("  长尾crypto×持有(60s+):")
print(agg([r for r in v1 if sym_class(r["sym"]) == "crypto"], lambda r: hold_bucket(r["hold"]), HOLD_ORDER))

print(f"\n=== B. v2/v3/v4 fast 结算: {len(v2)} 笔, 净={sum(r['net'] for r in v2):+.2f}U, 费={sum(r['fee'] for r in v2):.2f}U ===")
print("  按持有时长(跨代对照 v1 同桶):")
print(agg(v2, lambda r: hold_bucket(r["hold"]), HOLD_ORDER))
print("  按出场原因:")
print(agg(v2, lambda r: r["reason"]))
print("  按来源:")
print(agg(v2, lambda r: r["src"]))
print("  按标的类型:")
print(agg(v2, lambda r: sym_class(r["sym"])))
print("  trail_tp 单独看(唯一嫌疑正口袋)×标的类型:")
print(agg([r for r in v2 if r["reason"] == "fast_trail_tp"], lambda r: sym_class(r["sym"])))
print("  trail_tp×持有:")
print(agg([r for r in v2 if r["reason"] == "fast_trail_tp"], lambda r: hold_bucket(r["hold"]), HOLD_ORDER))

# 跨代同桶对照表
print("\n=== C. 跨代同桶对照(同桶两代同号才算真的) ===")
for tag in HOLD_ORDER:
    a = [r for r in v1 if hold_bucket(r["hold"]) == tag]
    b = [r for r in v2 if hold_bucket(r["hold"]) == tag]
    ga = sum(r["gross_bps"] for r in a) / len(a) if a else 0
    gb = sum(r["gross_bps"] for r in b) / len(b) if b else 0
    agree = "✓同号" if (ga > 0) == (gb > 0) and a and b else "✗"
    print(f"  {tag:8s} v1毛均={ga:+6.1f}bps(n={len(a):4d}) | v2毛均={gb:+6.1f}bps(n={len(b):4d}) {agree}")
for cls in ["stock_perp", "crypto"]:
    a = [r for r in v1 if sym_class(r["sym"]) == cls]
    b = [r for r in v2 if sym_class(r["sym"]) == cls]
    ga = sum(r["gross_bps"] for r in a) / len(a) if a else 0
    gb = sum(r["gross_bps"] for r in b) / len(b) if b else 0
    agree = "✓同号" if (ga > 0) == (gb > 0) and a and b else "✗"
    print(f"  {cls:10s} v1毛均={ga:+6.1f}bps(n={len(a):4d}) | v2毛均={gb:+6.1f}bps(n={len(b):4d}) {agree}")

print("\n=== D. 慢线全史 ===")
for v in ["okx", "bybit", "binance"]:
    rows = load_slow(v)
    if not rows:
        continue
    net = sum(r["net"] for r in rows)
    w = sum(1 for r in rows if r["net"] > 0)
    print(f"  {v}: n={len(rows)} 净={net:+.2f}U 胜率={w/len(rows)*100:.0f}%")
    # 近3天 vs 更早(记录被07-02事故回滚,近期才连续)
    recent = [r for r in rows if r["t"] >= "2026-07-02"]
    if recent:
        print(f"    07-02后: n={len(recent)} 净={sum(r['net'] for r in recent):+.2f}U")

# v1 60s+ 桶按币逐个看(防单币幸存者)
print("\n=== E. v1 60s+ 桶按币 top(防单币幸存者) ===")
sub = [r for r in v1 if r["hold"] >= 60]
bysym = collections.defaultdict(lambda: [0, 0.0])
for r in sub:
    bysym[r["sym"]][0] += 1
    bysym[r["sym"]][1] += r["net"]
for k, (n, net) in sorted(bysym.items(), key=lambda x: -x[1][1])[:8]:
    print(f"    {k:16s} n={n:4d} 净={net:+.3f}U")
neg = sum(1 for k, (n, net) in bysym.items() if net < 0)
print(f"    (共{len(bysym)}币, 其中净负{neg}个)")
