#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「战役跟多」研究(2026-07-09;用户"打不过就吃掉对手/对手当粒子"指令 + EDGE 首枪尸检产出;只读)。

对手模型(第2层玩家):同币短窗内买方 burst 连发 = 有组织的进攻流(庄家/bot 把价格推进
空头爆仓区,轧空点火)。它不是散户单波 FOMO(会竭尽),是战役(有计划,直到空头爆仓打印)。
我们打不过它的速度,但它的战役签名(burst 连发)公开印在磁带上——站到它那边,
跟多直到 L0(空头强平)打印或 trail 耗尽。

镜像证据(_fw_burst_campaign_study):战役桶做空毛中位 -42bps → 跟多毛 +42,train/holdout 同号。
本脚本做正经的多头判决:真实多头出场,market_mids 3s 路径,taker 成本 12bps。

信息集:进场=第3次买方burst 到达时刻(走前;识别延迟38ms 已在数据里);
价格=下一个 ≥t 的 3s 网格点(保守近似,不含滑点——如实标注)。
纪律 14b:最后一天=holdout;14d:12h 分片同号检验。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
CAMPAIGN_K = 3        # 第 K 次买方 burst = 战役确认
CAMPAIGN_WIN = 900.0  # 15min
COOLDOWN = 1800.0

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

# ---- 1) 买方 burst 流(热币探测器 07-05 起 + 全市场探针 07-08 起,合并去重 1s)----
bursts = {}
for fn in ("fast_burst_detect.jsonl", "fast_full_burst_probe.jsonl"):
    with open(f"{REC}/{fn}") as f:
        for line in f:
            try:
                r = json.loads(line)
            except Exception:
                continue
            if r.get("side") != "buy":
                continue
            bursts.setdefault(r["symbol"], []).append(ts(r["recorded_at"]))
for s in bursts:
    arr = sorted(bursts[s])
    ded = []
    for t in arr:
        if not ded or t - ded[-1] > 1.0:
            ded.append(t)
    bursts[s] = ded

# ---- 2) 战役进场点:15min 窗内第 K 次买方 burst;同币 1800s 冷却 ----
entries = []
for sym, arr in bursts.items():
    last = 0.0
    for i in range(len(arr)):
        if arr[i] - last < COOLDOWN:
            continue
        lo = bisect.bisect_left(arr, arr[i] - CAMPAIGN_WIN)
        if i - lo + 1 >= CAMPAIGN_K:
            entries.append((sym, arr[i]))
            last = arr[i]
entries.sort(key=lambda e: e[1])
print(f"战役进场点={len(entries)}(K={CAMPAIGN_K}/{int(CAMPAIGN_WIN)}s,冷却{int(COOLDOWN)}s,币数={len(set(s for s,_ in entries))})")

# ---- 3) 空头强平打印时刻(L0 出场钟)----
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

# ---- 4) 一遍扫 market_mids 抽取所需路径 ----
need = {}  # sym -> [(t0, t0+930)]
for sym, t0 in entries:
    need.setdefault(sym, []).append((t0, t0 + 930))
win_lo = min(t for _, t in entries) - 5
win_hi = max(t for _, t in entries) + 940
paths = {(s, t0): [] for s, t0 in entries}
idx = {}
for sym, t0 in entries:
    idx.setdefault(sym, []).append(t0)
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0 or '"mids"' not in line[:40] and '"mids"' not in line:
            continue
        stamp = line[p + 15:p + 34]
        try:
            t = ts(stamp)
        except Exception:
            continue
        if t < win_lo or t > win_hi:
            continue
        row = None
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t) - 1
            if j >= 0 and t0s[j] - 5 <= t <= t0s[j] + 930:
                if row is None:
                    try:
                        row = (json.loads(line).get("mids") or {})
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

