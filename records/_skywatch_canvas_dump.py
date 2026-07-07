#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""复用 _skywatch_report.py 的计算,导出 canvas 用 JSON(小时网格曲线/派班占比/统计表)。"""
import json
import collections
from datetime import datetime

src = open("/root/lingxi/records/_skywatch_report.py").read()
src = src.split("# ---------- 文档式报告图 ----------")[0]
g = {"__name__": "dump"}
exec(src, g)

T0, NOW = g["T0"], g["NOW"]
SEAL_V1, SEAL_V2 = g["SEAL_V1"], g["SEAL_V2"]
duty_at, S = g["duty_at"], g["S"]
e_g, f_g, w_g, combo = g["e_g"], g["f_g"], g["w_g"], g["combo"]
e_u, f_u, w_u = g["e_u"], g["f_u"], g["w_u"]

hours = []
t = (int(T0) // 3600) * 3600 + 3600
while t <= NOW:
    hours.append(t)
    t += 3600


def cum_at(trades, ts_list):
    out, s, i = [], 0.0, 0
    for ts in ts_list:
        while i < len(trades) and trades[i][0] <= ts:
            s += trades[i][2]
            i += 1
        out.append(round(s, 1))
    return out


duty_share = []
for h in hours:
    c = collections.Counter()
    for k in range(12):
        c[duty_at(h - 3600 + k * 300 + 150)] += 1
    duty_share.append({d: c.get(d, 0) * 100 // 12 for d in ("none", "xgap", "firework", "fire")})

cats = [datetime.utcfromtimestamp(h).strftime("%d日%H时") for h in hours]


def stat_row(label, trades):
    st = S(trades)
    return dict(label=label, n=st["n"], med=round(st["med"], 1), tot=round(st["tot"]),
                pf=round(st["pf"], 2), wr=round(st["wr"]))


by_sym = collections.defaultdict(float)
by_sym_n = collections.Counter()
for t_, sym, v in combo:
    by_sym[sym] += v
    by_sym_n[sym] += 1
tops = sorted(by_sym.items(), key=lambda kv: -kv[1])[:5]
bots = sorted(by_sym.items(), key=lambda kv: kv[1])[:5]

out = dict(
    window="%s → %s UTC" % (datetime.utcfromtimestamp(T0).strftime("%m-%d %H:%M"),
                            datetime.utcfromtimestamp(NOW).strftime("%m-%d %H:%M")),
    generated=datetime.utcfromtimestamp(NOW).strftime("%Y-%m-%d %H:%M UTC"),
    cats=cats,
    curves=dict(
        combo=cum_at(combo, hours),
        e=cum_at(e_g, hours),
        fire=cum_at(f_g, hours),
        firework=cum_at(w_g, hours),
        e_ungated=cum_at(e_u, hours),
    ),
    duty=duty_share,
    stats=[stat_row("E 捡便宜货(值班内)", e_g),
           stat_row("火灾瀑布刀(值班内)", f_g),
           stat_row("烟花反手刀(值班内)", w_g),
           stat_row("司天监 合计", combo),
           stat_row("E(无派班对照)", e_u),
           stat_row("火刀(无派班对照)", f_u),
           stat_row("烟花(无派班对照)", w_u)],
    segs=[stat_row("发现窗(<00:50Z)", [x for x in combo if x[0] < SEAL_V1]),
          stat_row("血窗段(00:50-04:15Z)", [x for x in combo if SEAL_V1 <= x[0] < SEAL_V2]),
          stat_row("干净段(>=04:15Z)", [x for x in combo if x[0] >= SEAL_V2])],
    tops=[dict(sym=s.replace("-USDT", ""), net=round(v), n=by_sym_n[s]) for s, v in tops],
    bots=[dict(sym=s.replace("-USDT", ""), net=round(v), n=by_sym_n[s]) for s, v in bots],
)
path = "/root/lingxi/records/skywatch_canvas_data.json"
json.dump(out, open(path, "w"), ensure_ascii=False)
print("dumped:", path, "hours=%d" % len(hours))
