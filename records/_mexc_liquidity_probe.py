#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""MEXC 换战场前置检验之一:长尾真实深度/点差 REST 快照对比(2026-07-06,零成本只读)。

口径:取我方 census 最近 24h 暴动次数最多的币 ∩ MEXC 合约清单,对每币同时拉
OKX 与 MEXC 盘口,算 spread_bps 与 ±10bps 内深度名义(USDT)。
MEXC vol=张数,names 换算=px*vol*contractSize;OKX sz=张数,换算=px*sz*ctVal。
注意:这是时点快照,只回答"数量级是否同一世界",不回答成交冲击;磁带纯度另测。
"""
import json
import collections
import time
import urllib.request
import statistics
import os

HERE = os.path.dirname(os.path.abspath(__file__))
REC = os.path.join(HERE, "okx_directional")


def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "lingxi-probe/1.0"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


# ---- 样本币:census 最近 24h 暴动榜 ----
now = time.time()
cnt = collections.Counter()
with open(REC + "/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        ts = d.get("recorded_at", "")
        if ts >= "2026-07-05T04:":
            cnt[d.get("symbol", "")] += 1
top = [s for s, _ in cnt.most_common(40) if s.endswith("-USDT")]

# ---- MEXC 合约清单(contractSize)----
mx = get("https://contract.mexc.com/api/v1/contract/detail")
mx_meta = {}
for c in mx.get("data", []):
    if c.get("quoteCoin") == "USDT" and c.get("futureType") == 1:
        mx_meta[c["symbol"]] = float(c.get("contractSize", 0))

# ---- OKX instruments(ctVal)----
okx_inst = get("https://www.okx.com/api/v5/public/instruments?instType=SWAP")
okx_meta = {}
for c in okx_inst.get("data", []):
    if c.get("settleCcy") == "USDT":
        okx_meta[c["instId"]] = float(c.get("ctVal", 0))


def book_stats(bids, asks, conv):
    """bids/asks=[(px,qty)];conv(px,qty)->notional。返回 (spread_bps, depth10_usdt)"""
    if not bids or not asks:
        return None
    bb, ba = bids[0][0], asks[0][0]
    mid = (bb + ba) / 2
    spread = (ba - bb) / mid * 1e4
    lo, hi = mid * (1 - 0.001), mid * (1 + 0.001)
    d = sum(conv(px, q) for px, q in bids if px >= lo) + sum(conv(px, q) for px, q in asks if px <= hi)
    return spread, d


rows = []
for sym in top:
    base = sym[:-5]
    mxsym = base + "_USDT"
    okid = sym + "-SWAP"
    if mxsym not in mx_meta or okid not in okx_meta:
        continue
    try:
        ob = get(f"https://www.okx.com/api/v5/market/books?instId={okid}&sz=50")["data"][0]
        okb = [(float(x[0]), float(x[1])) for x in ob["bids"]]
        oka = [(float(x[0]), float(x[1])) for x in ob["asks"]]
        ctval = okx_meta[okid]
        ok = book_stats(okb, oka, lambda px, q: px * q * ctval)

        mb = get(f"https://contract.mexc.com/api/v1/contract/depth/{mxsym}?limit=50")["data"]
        mxb = [(float(x[0]), float(x[1])) for x in mb["bids"]]
        mxa = [(float(x[0]), float(x[1])) for x in mb["asks"]]
        cs = mx_meta[mxsym]
        mxs = book_stats(mxb, mxa, lambda px, q: px * q * cs)
    except Exception as ex:
        print(f"  {sym}: 拉取失败 {ex}")
        continue
    if not ok or not mxs:
        continue
    rows.append((sym, cnt[sym], ok[0], ok[1], mxs[0], mxs[1]))
    time.sleep(0.25)

print(f"\n样本={len(rows)} 币(census 24h 暴动榜 ∩ 两所同上市)\n")
print(f"{'币':<14}{'暴动':>5} | {'OKX点差':>8}{'OKX深度10bp':>12} | {'MEXC点差':>9}{'MEXC深度10bp':>13} | 深度比")
for sym, n, os_, od, ms, md in sorted(rows, key=lambda r: -r[1]):
    ratio = md / od if od > 0 else float("nan")
    print(f"{sym:<14}{n:>5} | {os_:>7.1f} {od:>11,.0f} | {ms:>8.1f} {md:>12,.0f} | {ratio:>5.2f}x")
if rows:
    print(f"\n中位数: OKX 点差 {statistics.median(r[2] for r in rows):.1f}bps 深度 {statistics.median(r[3] for r in rows):,.0f}U"
          f" | MEXC 点差 {statistics.median(r[4] for r in rows):.1f}bps 深度 {statistics.median(r[5] for r in rows):,.0f}U"
          f" | 深度比中位 {statistics.median((r[5]/r[3]) for r in rows if r[3]>0):.2f}x")
    both = [r for r in rows if r[4] <= 12 and 5000 <= r[5]]
    print(f"过我方资格闸(点差≤12 且深度≥5k)的 MEXC 币: {len(both)}/{len(rows)}")
