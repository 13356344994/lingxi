#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""烟花点·能量刻度出场重审(2026-07-09;"已死的不一定死"指令下的仪器修正重测;只读)。

假设:烟花全族判决用的 -40 固定灾难线躺在噪声里(细胞定义 r60≥60 ⇒ 噪声步长>40),
双向都被呼吸打穿(实测双向中位都 -53)。用系统已验证的能量单位制重测:
  unit = min(max(40, |r60|), 200)   (瀑布自己报能量,封顶防闪崩,与 fastLiqFollowExitUnit 同哲学)
  灾难 = 1.0×unit                    (v3⑤ 验证 k=2.0×30s池塘能量;r60≈2×30s能量,故 1.0×r60 同刻度)
  trail 激活 = 0.8×unit,回吐 = 0.4×unit(v5 验证 k,原样移植)
  cap 600s;成本 12bps taker。
【参数在运行前登记,不扫描挑优;两方向同规则对称测试。】
纪律 14b holdout=最后一天;12h 分片;单币主导报告。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

entries = []
with open(f"{REC}/particle_ledger.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("module") != "firework" or r.get("src") != "rest" or r.get("exit") == "unfilled":
            continue
        entries.append(dict(sym=r["symbol"], t0=ts(r["t0"]), short_net=r["net_bps"]))
entries.sort(key=lambda e: e["t0"])

idx = {}
for e in entries:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in entries}
lo_t, hi_t = entries[0]["t0"] - 70, entries[-1]["t0"] + 640
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
            j = bisect.bisect_right(t0s, t + 70) - 1
            if j >= 0 and t0s[j] - 70 <= t <= t0s[j] + 630:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

def simulate(e):
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pre = [(t, m) for t, m in path if t < e["t0"]]
    pts = [(t, m) for t, m in path if t >= e["t0"]]
    if len(pts) < 3 or not pre:
        return None
    ent = pts[0][1]
    p60 = None
    for t, m in pre:
        if e["t0"] - t >= 55:
            p60 = m
    if p60 is None:
        p60 = pre[0][1]
        if e["t0"] - pre[0][0] < 30:
            return None  # 没有像样的 r60 历史,弃
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(40.0, abs(r60)), 200.0)
    out = dict(e, unit=unit)
    for side in ("long", "short"):
        sgn = 1 if side == "long" else -1
        peak, px = 0.0, None
        for t, m in pts:
            u = sgn * (m - ent) / ent * 1e4
            if t - e["t0"] >= 600 or u <= -1.0 * unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
                px = m
                break
            peak = max(peak, u)
            px = m
        if px:
            out[side] = sgn * (px - ent) / ent * 1e4 - COST
    return out

rows = [x for x in (simulate(e) for e in entries) if x]
print(f"可评={len(rows)}/{len(entries)} 能量单位中位={statistics.median([r['unit'] for r in rows]):.0f}bps")
last_day = datetime.fromtimestamp(rows[-1]["t0"], tz=timezone.utc).date()

def rep(sub, tag):
    if not sub:
        print(f"-- {tag}: n=0")
        return
    cnt = {}
    for r in sub:
        cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
    tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
    line = f"-- {tag} (n={len(sub)},顶币{tsym[:-5]} {tn/len(sub)*100:.0f}%) "
    for side in ("long", "short"):
        nets = [r[side] for r in sub if side in r]
        gp = sum(x for x in nets if x > 0)
        gl = -sum(x for x in nets if x < 0)
        line += f"| {side}: 中位{statistics.median(nets):+7.1f} 均{sum(nets)/len(nets):+7.1f} PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in nets if x>0)/len(nets)*100:.0f}% "
    print(line)

tr = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() < last_day]
ho = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() == last_day]
rep(tr, "train")
rep(ho, f"holdout({last_day})")
print("对照(坏尺子口径,同点位短刀账本): train 中位 %+.1f / holdout %+.1f" % (
    statistics.median([r["short_net"] for r in tr]), statistics.median([r["short_net"] for r in ho])))

print("\n12h 分片(能量口径;两向中位):")
t0min = rows[0]["t0"]
i = 0
while t0min + i * 43200 <= rows[-1]["t0"]:
    sl = [r for r in rows if t0min + i * 43200 <= r["t0"] < t0min + (i + 1) * 43200]
    if len(sl) >= 5:
        lm = statistics.median([r["long"] for r in sl if "long" in r])
        sm = statistics.median([r["short"] for r in sl if "short" in r])
        print(f"  片{i}: n={len(sl):3d} long {lm:+7.1f} | short {sm:+7.1f}")
    i += 1
