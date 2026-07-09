#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""奖品地图(2026-07-09;用户宗旨令"从终点倒推,不许一条条排除";只读)。

方法倒转:不问"假设X赚不赚",问"钱在哪、开始前一刻盘面有什么"。
  奖品=非圣日数据里每一笔 ≥5×门票的真实行情(30s 网格):
    5m 尺度 |ret|≥60bps / 30m ≥100 / 2h ≥150(同币同尺度按横向期去重);
  倒推=每个奖品 t0 前 15min 的全部已记信号:burst(双源)/官方爆仓/暴动(census)/
    极端负资费/风暴州/币种(股票perp);再看奖品的方向可预测性(签名方向 vs 奖品方向)。
产出:①钱的分布(尺度×币类×州×时段)②每种签名对奖品的覆盖率与方向命中
  ③"签名→奖品"最肥的一格=下一把枪的出生地。
挖掘=06/07/09;圣日(05/08)不入内存。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}
STOCKS = {s + "-USDT" for s in ("SAMSUNG", "SKHYNIX", "SNDK", "AAPL", "SOXL", "TSLA", "NVDA", "MSTR", "COIN", "CRCL", "HOOD", "META", "AMZN", "GOOGL", "ORCL", "MCD")}
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# ---- 30s 网格价格(仅挖掘日)----
grid = {}  # sym -> [(t, mid)](30s 降采样)
gridlast = {}
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        if day_of(t) not in MINE:
            continue
        try:
            mids = json.loads(line).get("mids") or {}
        except Exception:
            continue
        for sym, m in mids.items():
            if m <= 0:
                continue
            if sym in gridlast and t - gridlast[sym] < 30:
                continue
            gridlast[sym] = t
            grid.setdefault(sym, []).append((t, m))
print(f"网格:{len(grid)} 币 × {sum(len(v) for v in grid.values())} 点")

# ---- 信号索引(倒推用;全部走前)----
def load_ts(fn, cond=None):
    out = {}
    try:
        with open(f"{REC}/{fn}") as f:
            for line in f:
                try:
                    r = json.loads(line)
                except Exception:
                    continue
                if cond and not cond(r):
                    continue
                out.setdefault(r["symbol"], []).append(ts(r["recorded_at"]))
    except FileNotFoundError:
        pass
    for v in out.values():
        v.sort()
    return out

bursts = load_ts("fast_burst_detect.jsonl")
for sym, arr in load_ts("fast_full_burst_probe.jsonl").items():
    bursts.setdefault(sym, []).extend(arr)
for v in bursts.values():
    v.sort()
