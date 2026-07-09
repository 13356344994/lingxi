#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""#8b 资费结算后崩·分钟-小时级重审(2026-07-09;战略转向"定时结构"首战;只读)。

对手模型:极端负资费币的多头=纯收租仓(唯一持有理由是收资费),结算入账即卖=制度性
被迫流;结算时刻全网公开但**没有速度竞争**(卖压持续分钟级,不抢毫秒)。机构不碰
(长尾小池),同行 bot 不盯(非爆仓推送)。正宗安静池塘。

冻结口径(运行前登记;与 _8b_autopsy 同源可比):
  事件=Bybit xmeta 快照 nextFunding 钟,f≤-0.0005 主桶(f≤-0.001 深桶=唯一第二读数);
  进场=结算时刻后首个 3s 标尺点做空(旧研究 300s 粗格→3s);剔主流币;(sym,结算钟)去重;
  出场:A=fix300(旧 post5 可比对照)/ B=能量trail(unit=clip(|r60|,40,200),1×灾难/
  0.8×激活/0.4×回吐/cap1800)/ C=fix1800(30min,小时级测试);成本 12。
  州条件=平静(结算时刻 fuel<40;已登记尸检假设:风暴州反弹,平静州才崩)。
圣日纪律:挖掘=07-06/07/09;圣日 07-05+08 不碰,除非挖掘集 PF≥1.3(纪律 14h,否则处决)。
"""
import json, bisect, statistics, sys
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
# 圣日终验模式(--vault):挖掘集 PF1.79/三日同号已过预门(2026-07-09 19:05 登记),
# 本假设一次性消费圣日;纪律 14h:PF<1.3 或中位不净正 → 就地处决。
if "--vault" in sys.argv:
    MINE = {"2026-07-05", "2026-07-08"}
    print("🏛 圣日终验模式(07-05+07-08;一次性消费)")
else:
    MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}
F_MAIN, F_DEEP = -0.0005, -0.001

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

# ---- 事件:xmeta bybit 快照 → (sym, 结算钟, funding);快照在结算前≤30min 的才算(资费冻结近似)----
ev = {}
with open(f"{REC}/fast_xmeta_snapshots.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except Exception:
            continue
        T = ts(d["recorded_at"])
        for sym, v in (d.get("venues", {}).get("bybit") or {}).items():
            if not (isinstance(v, list) and len(v) >= 3 and v[2] and v[2] > 0):
                continue
            fr = float(v[0])
            if fr > F_MAIN or sym in MAJORS:
                continue
            st = float(v[2]) / 1000.0
            if st > T and st - T <= 1800:
                key = (sym, int(st))
                if key not in ev or T > ev[key][0]:
                    ev[key] = (T, fr)  # 取结算前最后一张快照的资费
events = [dict(sym=k[0], st=float(k[1]), f=v[1], day=day_of(k[1])) for k, v in ev.items()]
events = [e for e in events if e["day"] in MINE and e["st"] >= ts("2026-07-05T07:10:00")]
events.sort(key=lambda e: e["st"])
print(f"极端负资费结算事件(挖掘集)={len(events)} 深桶(f≤-0.1%)={sum(1 for e in events if e['f']<=F_DEEP)} "
      f"币数={len(set(e['sym'] for e in events))}")

# ---- 燃料钟(州条件)----
liq_ts = []
with open(f"{REC}/fast_liquidation_events.jsonl") as f:
    for line in f:
        p = line.find('"recorded_at":"')
        if p > 0:
            try:
                liq_ts.append(ts(line[p + 15:p + 34]))
            except Exception:
                pass
liq_ts.sort()

def fuel_at(t):
    return bisect.bisect_right(liq_ts, t) - bisect.bisect_left(liq_ts, t - 300)

# ---- 路径(3s 标尺;含 st-70s 供 r60)----
idx = {}
for e in events:
    idx.setdefault(e["sym"], []).append(e["st"])
paths = {(e["sym"], e["st"]): [] for e in events}
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
        for sym, sts in idx.items():
            j = bisect.bisect_right(sts, t + 70) - 1
            if j >= 0 and sts[j] - 70 <= t <= sts[j] + 1830:
                if row is None:
                    try:
                        row = json.loads(line).get("mids") or {}
                    except Exception:
                        row = {}
                m = row.get(sym)
                if m:
                    paths[(sym, sts[j])].append((t, m))

rows = []
for e in events:
    path = sorted(set(paths[(e["sym"], e["st"])]))
    pre = [(t, m) for t, m in path if t < e["st"]]
    pts = [(t - e["st"], m) for t, m in path if t >= e["st"]]
    if len(pts) < 5 or not pre:
        continue
    ent = pts[0][1]
    p60 = None
    for t, m in pre:
        if e["st"] - t >= 55:
            p60 = m
    if p60 is None:
        continue
    r60 = (ent - p60) / p60 * 1e4
    unit = min(max(abs(r60), 40.0), 200.0)
    out = dict(e, fuel=fuel_at(e["st"]))
    for cap, tag in ((300, "fix300"), (1800, "fix1800")):
        px = None
        for t, m in pts:
            u = -(m - ent) / ent * 1e4
            if t >= cap or u <= -unit:
                px = m
                break
            px = m
        if px:
            out[tag] = -(px - ent) / ent * 1e4 - COST
    peak, px = 0.0, None
    for t, m in pts:
        u = -(m - ent) / ent * 1e4
        if t >= 1800 or u <= -unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px:
        out["trail"] = -(px - ent) / ent * 1e4 - COST
    rows.append(out)
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key in ("fix300", "trail", "fix1800"):
        n = [r[key] for r in sub if key in r]
        if not n:
            continue
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {key:7s}: n={len(n):3d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

rep(rows, "主桶(f≤-0.05%)全体")
rep([r for r in rows if r["fuel"] < 40], "主桶×平静州       ")
rep([r for r in rows if r["f"] <= F_DEEP], "深桶(f≤-0.1%)     ")
rep([r for r in rows if r["f"] <= F_DEEP and r["fuel"] < 40], "深桶×平静州       ")
print("按日(主桶×平静,trail):")
for d in sorted(MINE):
    sub = [r for r in rows if r["fuel"] < 40 and r["day"] == d and "trail" in r]
    if len(sub) >= 5:
        n = [r["trail"] for r in sub]
        print(f"  {d}: n={len(n):3d} 中位{statistics.median(n):+7.1f} {'✓' if statistics.median(n)>0 else '✗'}")
    else:
        print(f"  {d}: n={len(sub)}(不足)")

print("\n== 单币主导拷问(深桶,trail;纪律:剔顶不活=作废)==")
deep = [r for r in rows if r["f"] <= F_DEEP and "trail" in r]
rep([r for r in deep if r["sym"] != "SLX-USDT"], "深桶 ex-SLX       ")
cnt = {}
for r in deep:
    cnt.setdefault(r["sym"], []).append(r["trail"])
tops = sorted(cnt.items(), key=lambda kv: -len(kv[1]))[:8]
print("  币级分布(n≥3 的币的自身中位):")
coin_meds = []
for sym, v in cnt.items():
    if len(v) >= 3:
        coin_meds.append(statistics.median(v))
for sym, v in tops:
    print(f"    {sym[:-5]:10s} n={len(v):3d} 币内中位{statistics.median(v):+7.1f}")
if coin_meds:
    print(f"  币级中位的中位(n≥3 的 {len(coin_meds)} 个币)= {statistics.median(coin_meds):+.1f} 正币率 {sum(1 for m in coin_meds if m>0)}/{len(coin_meds)}")
print("\n== 同币连锤独立性(episode=同币 4h 归并后)==")
ep_last, ep_rows = {}, []
for r in sorted(deep, key=lambda r: r["st"]):
    if r["sym"] in ep_last and r["st"] - ep_last[r["sym"]] < 14400:
        ep_last[r["sym"]] = r["st"]
        continue
    ep_last[r["sym"]] = r["st"]
    ep_rows.append(r)
rep(ep_rows, "深桶 episode(4h归并)")
rep([r for r in ep_rows if r["sym"] != "SLX-USDT"], "深桶 episode ex-SLX ")
