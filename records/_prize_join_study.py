#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""四号任务:奖品 × 非价格流 join(2026-07-09;用户宗旨令;只读)。

奖品=≥300bps 的 30~120min 移动(30s 网格,同币 7200s 去重);
快照=起点前 15min 冻结(全部走前):bybit资费/OI 5min变化/本币爆仓打印(分向)/
burst次数与方向(双源)/点差(hot_micro,坑内币)/跨所价差(hot_mids bybit锚)/时段/前24h惯犯;
对照组=同一分钟、当日无任何奖品的随机币(每奖品配 2 个),同样快照;
读法=天气钥匙:特征在奖品组 vs 对照组分布拉得开、且 train(06/07)/holdout(09)两侧都拉得开;
方向不先验拍板:分桶后由 train 定方向,holdout 验证;
产出=按预期净 bps 排序的候选清单 + 供给/容量。
"""
import json, bisect, statistics, random
from datetime import datetime, timezone

random.seed(42)
REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}
TRAIN = {"2026-07-06", "2026-07-07"}
PRIZE_BPS = 300.0
HORIZONS = (1800, 3600, 7200)

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# ============ 网格 ============
grid, gridlast = {}, {}
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
print(f"网格 {len(grid)} 币")

# ============ 奖品扫描 ============
prizes = []
prize_coin_day = set()
for sym, pts in grid.items():
    tsa = [x[0] for x in pts]
    lastT = 0.0
    for i, (t, m) in enumerate(pts):
        if t - lastT < 7200:
            continue
        best = 0.0
        for H in HORIZONS:
            j = bisect.bisect_right(tsa, t + H) - 1
            if j > i and tsa[j] - t >= H * 0.8:
                r = (pts[j][1] - m) / m * 1e4
                if abs(r) > abs(best):
                    best = r
        if abs(best) < PRIZE_BPS:
            continue
        lastT = t
        prizes.append(dict(sym=sym, t0=t, ret=best, day=day_of(t)))
        prize_coin_day.add((sym, day_of(t)))
print(f"奖品(≥{PRIZE_BPS:.0f}bps/30-120m)={len(prizes)}")

# ============ 对照组:同分钟、当日无奖品的随机币 ============
controls = []
coins = list(grid.keys())
for p in prizes:
    got = 0
    tries = 0
    while got < 2 and tries < 60:
        tries += 1
        c = random.choice(coins)
        if (c, p["day"]) in prize_coin_day or c == p["sym"]:
            continue
        pts = grid[c]
        tsa = [x[0] for x in pts]
        i = bisect.bisect_right(tsa, p["t0"]) - 1
        if i < 0 or p["t0"] - tsa[i] > 60:
            continue
        controls.append(dict(sym=c, t0=p["t0"], ret=0.0, day=p["day"]))
        got += 1
print(f"对照={len(controls)}")

# ============ 非价格流索引 ============
def load_sided(fn, side_key):
    out = {}
    try:
        with open(f"{REC}/{fn}") as f:
            for line in f:
                try:
                    r = json.loads(line)
                except Exception:
                    continue
                out.setdefault(r["symbol"], []).append((ts(r["recorded_at"]), r.get(side_key, "")))
    except FileNotFoundError:
        pass
    for v in out.values():
        v.sort()
    return out

liqs = load_sided("fast_liquidation_events.jsonl", "pos_side")
bursts = load_sided("fast_burst_detect.jsonl", "side")
for sym, arr in load_sided("fast_full_burst_probe.jsonl", "side").items():
    bursts.setdefault(sym, []).extend(arr)
for v in bursts.values():
    v.sort()
riots = load_sided("fast_mover_census.jsonl", "symbol")  # side 无义,只用时刻

def count_sided(idx, sym, lo, hi, want=None):
    a = idx.get(sym)
    if not a:
        return 0
    tsa = [x[0] for x in a]
    i, j = bisect.bisect_left(tsa, lo), bisect.bisect_right(tsa, hi)
    if want is None:
        return j - i
    return sum(1 for k in range(i, j) if a[k][1] == want)

# xmeta:bybit 资费 + okx OI(5min 快照序列)
snaps = []  # (t, {sym:(funding_bybit)}, {sym: oi_okx})
with open(f"{REC}/fast_xmeta_snapshots.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except Exception:
            continue
        t = ts(d["recorded_at"])
        fb, oi = {}, {}
        for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
            if isinstance(v, list) and v:
                fb[sym] = float(v[0])
        for sym, v in (d.get("venues", {}).get("okx") or {}).items():
            if isinstance(v, list) and len(v) >= 2 and v[1] > 0:
                oi[sym] = float(v[1])
        snaps.append((t, fb, oi))
snaps.sort(key=lambda x: x[0])
snap_ts = [x[0] for x in snaps]

def xmeta_at(sym, t0):
    """返回 (bybit_funding, oi_chg_pct_5m):走前,取 ≤t0 最近两张快照。"""
    i = bisect.bisect_right(snap_ts, t0) - 1
    if i < 0 or t0 - snap_ts[i] > 900:
        return None, None
    fb = snaps[i][1].get(sym)
    oi_now = snaps[i][2].get(sym)
    oi_chg = None
    if oi_now and i >= 1:
        prev = snaps[i - 1][2].get(sym)
        if prev and prev > 0:
            oi_chg = (oi_now - prev) / prev * 100
    return fb, oi_chg

# ============ 快照(重字段:hot_micro 点差 / hot_mids 锚价差,一遍针对性扫)============
need = {}
for e in prizes + controls:
    need.setdefault(e["sym"], []).append(e["t0"])
for v in need.values():
    v.sort()

spread_at = {}
for fn in ("fast_hot_micro.jsonl.1", "fast_hot_micro.jsonl"):
    try:
        f = open(f"{REC}/{fn}")
    except FileNotFoundError:
        continue
    with f:
        for line in f:
            s1 = line.find('"symbol":"')
            p = line.find('"recorded_at":"')
            if p < 0 or s1 < 0:
                continue
            sym = line[s1 + 10:line.find('"', s1 + 10)]
            t0s = need.get(sym)
            if not t0s:
                continue
            try:
                t = ts(line[p + 15:p + 34])
            except Exception:
                continue
            j = bisect.bisect_left(t0s, t)
            if j >= len(t0s) or t0s[j] - t > 120 or t > t0s[j]:
                continue
            try:
                r = json.loads(line)
            except Exception:
                continue
            key = (sym, t0s[j])
            spread_at[key] = (r.get("spread_bps"), r.get("book_imbalance_now"))
print(f"hot_micro 快照命中 {len(spread_at)}/{len(prizes)+len(controls)}")

gap_at = {}
with open(f"{REC}/fast_hot_mids.jsonl") as f:
    for line in f:
        if '"anchors"' not in line:
            continue
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        mids = d.get("mids") or {}
        by = (d.get("anchors") or {}).get("bybit") or {}
        for sym, anchor in by.items():
            t0s = need.get(sym)
            if not t0s:
                continue
            j = bisect.bisect_left(t0s, t)
            if j >= len(t0s) or t0s[j] - t > 120 or t > t0s[j]:
                continue
            m = mids.get(sym)
            if m and anchor[0] > 0:
                gap_at[(sym, t0s[j])] = (m - anchor[0]) / anchor[0] * 1e4
print(f"hot_mids 锚命中 {len(gap_at)}")

def snapshot(e):
    sym, t0 = e["sym"], e["t0"]
    fb, oi_chg = xmeta_at(sym, t0)
    sp = spread_at.get((sym, t0), (None, None))
    return dict(
        fneg=fb, oi_chg=oi_chg,
        liq_long=count_sided(liqs, sym, t0 - 900, t0, "long"),
        liq_short=count_sided(liqs, sym, t0 - 900, t0, "short"),
        burst_buy=count_sided(bursts, sym, t0 - 900, t0, "buy"),
        burst_sell=count_sided(bursts, sym, t0 - 900, t0, "sell"),
        riots15=count_sided(riots, sym, t0 - 900, t0),
        prior24=count_sided(riots, sym, t0 - 86400, t0 - 900),
        spread=sp[0], bimb=sp[1],
        gap=gap_at.get((sym, t0)),
        hour=datetime.fromtimestamp(t0, tz=timezone.utc).hour,
    )

for e in prizes + controls:
    e["snap"] = snapshot(e)

# ============ Stage A:分布拉开检验(train/holdout 双侧)============
FEATS = ["fneg", "oi_chg", "liq_long", "liq_short", "burst_buy", "burst_sell",
         "riots15", "prior24", "spread", "bimb", "gap"]

def dist(vals):
    v = sorted(x for x in vals if x is not None)
    if len(v) < 10:
        return None
    return (v[len(v) // 4], v[len(v) // 2], v[3 * len(v) // 4], len(v))

def auc(pos, neg):
    """秩和 AUC(拉开度;0.5=没差别)。"""
    pos = [x for x in pos if x is not None]
    neg = [x for x in neg if x is not None]
    if len(pos) < 10 or len(neg) < 10:
        return None
    allv = sorted((v, 1) for v in pos) + sorted((v, 0) for v in neg)
    allv.sort(key=lambda x: x[0])
    rank_sum = 0.0
    i = 0
    while i < len(allv):
        j = i
        while j < len(allv) and allv[j][0] == allv[i][0]:
            j += 1
        r = (i + j + 1) / 2.0
        for k in range(i, j):
            if allv[k][1] == 1:
                rank_sum += r
        i = j
    n1, n0 = len(pos), len(neg)
    return (rank_sum - n1 * (n1 + 1) / 2) / (n1 * n0)

print("\n== Stage A:奖品 vs 对照 分布拉开度(AUC;两侧都偏离 0.5 同向=入场券候选)==")
print(f"{'特征':10s} {'trainAUC':>9s} {'holdAUC':>9s}  奖品中位(tr)  对照中位(tr)")
results = []
for ft in FEATS:
    row = {"feat": ft}
    for tag, dset in (("tr", TRAIN), ("ho", MINE - TRAIN)):
        pv = [e["snap"][ft] for e in prizes if e["day"] in dset]
        cv = [e["snap"][ft] for e in controls if e["day"] in dset]
        row[tag] = auc(pv, cv)
        if tag == "tr":
            dp, dc = dist(pv), dist(cv)
            row["pm"] = dp[1] if dp else None
            row["cm"] = dc[1] if dc else None
    results.append(row)
    tr = f"{row['tr']:.3f}" if row["tr"] is not None else "  -  "
    ho = f"{row['ho']:.3f}" if row["ho"] is not None else "  -  "
    pm = f"{row['pm']:.4g}" if row["pm"] is not None else "-"
    cm = f"{row['cm']:.4g}" if row["cm"] is not None else "-"
    print(f"{ft:10s} {tr:>9s} {ho:>9s}  {pm:>12s} {cm:>12s}")

# ============ Stage B:幸存特征 → 方向由 train 分桶说话 → EV 清单 ============
print("\n== Stage B:天气钥匙读法(幸存特征的奖品方向分布;train 定向 holdout 验)==")
for row in results:
    if row["tr"] is None or row["ho"] is None:
        continue
    if not ((row["tr"] > 0.58 and row["ho"] > 0.58) or (row["tr"] < 0.42 and row["ho"] < 0.42)):
        continue
    ft = row["feat"]
    hi = row["tr"] > 0.5  # 奖品组偏高
    pv = sorted(x for x in (e["snap"][ft] for e in prizes + controls) if x is not None)
    thr = pv[int(len(pv) * (0.75 if hi else 0.25))]
    for tag, dset in (("train", TRAIN), ("holdout", MINE - TRAIN)):
        sel = [e for e in prizes if e["day"] in dset and e["snap"][ft] is not None and
               ((e["snap"][ft] >= thr) if hi else (e["snap"][ft] <= thr))]
        if len(sel) < 10:
            print(f"  {ft} [{tag}]: n={len(sel)} 不足")
            continue
        up = sum(1 for e in sel if e["ret"] > 0)
        med = statistics.median(abs(e["ret"]) for e in sel)
        print(f"  {ft}{'≥' if hi else '≤'}{thr:.4g} [{tag}]: 奖品n={len(sel)} 向上{up/len(sel)*100:.0f}% 肉中位{med:.0f}bps")

# ============ Stage C:方向对比(流的不对称 → 奖品方向;train 读数 holdout 验证)============
print("\n== Stage C:方向钥匙(非价格流不对称 vs 奖品方向;50%=硬币)==")
liq_all_ts = sorted(t for arr in liqs.values() for t, _ in arr)

def fuel_at(t):
    return bisect.bisect_right(liq_all_ts, t) - bisect.bisect_left(liq_all_ts, t - 300)

def dir_test(name, cond_up, cond_dn):
    for tag, dset in (("train", TRAIN), ("holdout", MINE - TRAIN)):
        pu = [e for e in prizes if e["day"] in dset and cond_up(e["snap"])]
        pd = [e for e in prizes if e["day"] in dset and cond_dn(e["snap"])]
        su = (sum(1 for e in pu if e["ret"] > 0), len(pu))
        sd = (sum(1 for e in pd if e["ret"] > 0), len(pd))
        line = f"  {name} [{tag:7s}]:"
        line += f" 看涨侧 n={su[1]:4d} 实向上{su[0]/max(su[1],1)*100:3.0f}% |"
        line += f" 看跌侧 n={sd[1]:4d} 实向上{sd[0]/max(sd[1],1)*100:3.0f}%"
        print(line)

dir_test("burst差(buy>sell→? / sell>buy→?)",
         lambda s: s["burst_buy"] > s["burst_sell"],
         lambda s: s["burst_sell"] > s["burst_buy"])
dir_test("爆仓差(short爆多→? / long爆多→?)",
         lambda s: s["liq_short"] > s["liq_long"],
         lambda s: s["liq_long"] > s["liq_short"])
dir_test("锚价差(折价<-20→? / 溢价>+20→?)",
         lambda s: s["gap"] is not None and s["gap"] < -20,
         lambda s: s["gap"] is not None and s["gap"] > 20)
dir_test("资费(f≤-5bp→? / f≥+5bp→?)",
         lambda s: s["fneg"] is not None and s["fneg"] <= -0.0005,
         lambda s: s["fneg"] is not None and s["fneg"] >= 0.0005)
dir_test("OI(5m降≤-0.5%→? / 升≥0.5%→?)",
         lambda s: s["oi_chg"] is not None and s["oi_chg"] <= -0.5,
         lambda s: s["oi_chg"] is not None and s["oi_chg"] >= 0.5)

# ============ Stage D:天气分层(纪律 14i)+ 注意力钥匙容量 ============
print("\n== Stage D:天气分层(奖品密度;风暴 fuel≥40 / 平静 <40)==")
for tag, dset in (("train", TRAIN), ("holdout", MINE - TRAIN)):
    st = [e for e in prizes if e["day"] in dset and fuel_at(e["t0"]) >= 40]
    ca = [e for e in prizes if e["day"] in dset and fuel_at(e["t0"]) < 40]
    for nm, sub in (("风暴", st), ("平静", ca)):
        if sub:
            up = sum(1 for e in sub if e["ret"] > 0)
            print(f"  [{tag}] {nm}: 奖品n={len(sub):4d} 向上{up/len(sub)*100:.0f}% 肉中位{statistics.median(abs(e['ret']) for e in sub):.0f}bps")

print("\n== 注意力钥匙容量(prior24≥134;每日聚焦名单大小=供给)==")
for d in sorted(MINE):
    syms = set(e["sym"] for e in prizes + controls if e["day"] == d and e["snap"]["prior24"] >= 134)
    psyms = set(e["sym"] for e in prizes if e["day"] == d and e["snap"]["prior24"] >= 134)
    allp = set(e["sym"] for e in prizes if e["day"] == d)
    print(f"  {d}: 钥匙名单币数={len(syms)} 其中出奖品币={len(psyms)} 当日全部奖品币={len(allp)}")
