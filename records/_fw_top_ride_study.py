#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「烟花顶跟多」直测(2026-07-09;战役跟多 v1 判死后的精确镜像;只读)。

假设:烟花细胞时刻(拉升≥60 且减速)且有战役签名(t0 前 15min 买方 burst≥3)的币,
不是"力竭顶"而是"战役中场"——做空死(账本实证中位 -53),跟多吃战役后半场。
对手模型:第2层庄家把价格推进空头爆仓区,战役不因"减速"结束,因空头强平打印而结束。

进场=账本烟花行 t0(与死掉的短刀完全同点位=信息集天然对齐);多头;market_mids 3s 路径;
出场=fix300 / trail(激活15回吐25灾难40 cap600) / liq_exit(首笔空头强平+3s,600s 兜底) / fix600;
成本 12bps taker;对照组=同点位无战役签名(burst<3)。
纪律 14b holdout=最后一天;14d 12h 分片;单币主导检查。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
K = 3

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

# 买方 burst 索引(双源合并,1s 去重)
bursts = {}
for fn in ("fast_burst_detect.jsonl", "fast_full_burst_probe.jsonl"):
    with open(f"{REC}/{fn}") as f:
        for line in f:
            try:
                r = json.loads(line)
            except Exception:
                continue
            if r.get("side") == "buy":
                bursts.setdefault(r["symbol"], []).append(ts(r["recorded_at"]))
for s in bursts:
    arr = sorted(bursts[s])
    ded = []
    for t in arr:
        if not ded or t - ded[-1] > 1.0:
            ded.append(t)
    bursts[s] = ded

def nburst(sym, t0):
    arr = bursts.get(sym, [])
    return bisect.bisect_left(arr, t0 - 0.001) - bisect.bisect_left(arr, t0 - 900)

# 账本烟花行=进场点(与短刀同点位)
entries = []
with open(f"{REC}/particle_ledger.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("module") != "firework" or r.get("src") != "rest" or r.get("exit") == "unfilled":
            continue
        t0 = ts(r["t0"])
        entries.append(dict(sym=r["symbol"], t0=t0, nb=nburst(r["symbol"], t0),
                            short_net=r["net_bps"], fuel=r.get("fuel_5m", 0)))
entries.sort(key=lambda e: e["t0"])
print(f"烟花细胞点={len(entries)} 战役签名(≥{K})={sum(1 for e in entries if e['nb']>=K)}")

# 空头强平钟
sliq = {}
with open(f"{REC}/fast_liquidation_events.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("pos_side") == "short":
            sliq.setdefault(r["symbol"], []).append(ts(r["recorded_at"]))
for s in sliq:
    sliq[s].sort()

# 一遍扫标尺抽路径
idx = {}
for e in entries:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in entries}
lo_t = entries[0]["t0"] - 5
hi_t = entries[-1]["t0"] + 940
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        if t < lo_t or t > hi_t:
            continue
        row = None
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t) - 1
            if j >= 0 and t0s[j] - 5 <= t <= t0s[j] + 930:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

def simulate(e):
    path = sorted(paths[(e["sym"], e["t0"])])
    pts = [(t, m) for t, m in path if t >= e["t0"]]
    if len(pts) < 3:
        return None
    ent = pts[0][1]
    liq_arr = sliq.get(e["sym"])
    liq_t = None
    if liq_arr:
        j = bisect.bisect_right(liq_arr, e["t0"])
        if j < len(liq_arr) and liq_arr[j] <= e["t0"] + 900:
            liq_t = liq_arr[j]
    out = dict(e)
    out["_liq"] = liq_t is not None
    for cap, tag in ((300, "fix300"), (600, "fix600")):
        px = None
        for t, m in pts:
            u = (m - ent) / ent * 1e4
            if t - e["t0"] >= cap or u <= -40:
                px = m
                break
            px = m
        if px:
            out[tag] = (px - ent) / ent * 1e4 - COST
    peak, px = 0.0, None
    for t, m in pts:
        u = (m - ent) / ent * 1e4
        if t - e["t0"] >= 600 or u <= -40 or (peak >= 15 and u <= peak - 25):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px:
        out["trail"] = (px - ent) / ent * 1e4 - COST
    px = None
    for t, m in pts:
        u = (m - ent) / ent * 1e4
        if t - e["t0"] >= 600 or u <= -40 or (liq_t and t >= liq_t + 3):
            px = m
            break
        px = m
    if px:
        out["liq_exit"] = (px - ent) / ent * 1e4 - COST
    return out

rows = [x for x in (simulate(e) for e in entries) if x]
print(f"路径可评={len(rows)}")

def rep(sub, tag):
    print(f"-- {tag} (n={len(sub)}) --")
    if not sub:
        return
    for ex in ("fix300", "fix600", "trail", "liq_exit"):
        nets = [r[ex] for r in sub if ex in r]
        if not nets:
            continue
        gp = sum(x for x in nets if x > 0)
        gl = -sum(x for x in nets if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {ex:8s}: n={len(nets):3d} 中位{statistics.median(nets):+7.1f} 均{sum(nets)/len(nets):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜率{sum(1 for x in nets if x>0)/len(nets)*100:.0f}% 顶币{tsym[:-5]}({tn/len(sub)*100:.0f}%)")
    print(f"  空头强平15min打印率 {sum(1 for r in sub if r['_liq'])/len(sub)*100:.0f}%  同点位短刀净中位 {statistics.median([r['short_net'] for r in sub]):+.1f}(互证)")

last_day = datetime.fromtimestamp(rows[-1]["t0"], tz=timezone.utc).date()
for cond, name in ((lambda r: r["nb"] >= K, f"战役签名≥{K}"), (lambda r: r["nb"] < K, f"无签名<{K}(对照)")):
    sub = [r for r in rows if cond(r)]
    tr = [r for r in sub if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() < last_day]
    ho = [r for r in sub if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() == last_day]
    rep(tr, f"{name} train")
    rep(ho, f"{name} holdout({last_day})")

print("\n== 12h 分片(战役签名桶,trail;中位>0 ✓)==")
camp = [r for r in rows if r["nb"] >= K]
t0min = camp[0]["t0"]
i = 0
while t0min + i * 43200 <= camp[-1]["t0"]:
    sl = [r["trail"] for r in camp if t0min + i * 43200 <= r["t0"] < t0min + (i + 1) * 43200 and "trail" in r]
    if len(sl) >= 5:
        print(f"  片{i}: n={len(sl)} 中位{statistics.median(sl):+7.1f} {'✓' if statistics.median(sl)>0 else '✗'}")
    i += 1
