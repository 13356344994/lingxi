#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""假设㋓「burst 连发否决」研究(2026-07-09,EDGE 首枪尸检产出;只读)。

对手模型:入场前同币买方 burst 连发 = 有组织的进攻流(把价格推进空头爆仓区的轧空点火),
不是散户单波 FOMO——单波会竭尽,战役不会。反手做空 = 站在庄家对面。
EDGE 实证:进场前 7.5min 十次买方 burst(x16~x452),进场后 4min 空头爆仓瀑布,+250bps 轧空。

判决口径(纪律 10/14b/14d):
  样本 = Go 粒子账本 module=firework src=rest 行(rest 可执行口径);
  特征 = t0 前 15min 同币买方 burst 计数(fast_full_burst_probe=全市场,走前无前视);
  分割 = train(除最后一天) / holdout(最后一天) + 12h K 折片同号检验;
  本脚本只提名,不判决、不加闸。
"""
import json, statistics, sys, bisect
from datetime import datetime, timedelta, timezone

DIR = "/root/lingxi/records/okx_directional"
WINDOW_SEC = 900  # 15min

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)

# ---- burst 流(全市场探针为主,热币探测器为辅,两源分别记)----
def load_bursts(fn):
    out = {}
    try:
        with open(f"{DIR}/{fn}") as f:
            for line in f:
                try:
                    r = json.loads(line)
                except Exception:
                    continue
                if r.get("side") != "buy":
                    continue
                out.setdefault(r["symbol"], []).append(ts(r["recorded_at"]).timestamp())
    except FileNotFoundError:
        pass
    for v in out.values():
        v.sort()
    return out

full_bursts = load_bursts("fast_full_burst_probe.jsonl")
hot_bursts = load_bursts("fast_burst_detect.jsonl")

def burst_count(idx, sym, t0, window=WINDOW_SEC):
    arr = idx.get(sym)
    if not arr:
        return 0
    lo = bisect.bisect_left(arr, t0 - window)
    hi = bisect.bisect_left(arr, t0 - 0.001)  # 严格 t0 之前(走前)
    return hi - lo

# ---- 账本烟花行 ----
rows = []
with open(f"{DIR}/particle_ledger.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("module") != "firework" or r.get("src") != "rest":
            continue
        if r.get("exit") == "unfilled":
            continue
        t0 = ts(r["t0"]).timestamp()
        r["_t0"] = t0
        r["_nburst_full"] = burst_count(full_bursts, r["symbol"], t0)
        r["_nburst_hot"] = burst_count(hot_bursts, r["symbol"], t0)
        rows.append(r)
rows.sort(key=lambda r: r["_t0"])
if not rows:
    sys.exit("无样本")

def stats(sub):
    nets = [r["net_bps"] for r in sub]
    if not nets:
        return None
    gp = sum(x for x in nets if x > 0)
    gl = -sum(x for x in nets if x < 0)
    dis = sum(1 for r in sub if r.get("exit") == "disaster")
    return dict(n=len(nets), med=statistics.median(nets), tot=sum(nets),
                pf=(gp / gl if gl > 0 else 99), dis_rate=dis / len(nets))

def fmt(s):
    if s is None:
        return "n=0"
    return f"n={s['n']:3d} 中位{s['med']:+7.1f} 合计{s['tot']:+8.0f} PF{s['pf']:.2f} 灾难率{s['dis_rate']*100:.0f}%"

last_day = datetime.fromtimestamp(rows[-1]["_t0"], tz=timezone.utc).date()
train = [r for r in rows if datetime.fromtimestamp(r["_t0"], tz=timezone.utc).date() < last_day]
hold = [r for r in rows if datetime.fromtimestamp(r["_t0"], tz=timezone.utc).date() == last_day]
print(f"样本: 全部 {len(rows)}(train={len(train)} / holdout[{last_day}]={len(hold)});burst源覆盖: "
      f"full={sum(1 for r in rows if r['_nburst_full']>0)} hot={sum(1 for r in rows if r['_nburst_hot']>0)}")

print("\n== ① 特征分布拷问(train,先看单调性再谈切点) ==")
for src in ("_nburst_full", "_nburst_hot"):
    print(f"-- {src} (t0前15min买方burst数) --")
    for lo, hi, tag in [(0, 1, " 0  "), (1, 3, "1-2 "), (3, 6, "3-5 "), (6, 999, "≥6  ")]:
        sub = [r for r in train if lo <= r[src] < hi]
        print(f"  {tag}: {fmt(stats(sub))}")

print("\n== ② 切点扫描(train;否决=计数≥k;看'被否决桶'毒性 与 '存活桶'净化度) ==")
for k in (2, 3, 4, 6):
    veto = [r for r in train if r["_nburst_full"] >= k]
    keep = [r for r in train if r["_nburst_full"] < k]
    print(f"  k={k}: 否决 {fmt(stats(veto))}  |  存活 {fmt(stats(keep))}")

# 先验冻结:k=3(EDGE 案例=10,单波 FOMO 通常 1-2 发;与惯犯粒子同款切点数字)
K = 3
print(f"\n== ③ holdout 当场校验(k={K} 冻结,{last_day}) ==")
veto = [r for r in hold if r["_nburst_full"] >= K]
keep = [r for r in hold if r["_nburst_full"] < K]
print(f"  否决: {fmt(stats(veto))}")
print(f"  存活: {fmt(stats(keep))}")

print(f"\n== ④ 12h K 折片同号检验(全样本,k={K};片内 否决桶中位<存活桶中位 记同号) ==")
t_min, t_max = rows[0]["_t0"], rows[-1]["_t0"]
n_slice = max(2, int((t_max - t_min) // 43200) + 1)
ok = valid = 0
for i in range(n_slice):
    lo, hi = t_min + i * 43200, t_min + (i + 1) * 43200
    sl = [r for r in rows if lo <= r["_t0"] < hi]
    a = [r["net_bps"] for r in sl if r["_nburst_full"] >= K]
    b = [r["net_bps"] for r in sl if r["_nburst_full"] < K]
    if len(a) < 3 or len(b) < 3:
        print(f"  片{i}: 样本不足(否决{len(a)}/存活{len(b)})")
        continue
    valid += 1
    same = statistics.median(a) < statistics.median(b)
    ok += same
    print(f"  片{i}: 否决中位{statistics.median(a):+7.1f}(n={len(a)}) vs 存活{statistics.median(b):+7.1f}(n={len(b)}) {'✓' if same else '✗'}")
print(f"  同号 {ok}/{valid}")

print(f"\n== ⑤ 叠加 fw_pure 三特征(07-09 04:30Z 后有字段的行;k={K}) ==")
pure = [r for r in rows if r.get("fw_ret60_bps", 0) > 0 and r.get("fw_ret15_bps", 0) < 0.05 * r["fw_ret60_bps"]
        and r.get("fw_sym_liq_5m", 9) == 0 and r.get("prior_fw_24h", 99) < 3]
print(f"  纯FOMO顶全体: {fmt(stats(pure))}")
print(f"  ×burst否决后: {fmt(stats([r for r in pure if r['_nburst_full'] < K]))}")
print(f"  被否决的:     {fmt(stats([r for r in pure if r['_nburst_full'] >= K]))}")

print(f"\n== ⑥ EDGE 首枪反事实(k={K}) ==")
for r in rows:
    if r["symbol"] == "EDGE-USDT" and abs(r["_t0"] - ts("2026-07-09T07:27:46").timestamp()) < 60:
        print(f"  EDGE t0前15min: full={r['_nburst_full']} hot={r['_nburst_hot']} → k={K} 下{'会被否决 ✓' if r['_nburst_full'] >= K else '不会被否决 ✗'}")
