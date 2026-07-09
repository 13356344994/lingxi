#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""长历史天气×钥匙验证管线(2026-07-09;纪律 14i"5天窗=一种天气的女儿"的解药;只读)。

数据:records/history/okx_5m/*.jsonl(60天×成交额前150币)+ bybit_funding/*.jsonl(一年)。
下载器跑完后运行;跑完前可用已落地文件冒烟(--smoke 只解析验证)。

三问(全部按周分层报告=天气多样性检验):
  ①奖品气候学:≥300bps 的 30-120m 移动,每周多少、什么天气(全市场暴动率三分位)下多少;
  ②WHERE 钥匙稳定性:前24h暴动计数(5m|ret|≥25 bar 数)对奖品的 AUC,逐周出数——
    0.91 是三天读数,它在 9 周不同天气里站不站得住;
  ③资费钥匙(#8b 长历史版):极端负资费币结算后 30m/2h,逐周×天气分层。
用法:python3 _weather_key_longhist.py [--smoke]
"""
import json, os, sys, bisect, statistics, random
from datetime import datetime, timezone

random.seed(7)
HIST = "/root/lingxi/records/history"
PRIZE_BPS = 300.0
RIOT_BPS = 25.0
SMOKE = "--smoke" in sys.argv

def load_candles(path):
    """→ [(ts_sec, close)] 升序;JSONL 行 {"data":[ts,o,h,l,c,...]}。"""
    out = []
    try:
        with open(path) as f:
            for line in f:
                try:
                    d = json.loads(line)["data"]
                    out.append((int(d[0]) / 1000.0, float(d[4])))
                except Exception:
                    continue
    except FileNotFoundError:
        return []
    out.sort()
    ded = []
    for t, c in out:
        if ded and abs(t - ded[-1][0]) < 1:
            continue
        ded.append((t, c))
    return ded

# ---- 载入 5m ----
coins = {}
d5 = f"{HIST}/okx_5m"
files = sorted(os.listdir(d5)) if os.path.isdir(d5) else []
for fn in files:
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-SWAP", "")
    c = load_candles(f"{d5}/{fn}")
    if len(c) >= 288:  # 至少1天
        coins[sym] = c
print(f"5m 币数={len(coins)}(文件 {len(files)});", "冒烟模式" if SMOKE else "全量模式")
if not coins:
    sys.exit("无 5m 数据")
if SMOKE:
    for sym, c in list(coins.items())[:3]:
        t0, t1 = c[0][0], c[-1][0]
        print(f"  {sym}: {len(c)} bars {datetime.fromtimestamp(t0,tz=timezone.utc):%m-%d}→{datetime.fromtimestamp(t1,tz=timezone.utc):%m-%d} 收盘样例 {c[-1][1]}")

# ---- 每币 5m 收益序列与暴动 bar 集 ----
riot_bars = {}   # sym -> sorted [ts](|5m ret|≥25bps)
for sym, c in coins.items():
    rb = []
    for i in range(1, len(c)):
        if c[i - 1][1] > 0 and abs(c[i][1] / c[i - 1][1] - 1) * 1e4 >= RIOT_BPS:
            rb.append(c[i][0])
    riot_bars[sym] = rb

def riots24(sym, t):
    a = riot_bars.get(sym, [])
    return bisect.bisect_left(a, t) - bisect.bisect_left(a, t - 86400)

# ---- 全市场天气钟(共同时间网格上的暴动率)----
all_ts = sorted(set(t for c in coins.values() for t, _ in c))
wx_ratio = {}
tsidx = {sym: [x[0] for x in c] for sym, c in coins.items()}
for t in all_ts[::3]:  # 15min 采样够
    n = hit = 0
    for sym in coins:
        a = riot_bars.get(sym, [])
        j = bisect.bisect_left(tsidx[sym], t)
        if j >= len(tsidx[sym]) or abs(tsidx[sym][j] - t) > 300:
            continue
        n += 1
        k = bisect.bisect_left(a, t)
        if k > 0 and t - a[k - 1] <= 900:
            hit += 1
    if n >= 20:
        wx_ratio[t] = hit / n
wx_ts = sorted(wx_ratio)
wx_vals = sorted(wx_ratio.values())
if not wx_vals:
    sys.exit("天气钟为空")
q1, q2 = wx_vals[len(wx_vals) // 3], wx_vals[2 * len(wx_vals) // 3]

def weather(t):
    j = bisect.bisect_right(wx_ts, t) - 1
    if j < 0 or t - wx_ts[j] > 1800:
        return None
    r = wx_ratio[wx_ts[j]]
    return "死水" if r <= q1 else ("风暴" if r > q2 else "平静")

print(f"天气钟点={len(wx_ts)} 三分位切点={q1:.3f}/{q2:.3f}")
if SMOKE:
    sys.exit(0)

# ---- ① 奖品扫描 + ② WHERE 钥匙逐周 AUC ----
def week_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%G-W%V")

prizes = []
for sym, c in coins.items():
    tsa = [x[0] for x in c]
    lastT = 0.0
    for i, (t, px) in enumerate(c):
        if t - lastT < 7200:
            continue
        best = 0.0
        for H in (1800, 3600, 7200):
            j = bisect.bisect_right(tsa, t + H) - 1
            if j > i and tsa[j] - t >= H * 0.8 and px > 0:
                r = (c[j][1] - px) / px * 1e4
                if abs(r) > abs(best):
                    best = r
        if abs(best) < PRIZE_BPS:
            continue
        lastT = t
        prizes.append(dict(sym=sym, t0=t, ret=best, wk=week_of(t), wx=weather(t)))
print(f"\n奖品总数={len(prizes)}(≥{PRIZE_BPS:.0f}bps/30-120m,60天,150币)")

# 对照:同时刻随机币(当刻在场、非奖品币)
prize_sym_day = set((p["sym"], int(p["t0"] // 86400)) for p in prizes)
controls = []
symlist = list(coins)
for p in prizes:
    got = tries = 0
    while got < 2 and tries < 40:
        tries += 1
        s = random.choice(symlist)
        if s == p["sym"] or (s, int(p["t0"] // 86400)) in prize_sym_day:
            continue
        j = bisect.bisect_left(tsidx[s], p["t0"])
        if j >= len(tsidx[s]) or abs(tsidx[s][j] - p["t0"]) > 300:
            continue
        controls.append(dict(sym=s, t0=p["t0"], wk=p["wk"], wx=p["wx"]))
        got += 1

def auc(pos, neg):
    if len(pos) < 10 or len(neg) < 10:
        return None
    allv = sorted([(v, 1) for v in pos] + [(v, 0) for v in neg])
    rs = i = 0
    while i < len(allv):
        j = i
        while j < len(allv) and allv[j][0] == allv[i][0]:
            j += 1
        r = (i + j + 1) / 2
        rs += sum(r for k in range(i, j) if allv[k][1] == 1) / max(1, 1)
        # 上行有误,改為逐个累加
        i = j
    # 重算(清晰版)
    rs = 0.0
    i = 0
    while i < len(allv):
        j = i
        while j < len(allv) and allv[j][0] == allv[i][0]:
            j += 1
        r = (i + j + 1) / 2
        for k in range(i, j):
            if allv[k][1] == 1:
                rs += r
        i = j
    n1, n0 = len(pos), len(neg)
    return (rs - n1 * (n1 + 1) / 2) / (n1 * n0)

print("\n== ② WHERE 钥匙(前24h暴动计数)逐周 AUC + ① 奖品气候学 ==")
weeks = sorted(set(p["wk"] for p in prizes))
print(f"{'周':10s} {'奖品n':>6s} {'AUC':>6s}  天气分布(死水/平静/风暴 奖品数)")
for wk in weeks:
    pv = [riots24(p["sym"], p["t0"]) for p in prizes if p["wk"] == wk]
    cv = [riots24(c["sym"], c["t0"]) for c in controls if c["wk"] == wk]
    a = auc(pv, cv)
    wxd = {}
    for p in prizes:
        if p["wk"] == wk:
            wxd[p["wx"]] = wxd.get(p["wx"], 0) + 1
    print(f"{wk:10s} {len(pv):6d} {a if a is None else round(a,3)!s:>6s}  死水{wxd.get('死水',0)} 平静{wxd.get('平静',0)} 风暴{wxd.get('风暴',0)} 无钟{wxd.get(None,0)}")

# ---- ③ 资费钥匙长历史(#8b 天气版)----
print("\n== ③ 极端负资费结算后(bybit funding 史 × 5m 价格;逐周×天气)==")
fund = {}
fd = f"{HIST}/bybit_funding"
if os.path.isdir(fd):
    for fn in os.listdir(fd):
        if not fn.endswith(".jsonl"):
            continue
        sym = fn[:-6].replace("USDT", "-USDT") if not fn[:-6].endswith("-USDT") else fn[:-6]
        try:
            with open(f"{fd}/{fn}") as f:
                for line in f:
                    try:
                        d = json.loads(line)
                        fr = float(d.get("fundingRate", 0))
                        ft = int(d.get("fundingRateTimestamp", 0)) / 1000.0
                        if fr <= -0.001 and ft > 0:
                            fund.setdefault(sym, []).append(ft)
                    except Exception:
                        continue
        except Exception:
            continue
ev = []
for sym, arr in fund.items():
    if sym not in coins:
        continue
    tsa = tsidx[sym]
    for st in sorted(arr):
        i = bisect.bisect_left(tsa, st)
        if i >= len(tsa) or tsa[i] - st > 600:
            continue
        e = coins[sym][i][1]
        j30 = bisect.bisect_right(tsa, st + 1800) - 1
        j2h = bisect.bisect_right(tsa, st + 7200) - 1
        if j30 <= i or e <= 0:
            continue
        ev.append(dict(sym=sym, wk=week_of(st), wx=weather(st),
                       r30=-(coins[sym][j30][1] / e - 1) * 1e4 - 12,
                       r2h=(-(coins[sym][j2h][1] / e - 1) * 1e4 - 12) if j2h > i else None))
print(f"深桶(f≤-0.1%)结算事件(有5m价格)={len(ev)}")
byk = {}
for x in ev:
    byk.setdefault((x["wk"], x["wx"]), []).append(x)
for (wk, wx), sub in sorted(byk.items()):
    n = [x["r30"] for x in sub]
    if len(n) < 8:
        continue
    gp = sum(v for v in n if v > 0)
    gl = -sum(v for v in n if v < 0)
    syms = len(set(x["sym"] for x in sub))
    print(f"  {wk} {wx or '无钟'}: n={len(n):3d}(币{syms}) 做空30m净中位{statistics.median(n):+7.1f} PF{(gp/gl if gl>0 else 99):.2f}")
