#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""钉死方向 v2:资费全账+期内版(2026-07-10;v1 漏扣资费的修正;只读)。

v1 缺陷:跟空=付租侧,钉死币升频 1h 后裸持 4h≈付 4%,v1 只扣了 12bps 票。
v2 三口径(全部扣真实资费/或结构性避开):
  A 裸持 4h/24h,扣持有窗内真实结算资费(付租侧=每期 -|f|)——v1 的诚实版;
  B 期内版:每期结算后 5m 进空,下期结算前 5m 出(瞬时口径零资费),期期重复到段末;
    用 5m K 线(前200币)优先,缺则 1h 近似;每期一笔,段内合计与逐期分布都报;
  C 段级 B 合计(一个段=一个样本,同币段近邻 24h 归并=真独立性)。
train/holdout(最后14天)+单币份额;负钉(跟空)为主(正钉 n≈0)。
"""
import json, os, bisect, statistics
from datetime import datetime, timezone

H = "/root/lingxi/records/history"
COST = 12.0
PIN = 0.0095

def load(d, suffix):
    out = {}
    for fn in sorted(os.listdir(d)):
        if not fn.endswith(".jsonl"):
            continue
        sym = fn[:-6].replace(suffix, "")
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

k1h = load(f"{H}/okx_1h", "-USDT-SWAP")
k5m = load(f"{H}/okx_5m", "-USDT-SWAP")
print(f"1h={len(k1h)} 5m={len(k5m)}")

fund = {}
for fn in os.listdir(f"{H}/okx_funding"):
    if not fn.endswith(".jsonl"):
        continue
    sym = fn[:-6].replace("-USDT-SWAP", "")
    rows = []
    with open(f"{H}/okx_funding/{fn}") as f:
        for line in f:
            try:
                x = json.loads(line)["data"]
                fr = float(x.get("realizedRate") or x.get("fundingRate") or 0)
                ft = int(x.get("fundingTime", 0) or 0) / 1000.0
                if ft:
                    rows.append((ft, fr))
            except Exception:
                continue
    fund[sym] = sorted(set(rows))

def px(sym, t, tol=7200):
    src = k5m.get(sym) or k1h.get(sym)
    if not src:
        return None
    tsa = [x[0] for x in src]
    j = bisect.bisect_right(tsa, t) - 1
    if j < 0 or t - tsa[j] > tol:
        return None
    return src[j][1]

# 段与钉死结算
segs = []
for sym, rows in fund.items():
    if sym not in k1h and sym not in k5m:
        continue
    i = 0
    while i < len(rows):
        if abs(rows[i][1]) >= PIN and rows[i][1] < 0:  # 负钉(正钉 n≈0)
            j = i
            while j < len(rows) and rows[j][1] <= -PIN:
                j += 1
            segs.append(dict(sym=sym, settles=[r[0] for r in rows[i:j]],
                             t_end=rows[j][0] if j < len(rows) else None,
                             frs=[r[1] for r in rows[i:j]]))
            i = j
        else:
            i += 1
# 同币近邻段 24h 归并
segs.sort(key=lambda s: (s["sym"], s["settles"][0]))
merged = []
for s in segs:
    if merged and merged[-1]["sym"] == s["sym"] and s["settles"][0] - merged[-1]["settles"][-1] < 86400:
        merged[-1]["settles"] += s["settles"]
        merged[-1]["frs"] += s["frs"]
        merged[-1]["t_end"] = s["t_end"]
    else:
        merged.append(dict(s))
segs = merged
t_hi = max(s["settles"][-1] for s in segs)
split = t_hi - 14 * 86400
print(f"负钉段(24h归并后)={len(segs)} (tr={sum(1 for s in segs if s['settles'][0]<split)} ho={sum(1 for s in segs if s['settles'][0]>=split)})")

def rep(vals, tag, syms=None):
    v = [x for x in vals if x is not None]
    if len(v) < 6:
        print(f"  {tag}: n={len(v)}(不足)")
        return
    gp = sum(x for x in v if x > 0)
    gl = -sum(x for x in v if x < 0)
    dom = ""
    if syms:
        from collections import Counter
        s, c = Counter([s for s, x in zip(syms, vals) if x is not None]).most_common(1)[0]
        dom = f" 顶{s}({c/len(v)*100:.0f}%)"
    print(f"  {tag}: n={len(v):4d} 中位{statistics.median(v):+7.1f} 均{sum(v)/len(v):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in v if x>0)/len(v)*100:.0f}%{dom}")

# ---- A 裸持扣真实资费 ----
print("\n== A 裸持跟空(扣持有窗内真实资费;v1 的诚实版)==")
for hz, tag in ((14400, "4h"), (86400, "24h")):
    for pt, cond in (("tr", lambda t: t < split), ("ho", lambda t: t >= split)):
        vals, syms = [], []
        for s in segs:
            t0 = s["settles"][0]
            if not cond(t0):
                continue
            p0, p1 = px(s["sym"], t0 + 60), px(s["sym"], t0 + hz)
            if not p0 or not p1:
                vals.append(None); syms.append(s["sym"]); continue
            gross = -(p1 / p0 - 1) * 1e4
            paid = sum(abs(f) for t, f in fund[s["sym"]] if t0 + 60 < t <= t0 + hz) * 1e4
            vals.append(gross - paid - COST)
            syms.append(s["sym"])
        rep(vals, f"A×{tag} [{pt}]", syms)

# ---- B 期内版(逐期;零资费)----
print("\n== B 期内跟空(结算后5m进,下期结算前5m出;零资费;逐期一笔)==")
per = []  # (sym, t_enter, net, part)
for s in segs:
    st = s["settles"]
    for k in range(len(st)):
        t_in = st[k] + 300
        t_out = (st[k + 1] - 300) if k + 1 < len(st) else (s["t_end"] - 300 if s["t_end"] else None)
        if not t_out or t_out - t_in < 600:
            continue
        p0, p1 = px(s["sym"], t_in, 3600), px(s["sym"], t_out, 3600)
        if not p0 or not p1:
            continue
        per.append(dict(sym=s["sym"], t=t_in, net=-(p1 / p0 - 1) * 1e4 - COST,
                        part="ho" if t_in >= split else "tr"))
for pt in ("tr", "ho"):
    sel = [p for p in per if p["part"] == pt]
    rep([p["net"] for p in sel], f"B 逐期 [{pt}]", [p["sym"] for p in sel])

# ---- C 段级合计 ----
print("\n== C 段级(B 的段内合计;一段=一样本)==")
from collections import defaultdict
by = defaultdict(list)
for p in per:
    by[(p["sym"], p["part"], min(s["settles"][0] for s in segs if s["sym"] == p["sym"] and s["settles"][0] <= p["t"] <= (s["t_end"] or 1e18)))].append(p["net"])
for pt in ("tr", "ho"):
    vals = [sum(v) for (sym, part, _), v in by.items() if part == pt]
    syms = [sym for (sym, part, _), v in by.items() if part == pt]
    rep(vals, f"C 段合计 [{pt}]", syms)
    for (sym, part, t0), v in sorted(by.items(), key=lambda kv: -sum(kv[1])):
        if part == pt:
            print(f"    {sym:10s} {datetime.fromtimestamp(t0,tz=timezone.utc):%m-%d} 期数{len(v)} 合计{sum(v):+8.0f}bps")