# ---- 5) 多头出场模拟(3s 路径;毛→净扣 12)----
def simulate(path, t0, liq_arr):
    """返回 dict(exit→net_bps);path 已按时间排序。"""
    pts = [(t, m) for t, m in path if t >= t0]
    if len(pts) < 3:
        return None
    e = pts[0][1]
    liq_t = None
    if liq_arr is not None:
        j = bisect.bisect_right(liq_arr, t0)
        if j < len(liq_arr) and liq_arr[j] <= t0 + 900:
            liq_t = liq_arr[j]
    out = {}
    # (a) 固定 300s / (b) 600s
    for cap, tag in ((300, "fix300"), (600, "fix600")):
        px = None
        for t, m in pts:
            if t - t0 >= cap:
                break
            px = m
        for t, m in pts:  # 灾难 -40 任何时刻
            if t - t0 >= cap:
                break
            if (m - e) / e * 1e4 <= -40:
                px = m
                break
        if px:
            out[tag] = (px - e) / e * 1e4 - COST
    # (c) trail:激活15 回吐25 灾难40 cap600
    peak, px, done = 0.0, None, False
    for t, m in pts:
        u = (m - e) / e * 1e4
        if t - t0 >= 600:
            px = m
            break
        if u <= -40:
            px, done = m, True
            break
        peak = max(peak, u)
        if peak >= 15 and u <= peak - 25:
            px, done = m, True
            break
        px = m
    if px:
        out["trail"] = (px - e) / e * 1e4 - COST
    # (d) L0 出场:首笔空头强平打印后 3s 离场(轧空目的达成=离场),否则 600s 兜底,灾难40
    px = None
    for t, m in pts:
        u = (m - e) / e * 1e4
        if u <= -40 or t - t0 >= 600 or (liq_t and t >= liq_t + 3):
            px = m
            break
        px = m
    if px:
        out["liq_exit"] = (px - e) / e * 1e4 - COST
    out["_liq_hit"] = liq_t is not None
    return out

rows = []
for (sym, t0), path in paths.items():
    path.sort()
    r = simulate(path, t0, sliq.get(sym))
    if r:
        r["sym"], r["t0"] = sym, t0
        rows.append(r)
rows.sort(key=lambda r: r["t0"])
print(f"路径可评={len(rows)}(标尺覆盖率 {len(rows)}/{len(entries)})")

def report(sub, tag):
    print(f"-- {tag} (n={len(sub)}) --")
    for ex in ("fix300", "fix600", "trail", "liq_exit"):
        nets = [r[ex] for r in sub if ex in r]
        if not nets:
            continue
        gp = sum(x for x in nets if x > 0)
        gl = -sum(x for x in nets if x < 0)
        top = {}
        for r in sub:
            if ex in r:
                top[r["sym"]] = top.get(r["sym"], 0) + 1
        tsym, tshr = max(top.items(), key=lambda kv: kv[1]) if top else ("", 0)
        print(f"  {ex:8s}: n={len(nets):3d} 中位{statistics.median(nets):+7.1f} 均{sum(nets)/len(nets):+7.1f} "
              f"合计{sum(nets):+8.0f} PF{(gp/gl if gl>0 else 99):.2f} 胜率{sum(1 for x in nets if x>0)/len(nets)*100:.0f}% "
              f"顶币{tsym[:-5]}({tshr/len(nets)*100:.0f}%)")
    hit = sum(1 for r in sub if r.get("_liq_hit"))
    print(f"  进场后 15min 内空头强平打印率: {hit}/{len(sub)} ({hit/max(len(sub),1)*100:.0f}%)")

last_day = datetime.fromtimestamp(rows[-1]["t0"], tz=timezone.utc).date()
train = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() < last_day]
hold = [r for r in rows if datetime.fromtimestamp(r["t0"], tz=timezone.utc).date() == last_day]
report(train, f"train(< {last_day})")
report(hold, f"holdout({last_day})")

print("\n== 12h 分片同号(trail 口径,中位>0 记 ✓)==")
t_min = rows[0]["t0"]
i = 0
while t_min + i * 43200 <= rows[-1]["t0"]:
    sl = [r["trail"] for r in rows if t_min + i * 43200 <= r["t0"] < t_min + (i + 1) * 43200 and "trail" in r]
    if len(sl) >= 5:
        print(f"  片{i}: n={len(sl)} 中位{statistics.median(sl):+7.1f} {'✓' if statistics.median(sl)>0 else '✗'}")
    else:
        print(f"  片{i}: n={len(sl)} 样本不足")
    i += 1
