#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""方向钥匙终审:rubik 主动买卖流 × 多空比(2026-07-10;死线裁决;只读)。

这是方向钥匙的最后一块非价格流。若本轮 holdout 净中位仍负 → 本池塘定论关闭(用户死线)。

数据:okx_5m K线(200 币×60 天)+ rubik_taker_1h([ts,sellVol,buyVol],30 天)+
      rubik_lsratio_1h([ts,ratio],30 天)。
触发(冻结):5m bar |ret|≥25bps(暴动)× 滚动惯犯(回看 24h 暴动 bar 数,阈=train 期 75 分位,
     每时点重算全币一致)× 同币 3600s 去重;窗口=rubik 重叠 30 天。
方向规则(全部走前,只用已完结 1H bar):
     taker 失衡 imb1h/imb4h 顺流与逆流;多空比 4h 变化顺/逆(散户加多→反着打=经典逆向)。
持有:30m / 2h,止损 -150 收盘价基准(死板),成本 12。
出分:train=前 23 天 / holdout=后 7 天 × 天气(全市场暴动率三分位);
交付=holdout 净中位排序清单+供给。
"""
import json, os, bisect, statistics
from datetime import datetime, timezone

H = "/root/lingxi/records/history"
COST = 12.0
STOP = 150.0
RIOT = 25.0

def load_klines(d):
    out = {}
    for fn in sorted(os.listdir(d)):
        if not fn.endswith(".jsonl"):
            continue
        sym = fn[:-6].replace("-SWAP", "").replace("-USDT", "")
        rows = []
        with open(f"{d}/{fn}") as f:
            for line in f:
                try:
                    x = json.loads(line)["data"]
                    rows.append((int(x[0]) / 1000.0, float(x[4])))
                except Exception:
                    continue
        rows = sorted(set(rows))
        if len(rows) > 500:
            out[sym] = rows
    return out

coins = load_klines(f"{H}/okx_5m")
print(f"5m 币数={len(coins)}")

def load_rubik(d, three):
    out = {}
    for fn in os.listdir(d):
        if not fn.endswith(".jsonl"):
            continue
        ccy = fn[:-6]
        rows = []
        with open(f"{H.split('/history')[0]}/history/{os.path.basename(d)}/{fn}") as f:
            for line in f:
                try:
                    x = json.loads(line)["data"]
                    if three:
                        rows.append((int(x[0]) / 1000.0, float(x[1]), float(x[2])))  # ts, sell, buy
                    else:
                        rows.append((int(x[0]) / 1000.0, float(x[1])))
                except Exception:
                    continue
        rows = sorted(set(rows))
        if rows:
            out[ccy] = rows
    return out

taker = load_rubik(f"{H}/rubik_taker_1h", True)
lsr = load_rubik(f"{H}/rubik_lsratio_1h", False)
print(f"rubik taker={len(taker)} lsr={len(lsr)}")

def imb(ccy, t0, hours):
    """已完结 1H bar(bar_ts+3600<=t0)最近 hours 根的 (buy-sell)/(buy+sell);None=数据缺。"""
    a = taker.get(ccy)
    if not a:
        return None
    tsa = [x[0] for x in a]
    j = bisect.bisect_right(tsa, t0 - 3600) - 1
    if j < hours - 1:
        return None
    if t0 - a[j][0] > 3 * 3600:
        return None
    s = b = 0.0
    for k in range(j - hours + 1, j + 1):
        s += a[k][1]
        b += a[k][2]
    if s + b <= 0:
        return None
    return (b - s) / (b + s)

def lsr_chg(ccy, t0, hours=4):
    a = lsr.get(ccy)
    if not a:
        return None
    tsa = [x[0] for x in a]
    j = bisect.bisect_right(tsa, t0 - 3600) - 1
    if j < hours or t0 - a[j][0] > 3 * 3600:
        return None
    prev = a[j - hours][1]
    if prev <= 0:
        return None
    return a[j][1] / prev - 1

# 暴动 bar 与惯犯
riot_ts = {}
for sym, c in coins.items():
    rb = []
    for i in range(1, len(c)):
        if c[i - 1][1] > 0 and abs(c[i][1] / c[i - 1][1] - 1) * 1e4 >= RIOT:
            rb.append(c[i][0])
    riot_ts[sym] = rb
tsidx = {s: [x[0] for x in c] for s, c in coins.items()}

def habit(sym, t):
    a = riot_ts[sym]
    return bisect.bisect_left(a, t) - bisect.bisect_left(a, t - 86400)

# 天气钟(小时暴动数)
hours_ct = {}
for rb in riot_ts.values():
    for t in rb:
        hours_ct[int(t // 3600)] = hours_ct.get(int(t // 3600), 0) + 1

# 窗口=rubik 重叠期
t_lo = max(min(a[0][0] for a in taker.values()), min(min(x[0] for x in c) for c in coins.values()))
t_hi = max(max(x[0] for x in c) for c in coins.values())
split = t_hi - 7 * 86400
print(f"窗口 {datetime.fromtimestamp(t_lo,tz=timezone.utc):%m-%d} → {datetime.fromtimestamp(t_hi,tz=timezone.utc):%m-%d},holdout=后7天")

# 惯犯阈=train 期暴动时点 habit 的 75 分位(train-only)
hv = []
for sym, rb in riot_ts.items():
    for t in rb[::5]:
        if t_lo <= t < split:
            hv.append(habit(sym, t))
hv.sort()
HAB = hv[int(len(hv) * 0.75)] if hv else 30
wxv = sorted(hours_ct.values())
q1, q2 = wxv[len(wxv) // 3], wxv[2 * len(wxv) // 3]
print(f"惯犯阈(train 75分位)={HAB} 天气三分位={q1}/{q2}")

def weather(t):
    n = hours_ct.get(int(t // 3600), 0)
    return "死水" if n <= q1 else ("风暴" if n > q2 else "平静")

# 触发+快照+双向持有
rows = []
for sym, c in coins.items():
    tsa = tsidx[sym]
    rb = riot_ts[sym]
    last = 0.0
    for t in rb:
        if t < t_lo + 86400 or t - last < 3600:
            continue
        if habit(sym, t) < HAB:
            continue
        i0 = bisect.bisect_left(tsa, t)
        if i0 >= len(c):
            continue
        ent = c[i0][1]
        seg = c[i0:bisect.bisect_right(tsa, t + 7500)]
        if len(seg) < 8 or ent <= 0:
            continue
        last = t
        sig = dict(i1=imb(sym, t, 1), i4=imb(sym, t, 4), lc=lsr_chg(sym, t, 4),
                   r5=(c[i0][1] / c[i0 - 1][1] - 1) * 1e4 if i0 >= 1 and c[i0 - 1][1] > 0 else 0)
        res = {}
        for sgn, stag in ((1, "L"), (-1, "S")):
            for cap, htag in ((1800, "30m"), (7200, "2h")):
                px = None
                for tt, m in seg:
                    u = sgn * (m / ent - 1) * 1e4
                    if tt - t >= cap or u <= -STOP:
                        px = m
                        break
                    px = m
                res[stag + htag] = max(sgn * (px / ent - 1) * 1e4, -STOP - 30) - COST
        rows.append(dict(sym=sym, t0=t, sig=sig, res=res, wx=weather(t),
                         part="ho" if t >= split else "tr"))
print(f"触发={len(rows)}(tr={sum(1 for r in rows if r['part']=='tr')} ho={sum(1 for r in rows if r['part']=='ho')})"
      f" rubik覆盖 i1={sum(1 for r in rows if r['sig']['i1'] is not None)}")

RULES = [
    ("顺taker流1h(imb>+.1→L)", lambda s: "L" if s["i1"] is not None and s["i1"] > 0.1 else None),
    ("逆taker流1h(imb>+.1→S)", lambda s: "S" if s["i1"] is not None and s["i1"] > 0.1 else None),
    ("顺taker流1h(imb<-.1→S)", lambda s: "S" if s["i1"] is not None and s["i1"] < -0.1 else None),
    ("逆taker流1h(imb<-.1→L)", lambda s: "L" if s["i1"] is not None and s["i1"] < -0.1 else None),
    ("顺taker流4h(→L)", lambda s: "L" if s["i4"] is not None and s["i4"] > 0.1 else None),
    ("逆taker流4h(→S)", lambda s: "S" if s["i4"] is not None and s["i4"] > 0.1 else None),
    ("顺taker流4h(→S)", lambda s: "S" if s["i4"] is not None and s["i4"] < -0.1 else None),
    ("逆taker流4h(→L)", lambda s: "L" if s["i4"] is not None and s["i4"] < -0.1 else None),
    ("散户加多→S(lsr+5%)", lambda s: "S" if s["lc"] is not None and s["lc"] > 0.05 else None),
    ("散户减多→L(lsr-5%)", lambda s: "L" if s["lc"] is not None and s["lc"] < -0.05 else None),
    ("散户加多→L(反检)", lambda s: "L" if s["lc"] is not None and s["lc"] > 0.05 else None),
    ("流×暴动同向顺(r5>0且i1>.1→L)", lambda s: "L" if s["i1"] is not None and s["i1"] > 0.1 and s["r5"] > 0 else None),
    ("流×暴动背离(r5>0且i1<-.1→S)", lambda s: "S" if s["i1"] is not None and s["i1"] < -0.1 and s["r5"] > 0 else None),
    ("流×暴动背离(r5<0且i1>.1→L)", lambda s: "L" if s["i1"] is not None and s["i1"] > 0.1 and s["r5"] < 0 else None),
]

def stat(v):
    if len(v) < 10:
        return None
    gp = sum(x for x in v if x > 0)
    gl = -sum(x for x in v if x < 0)
    return dict(n=len(v), med=statistics.median(v), pf=(gp / gl if gl > 0 else 99),
                win=sum(1 for x in v if x > 0) / len(v))

deliver = []
for name, rule in RULES:
    for htag in ("30m", "2h"):
        parts = []
        ho_med = None
        for pt in ("tr", "ho"):
            sel = [(r["res"][rule(r["sig"]) + htag], r["wx"]) for r in rows
                   if r["part"] == pt and rule(r["sig"]) is not None]
            st = stat([x[0] for x in sel])
            if st is None:
                parts.append(f"{pt}:n<10")
                continue
            wxs = ""
            for wx in ("风暴", "平静", "死水"):
                v = [x[0] for x in sel if x[1] == wx]
                if len(v) >= 8:
                    wxs += f" {wx}{statistics.median(v):+.0f}"
            parts.append(f"{pt}: n={st['n']} 中位{st['med']:+.1f} PF{st['pf']:.2f} 胜{st['win']*100:.0f}%{wxs}")
            if pt == "ho":
                ho_med = st["med"]
        print(f"{name:26s}×{htag} | {' | '.join(parts)}")
        if ho_med is not None:
            deliver.append((ho_med, f"{name}×{htag}"))

print("\n== 终审交付清单(holdout=最近7天,净中位降序)==")
for med, line in sorted(deliver, reverse=True)[:8]:
    print(f"  {med:+7.1f} bps/笔  {line}")