liqs = load_ts("fast_liquidation_events.jsonl")
riots = load_ts("fast_mover_census.jsonl")
liq_all = sorted(t for v in liqs.values() for t in v)
# 极端负资费币集(xmeta:结算前快照 f≤-0.05% 的 (sym, 快照时刻))
fneg = {}
with open(f"{REC}/fast_xmeta_snapshots.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except Exception:
            continue
        T = ts(d["recorded_at"])
        for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
            if isinstance(v, list) and v and float(v[0]) <= -0.0005:
                fneg.setdefault(sym, []).append(T)
for v in fneg.values():
    v.sort()

def has(idx, sym, lo, hi):
    a = idx.get(sym)
    if not a:
        return False
    i = bisect.bisect_left(a, lo)
    return i < len(a) and a[i] <= hi

def fuel_at(t):
    return bisect.bisect_right(liq_all, t) - bisect.bisect_left(liq_all, t - 300)

# ---- 奖品扫描 ----
HORIZONS = [(300, 60.0, "5m"), (1800, 100.0, "30m"), (7200, 150.0, "2h")]
prizes = []
for sym, pts in grid.items():
    tsarr = [p[0] for p in pts]
    for H, TH, tag in HORIZONS:
        lastT = 0.0
        for i, (t, m) in enumerate(pts):
            if t - lastT < H:
                continue
            j = bisect.bisect_right(tsarr, t + H) - 1
            if j <= i or tsarr[j] - t < H * 0.8:
                continue
            ret = (pts[j][1] - m) / m * 1e4
            if abs(ret) < TH:
                continue
            lastT = t
            prizes.append(dict(sym=sym, t0=t, h=tag, ret=ret, day=day_of(t),
                               stock=sym in STOCKS, major=sym in MAJORS,
                               burst=has(bursts, sym, t - 900, t),
                               liq=has(liqs, sym, t - 900, t),
                               riot=has(riots, sym, t - 900, t),
                               fneg=has(fneg, sym, t - 3600, t),
                               storm=fuel_at(t) >= 40,
                               hour=datetime.fromtimestamp(t, tz=timezone.utc).hour))
print(f"奖品总数={len(prizes)}")

def agg(sub, tag):
    if not sub:
        print(f"  {tag}: 0")
        return
    meat = sum(abs(p["ret"]) for p in sub)
    up = sum(1 for p in sub if p["ret"] > 0)
    print(f"  {tag}: n={len(sub):5d} 总肉={meat/1e4:7.1f}万bps 单均={meat/len(sub):5.0f} 向上{up/len(sub)*100:.0f}%")

print("\n== ① 钱的分布 ==")
for H, TH, tag in HORIZONS:
    sub = [p for p in prizes if p["h"] == tag]
    agg(sub, f"{tag}(≥{TH:.0f}bps)")
    agg([p for p in sub if p["stock"]], f"  股票perp")
    agg([p for p in sub if not p["stock"] and not p["major"]], f"  加密长尾")
    agg([p for p in sub if p["storm"]], f"  风暴州")
    agg([p for p in sub if not p["storm"]], f"  平静州")

print("\n== ② 倒推:奖品开始前 15min 盘面上有什么(覆盖率=多少钱是有前兆的)==")
for H, TH, tag in HORIZONS:
    sub = [p for p in prizes if p["h"] == tag]
    if not sub:
        continue
    tot = sum(abs(p["ret"]) for p in sub)
    print(f"-- {tag} 尺度(总肉 {tot/1e4:.1f}万bps)--")
    for sig in ("burst", "liq", "riot", "fneg"):
        cov = [p for p in sub if p[sig]]
        m = sum(abs(p["ret"]) for p in cov)
        print(f"  {sig:6s}: 奖品覆盖 {len(cov)/len(sub)*100:4.1f}% 肉覆盖 {m/tot*100:4.1f}%")
    anyc = [p for p in sub if p["burst"] or p["liq"] or p["riot"] or p["fneg"]]
    m = sum(abs(p["ret"]) for p in anyc)
    print(f"  任一  : 奖品覆盖 {len(anyc)/len(sub)*100:4.1f}% 肉覆盖 {m/tot*100:4.1f}%")

print("\n== ③ 方向:有前兆的奖品里,前兆时刻的 15min 动量方向 vs 奖品方向 ==")
# 动量代理:t0 前 15min 网格收益(奖品自己的入场时刻可知信息)
for H, TH, tag in HORIZONS:
    sub = [p for p in prizes if p["h"] == tag and (p["burst"] or p["liq"] or p["riot"])]
    okd = 0
    n = 0
    for p in sub:
        pts = grid[p["sym"]]
        tsa = [x[0] for x in pts]
        i = bisect.bisect_right(tsa, p["t0"]) - 1
        j = bisect.bisect_right(tsa, p["t0"] - 900) - 1
        if i <= j or j < 0:
            continue
        mom = pts[i][1] - pts[j][1]
        if mom == 0:
            continue
        n += 1
        if (mom > 0) == (p["ret"] > 0):
            okd += 1
    if n:
        print(f"  {tag}: 有前兆奖品 n={n} 顺前动量方向命中 {okd/n*100:.1f}%(50%=硬币)")

print("\n== ④ 时段分布(30m 尺度,UTC 时;钱扎堆的钟点=值班表原料)==")
sub30 = [p for p in prizes if p["h"] == "30m"]
byh = {}
for p in sub30:
    byh.setdefault(p["hour"], []).append(abs(p["ret"]))
for h in sorted(byh):
    v = byh[h]
    bar = "█" * int(sum(v) / 3000)
    print(f"  {h:02d}h n={len(v):4d} 肉={sum(v)/1e4:5.1f}万 {bar}")
