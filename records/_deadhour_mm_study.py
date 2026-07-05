#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""死水被动做市可行性(只读;用户"全天候+不亏也行+走量飞轮"初衷的首轮检验)。

机制:死水时段长尾币点差 5-15bps,挂双边收点差(maker 费 2bps/边)。
fade 之死("maker 成交=毒")不适用的前提:死水+无瀑布时,成交对手=随机噪声单而非知情流。
模拟(保守口径,1s 盘口数据):
  - 资格:燃料<20、点差∈[6,25]bps、该币 60s 内无爆仓(两所)、无 burst、|r15|<5(真死水);
  - 挂单:双边贴 best bid/ask;成交=价格"穿过"我们的价位(严格:下一 30s 内 bid<我们买价 → 买单成交;
    穿价成交是保守下界,排队位置未知故不算 touch 成交);
  - 平仓:成交后反向同规则挂 60s,未穿则 60s 末按 mid 市价撤退(付 taker 5bps+半点差);
  - 成本:maker 2bps/边;两腿 maker=4bps;单腿 maker+taker 撤退=2+5+半点差。
产出:round-trip 净 bps 分布、双腿完成率、撤退损耗、逐币/分时稳定性。
"""
import json
import datetime
import bisect
import collections
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
SINCE = "2026-07-04T16:00"


def tsf(s):
    return datetime.datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f")


# 爆仓(两所合并,资格闸用)
liq = collections.defaultdict(list)
for fn, key in [("fast_liquidation_events.jsonl", "symbol"), ("fast_xvenue_liq_events.jsonl", "symbol")]:
    with open(os.path.join(DIR, fn)) as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d["recorded_at"] < SINCE:
                continue
            liq[d[key]].append(tsf(d["recorded_at"]))
liq_all = sorted(t for arr in liq.values() for t in arr)
for s in liq:
    liq[s].sort()


def fuel(t):
    lo = bisect.bisect_left(liq_all, t - datetime.timedelta(seconds=300))
    return bisect.bisect_right(liq_all, t) - lo


def liq_near(sym, t, sec):
    arr = liq.get(sym, [])
    i = bisect.bisect_left(arr, t - datetime.timedelta(seconds=sec))
    return i < len(arr) and arr[i] <= t


# 主扫描:按币收集 1s 盘口序列(内存换速度:只存必需字段)
books = collections.defaultdict(list)  # sym -> [(t, bid, ask, mid, r15ok, burstish)]
state_vol = collections.defaultdict(lambda: collections.deque(maxlen=60))
files = [os.path.join(DIR, "fast_hot_micro.jsonl.1"), os.path.join(DIR, "fast_hot_micro.jsonl")]
for fn in files:
    if not os.path.exists(fn):
        continue
    with open(fn) as f:
        for line in f:
            i = line.find('"recorded_at":"')
            if i < 0 or line[i + 15:i + 34] < SINCE:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            sym = d["symbol"]
            bid, ask, mid = d.get("bid", 0), d.get("ask", 0), d.get("mid", 0)
            if bid <= 0 or ask <= 0 or mid <= 0:
                continue
            tot = (d.get("trade_buy_qty_1s", 0) + d.get("trade_sell_qty_1s", 0)) * mid
            hist = state_vol[sym]
            base = sorted(hist) if hist else []
            med = base[len(base) // 2] if base else 0
            hist.append(tot)
            burstish = med > 0 and tot >= med * 3  # 略严:3×即回避
            r15 = abs(d.get("mid_ret_5s", 0)) * 10000  # 5s ret 近似静度(1s数据有该字段)
            books[sym].append((tsf(d["recorded_at"]), bid, ask, mid, r15 < 5, burstish))

print(f"盘口序列: {len(books)} 币")

FEE_M = 2.0   # maker bps/边
FEE_T = 5.0   # taker bps
QUOTE_HOLD = 30   # 入场挂单窗(s)
EXIT_HOLD = 60    # 出场挂单窗(s)
rt = []           # 完整往返净 bps
bailouts = []     # 单腿撤退净 bps
attempts = 0
fills = 0
for sym, arr in books.items():
    n = len(arr)
    if n < 300:
        continue
    keys = [x[0] for x in arr]
    i = 0
    while i < n:
        t, bid, ask, mid, calm, burstish = arr[i]
        spread_bps = (ask - bid) / mid * 10000
        # 资格闸(全部该秒实时可知)
        if not calm or burstish or not (6 <= spread_bps <= 25) or fuel(t) >= 20 or liq_near(sym, t, 60):
            i += 1
            continue
        attempts += 1
        # 双边挂 best;先看哪边先"穿价"成交(30s)
        j_end = bisect.bisect_left(keys, t + datetime.timedelta(seconds=QUOTE_HOLD))
        fill_side = None
        fill_j = None
        for j in range(i + 1, min(j_end, n)):
            _, b2, a2, m2, _, _ = arr[j]
            if b2 < bid:      # 价格穿过我们的买价 → 买单成交(保守:严格小于)
                fill_side, fill_j = "long", j
                break
            if a2 > ask:      # 穿过卖价 → 卖单成交
                fill_side, fill_j = "short", j
                break
        if fill_side is None:
            i = j_end + 1 if j_end > i else i + 1
            continue
        fills += 1
        entry = bid if fill_side == "long" else ask
        # 出场:反向挂对侧 best(以成交时刻盘口),60s 穿价;否则 mid 撤退付 taker
        _, b3, a3, m3, _, _ = arr[fill_j]
        target = a3 if fill_side == "long" else b3
        k_end = bisect.bisect_left(keys, arr[fill_j][0] + datetime.timedelta(seconds=EXIT_HOLD))
        done = False
        for k in range(fill_j + 1, min(k_end, n)):
            _, b4, a4, m4, _, _ = arr[k]
            if fill_side == "long" and a4 > target:
                gross = (target - entry) / entry * 10000
                rt.append(gross - 2 * FEE_M)
                done = True
                break
            if fill_side == "short" and b4 < target:
                gross = (entry - target) / entry * 10000
                rt.append(gross - 2 * FEE_M)
                done = True
                break
        if not done:
            kk = min(k_end, n - 1)
            m_end = arr[kk][3]
            gross = (m_end - entry) / entry * 10000 * (1 if fill_side == "long" else -1)
            half_sp = (arr[kk][2] - arr[kk][1]) / arr[kk][3] * 10000 / 2
            bailouts.append(gross - FEE_M - FEE_T - half_sp)
        i = (k_end if not done else fill_j) + 1

print(f"资格时刻: {attempts} | 入场成交(穿价保守口径): {fills}({fills/max(attempts,1)*100:.1f}%)")
allpnl = rt + bailouts
if allpnl:
    v = sorted(allpnl)
    win = sum(1 for x in v if x > 0)
    print(f"\n== 往返结果(n={len(v)}) ==")
    print(f"  双腿完成 {len(rt)}({len(rt)/len(v)*100:.0f}%): 净均={sum(rt)/max(len(rt),1):+.1f} 中位={sorted(rt)[len(rt)//2]:+.1f}bps" if rt else "  双腿完成 0")
    print(f"  单腿撤退 {len(bailouts)}: 净均={sum(bailouts)/max(len(bailouts),1):+.1f} 中位={sorted(bailouts)[len(bailouts)//2]:+.1f}bps" if bailouts else "  撤退 0")
    print(f"  合计: 净均={sum(v)/len(v):+.2f}bps 中位={v[len(v)//2]:+.1f} 胜率={win/len(v)*100:.0f}% 最差={v[0]:+.1f}")
    tot_u = sum(v) / 10000 * 5
    print(f"  若 5U/笔: 合计净 ≈ {tot_u:+.3f}U / {len(v)}笔")
EOF_MARKER = None
