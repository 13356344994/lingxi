#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""死水暴动回补·圣日协议重审(2026-07-09;用户"从长尾池塘咬肉 PF≥1.5"指令;只读)。

对手模型:死水时段(燃料<20,占55%时段)的长尾币孤立暴动=单一鲁莽者的市价单事故
(无燃料接力、无同行 bot 在场、机构不碰)——事故后做市商把价格拽回。吃回补的对手=
那个已经打完的鲁莽者(不得不停)。这是"机构看不上、同行没盯"的正宗安静池塘。

冻结签名(运行前登记,与旧读数[+11.7均 n=143]同族,不扫描):
  普查行:燃料 market_liq_5m<20 且 本币无爆仓 sym_liq_5m=0(纯事故非强平)
  且 |ret60|≥40(真事故)且 |ret15|≤0.25|ret60|(冲量已停,不接飞刀)且 spread≤12;
  剔主流币;同币 600s 去重;方向=逆 ret60(回补)。
  出场三口径并行(全部先验):A=固定180s(旧读数口径)/B=能量trail(1×灾难/0.8×激活/
  0.4×回吐/cap600)/C=固定300s;成本12。
圣日纪律:挖掘=06/07/09;门=挖掘集 PF≥1.3 且分片同号才碰圣日(07-05+08),否则处决。
"""
import json, bisect, statistics
from datetime import datetime, timezone

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
MAJORS = {s + "-USDT" for s in ("BTC", "ETH", "SOL", "XRP", "BNB", "DOGE", "ADA", "LTC", "LINK", "AVAX", "TRX", "DOT", "TON")}
MINE = {"2026-07-06", "2026-07-07", "2026-07-09"}

def ts(s):
    return datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc).timestamp()

def day_of(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d")

entries = []
last = {}
with open(f"{REC}/fast_mover_census.jsonl") as f:
    for line in f:
        try:
            r = json.loads(line)
        except Exception:
            continue
        r60 = r.get("ret_60s_bps", 0.0)
        r15 = r.get("ret_15s_bps", 0.0)
        if abs(r60) < 40 or abs(r15) > 0.25 * abs(r60):
            continue
        if r.get("market_liq_5m", 99) >= 20 or r.get("sym_liq_5m", 9) != 0:
            continue
        sp = r.get("spread_bps", 99)
        if sp <= 0 or sp > 12:
            continue
        sym = r["symbol"]
        if sym in MAJORS:
            continue
        t0 = ts(r["recorded_at"])
        if t0 < ts("2026-07-05T07:10:00"):
            continue
        d = day_of(t0)
        if d not in MINE:
            continue  # 圣日行连内存都不进(挖掘绝对隔离)
        if sym in last and t0 - last[sym] < 600:
            continue
        last[sym] = t0
        entries.append(dict(sym=sym, t0=t0, r60=r60, day=d,
                            unit=min(max(abs(r60), 40.0), 200.0)))
entries.sort(key=lambda e: e["t0"])
print(f"死水事故(挖掘集)={len(entries)} 币数={len(set(e['sym'] for e in entries))}")

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

rows = []
for e in entries:
    pts = sorted(set(paths[(e["sym"], e["t0"])]))
    pts = [(t - e["t0"], m) for t, m in pts if t >= e["t0"]]
    if len(pts) < 3:
        continue
    ent = pts[0][1]
    sgn = -1 if e["r60"] > 0 else 1  # 逆暴动方向=回补
    unit = e["unit"]
    out = dict(e)
    for cap, tag in ((180, "fix180"), (300, "fix300")):
        px = None
        for t, m in pts:
            u = sgn * (m - ent) / ent * 1e4
            if t >= cap or u <= -unit:
                px = m
                break
            px = m
        if px:
            out[tag] = sgn * (px - ent) / ent * 1e4 - COST
    peak, px = 0.0, None
    for t, m in pts:
        u = sgn * (m - ent) / ent * 1e4
        if t >= 600 or u <= -unit or (peak >= 0.8 * unit and u <= peak - 0.4 * unit):
            px = m
            break
        peak = max(peak, u)
        px = m
    if px:
        out["trail"] = sgn * (px - ent) / ent * 1e4 - COST
    rows.append(out)
print(f"可评={len(rows)}")

def rep(sub, tag):
    if len(sub) < 8:
        print(f"  {tag}: n={len(sub)}(不足)")
        return
    for key in ("fix180", "fix300", "trail"):
        n = [r[key] for r in sub if key in r]
        if not n:
            continue
        gp = sum(x for x in n if x > 0)
        gl = -sum(x for x in n if x < 0)
        cnt = {}
        for r in sub:
            cnt[r["sym"]] = cnt.get(r["sym"], 0) + 1
        tsym, tn = max(cnt.items(), key=lambda kv: kv[1])
        print(f"  {tag} {key:6s}: n={len(n):3d} 中位{statistics.median(n):+7.1f} 均{sum(n)/len(n):+7.1f} "
              f"PF{(gp/gl if gl>0 else 99):.2f} 胜{sum(1 for x in n if x>0)/len(n)*100:.0f}% 顶{tsym[:-5]}({tn/len(sub)*100:.0f}%)")

rep(rows, "全体")
print("按日分片:")
for d in sorted(MINE):
    rep([r for r in rows if r["day"] == d], f"  {d}")
print("按事故方向:")
rep([r for r in rows if r["r60"] > 0], "  上冲回落(做空)")
rep([r for r in rows if r["r60"] < 0], "  下砸回弹(做多)")
