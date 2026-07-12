#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""key 线出分口径整改(2026-07-10 用户令三;研究口径,不动实弹;只读)。

两项整改:
①episode 去重:同币同源近邻结算归并(触发密集期逐笔=伪独立,纪律 12);
②穿停滑点三档敏感性:止损实收 vs 理论 -150 的穿线滑点,按 0/中位/P90 三档重算净值,
  看这把枪对滑点假设有多敏感(死板止损在长尾币会被滑穿=真实成本)。
口径=Go 粒子账本 rest 不可执行... 用真金结算账本 fast_v2_settlements(key_* 源),
  真金=已了结口径,不再回放。分 casc/sqz,报 episode 去重前后 + 三档。
"""
import json, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

rows = []
with open(f"{REC}/fast_v2_settlements.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        src = str(r.get("fast_candidate_source", ""))
        if not src.startswith("key_"):
            continue
        rows.append(dict(src=src, sym=r["symbol"], t=ts(r["time"]),
                         net=r["realized_usdt"], ntl=r["notional"],
                         bps=r["realized_usdt"] / r["notional"] * 1e4,
                         reason=r.get("reason", ""), fee=r.get("fee_usdt", 0),
                         slip=r.get("entry_slip_bps", 0)))
rows.sort(key=lambda r: r["t"])
print(f"key 结算总笔数={len(rows)}")

def episode_dedup(sub, gap=1800):
    """同币近邻 gap 秒内归并为一 episode(取首笔,合并净值 bps)。"""
    by = {}
    for r in sub:
        by.setdefault(r["sym"], []).append(r)
    out = []
    for sym, rs in by.items():
        rs.sort(key=lambda r: r["t"])
        cur = None
        for r in rs:
            if cur and r["t"] - cur["t_last"] < gap:
                cur["bps_list"].append(r["bps"])
                cur["t_last"] = r["t"]
            else:
                if cur:
                    out.append(cur)
                cur = dict(sym=sym, t_last=r["t"], bps_list=[r["bps"]])
        if cur:
            out.append(cur)
    # episode 净值=组内均值(一段=一样本)
    return [statistics.mean(e["bps_list"]) for e in out]

def rep(name, vals):
    if not vals:
        print(f"  {name}: n=0")
        return
    gp = sum(x for x in vals if x > 0)
    gl = -sum(x for x in vals if x < 0)
    print(f"  {name}: n={len(vals):3d} 中位{statistics.median(vals):+7.1f} 均{sum(vals)/len(vals):+7.1f} "
          f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in vals if x>0)/len(vals)*100:.0f}%")

for src in ("key_casc_fade", "key_sqz_fade"):
    sub = [r for r in rows if r["src"] == src]
    if not sub:
        continue
    print(f"\n=== {src} ===")
    print("① episode 去重前后(逐笔 vs 同币30min归并):")
    rep("逐笔      ", [r["bps"] for r in sub])
    rep("episode去重", episode_dedup(sub))
    # ② 穿停滑点三档:止损单的实收 vs 理论 -150,构造三档修正
    stops = [r for r in sub if r["reason"] == "fast_key_stop"]
    if stops:
        pierce = [r["bps"] - (-150 - r["fee"] / r["ntl"] * 1e4) for r in stops]  # 负=穿线损耗
        pierce_sorted = sorted(pierce)
        p0 = 0.0
        pmed = statistics.median(pierce)
        p90 = pierce_sorted[int(len(pierce_sorted) * 0.9)] if len(pierce_sorted) >= 3 else pierce_sorted[-1]
        print(f"② 穿停滑点(止损 n={len(stops)}):实测中位{pmed:+.1f}bps P90{p90:+.1f}(负=穿线);三档重算全体净中位:")
        for tag, adj in (("无滑点(理想)", -min(0, pmed)), ("中位滑点(实测)", 0.0), ("P90滑点(悲观)", pmed - p90)):
            # adj: 把止损单的净值向"该档滑点"平移
            v = []
            for r in sub:
                b = r["bps"]
                if r["reason"] == "fast_key_stop":
                    b += adj
                v.append(b)
            rep(f"   {tag}", v)
    else:
        print("② 无止损单,穿停滑点不适用")

print("\n注:真金已了结口径,不回放;episode 去重是防触发密集期伪独立(纪律12),")
print("三档穿停敏感性回答'死板止损被滑穿多少改变结论'——若三档间结论翻转=对滑点敏感=脆。")
