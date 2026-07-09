#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""「假阳性税定理」重审(2026-07-09;粒子方案㋀仪器修正矿头号项;只读)。

被审判决(07-04 赢家解剖·定理2):"交易每一个动量确认(ret15≥15/25/40 全档,trail 出场,
扣费):净中位一律 -10bps、PF 0.38-0.59"——公开动量无散户可达 edge。
嫌疑:该判决用固定 trail(10/20/40 族刻度)在暴动点测量,与杀死烟花的是同一类尺子
(57 号翻案:暴动点噪声单位 70-75bps,固定线躺在噪声里)。

重测口径(运行前冻结,不扫描挑优):
  事件=普查行(|ret60|≥25 暴动),动量确认档 |ret15|≥15/25/40,方向=顺 ret15;
  同币 1800s episode 去重(纪律 12);成本 12 taker;3s mid 路径(纪律 22:trail 峰值与
  灾难穿刺均低估,方向保守)。
  旧尺=trail 激活10/回吐20/灾难40/cap600;
  能量尺=unit=clip(|ret60|,40,200),灾难1×/激活0.8×/回吐0.4×/cap600(57 同款,零新参数)。
圣日协议(纪律 14g):分析集=07-06/07(train)+07-09(holdout);圣日 07-05+07-08 只在
最后出一个终验数字,本假设消费圣日【一次】,登记 HANDOFF。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
DEDUP = 1800.0
VAULT = {"2026-07-05", "2026-07-08"}
ANALYSIS = {"2026-07-06", "2026-07-07", "2026-07-09"}
HOLDOUT = "2026-07-09"

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# ---- 事件:普查行 → 动量确认(顺 ret15),同币 1800s 去重 ----
entries = []
last = {}
with open(f"{REC}/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        r15 = r.get("ret_15s_bps", 0.0)
        r60 = r.get("ret_60s_bps", 0.0)
        if abs(r15) < 15:
            continue
        t0 = ts(r["recorded_at"])
        if t0 < ts("2026-07-05T07:10:00"):
            continue  # market_mids 覆盖起点
        sym = r["symbol"]
        if sym in last and t0 - last[sym] < DEDUP:
            continue
        last[sym] = t0
        entries.append(dict(sym=sym, t0=t0, r15=r15, r60=r60, day=day_of(t0)))
entries.sort(key=lambda e: e["t0"])
print(f"动量确认事件(|ret15|≥15,1800s去重)={len(entries)} "
      f"分析集={sum(1 for e in entries if e['day'] in ANALYSIS)} 圣日={sum(1 for e in entries if e['day'] in VAULT)}")

# ---- 路径抽取(一遍扫标尺)----
idx = {}
for e in entries:
    idx.setdefault(e["sym"], []).append(e["t0"])
paths = {(e["sym"], e["t0"]): [] for e in entries}
with open(f"{REC}/fast_market_mids.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p < 0:
            continue
        try:
            t = ts(line[p + 15:p + 34])
        except Exception:
            continue
        row = None
        for sym, t0s in idx.items():
            j = bisect.bisect_right(t0s, t) - 1
            if j >= 0 and t0s[j] <= t <= t0s[j] + 630:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, t0s[j])].append((t, m))

def run_exit(pts, ent, sgn, act, give, dis, cap=600.0):
    peak, px = 0.0, None
    for t, m in pts:
        u = sgn * (m - ent) / ent * 1e4
        if t >= cap or u <= -dis or (peak >= act and u <= peak - give):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px is None:
        return None
    return sgn * (px - ent) / ent * 1e4 - COST

rows = []
for e in entries:
    path = sorted(set(paths[(e["sym"], e["t0"])]))
    pts = [(t - e["t0"], m) for t, m in path if t >= e["t0"]]
    if len(pts) < 3:
        continue
    ent = pts[0][1]
    sgn = 1 if e["r15"] > 0 else -1  # 顺动量
    unit = min(max(abs(e["r60"]), 40.0), 200.0)
    old = run_exit(pts, ent, sgn, 10, 20, 40)
    es = run_exit(pts, ent, sgn, 0.8 * unit, 0.4 * unit, 1.0 * unit)
    if old is None or es is None:
        continue
    rows.append(dict(e, old=old, es=es))
print(f"路径可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 5:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key, name in (("old", "旧尺"), ("es", "能量")):
        n = [r[key] for r in sub]
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {name}: n={len(n):4d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

for lo in (15, 25, 40):
    print(f"== 档 |ret15|≥{lo} ==")
    sub = [r for r in rows if abs(r["r15"]) >= lo]
    rep([r for r in sub if r["day"] in ANALYSIS and r["day"] != HOLDOUT], "train(06/07) ")
    rep([r for r in sub if r["day"] == HOLDOUT], f"holdout(09)  ")

print("\n== 12h 分片(分析集,≥15 档,能量尺中位)==")
ana = [r for r in rows if r["day"] in ANALYSIS]
t0min = ana[0]["t0"]
i = 0
while t0min + i * 43200 <= ana[-1]["t0"]:
    sl = [r["es"] for r in ana if t0min + i * 43200 <= r["t0"] < t0min + (i + 1) * 43200]
    if len(sl) >= 10:
        print(f"  片{i}: n={len(sl):4d} 中位{statistics.median(sl):+7.1f} {'✓' if statistics.median(sl)>0 else '✗'}")
    i += 1

print("\n== 圣日终验(07-05+07-08;本假设一次性消费,登记 HANDOFF)==")
for lo in (15, 25, 40):
    rep([r for r in rows if abs(r["r15"]) >= lo and r["day"] in VAULT], f"圣日 ≥{lo}")
