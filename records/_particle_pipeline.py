#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""粒子流水线(用户 07-05 提案制度化;章程见 HANDOFF ⑦⑧⑨条)。

mine 模式:当前全部确认事件=挖掘窗 → 全粒子(含方向交互项)扫组合(深度≤3)→
  按用户口径排序(主指标=净中位;门:止错中性化胜率≥50%、净PF≥1.3、n≥15、非单币主导)
  → 冻结 top2 进 _particle_champions.json(带冻结时刻,判决集=其后新事件)。
verdict 模式:读冠军文件,对冻结时刻之后的新事件出判决进度/终审。

用户口径(07-05 23:39 落实):
  净=毛-12bps;刮痕单(|毛|≤5bps)=止错,从胜率分子分母剔除(止错≠失败);
  胜率(中性化)与净PF双正才算"盈利状态";净中位为主排序键。
"""
import json
import sys
import bisect
import calendar
import collections
import itertools
import os
import statistics
from datetime import datetime

REC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "okx_directional")
CHAMP = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_particle_champions.json")
COST = 12.0
SCRATCH = 5.0


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


# ===== 数据装载:复用组合扫描器的 rows(含全部基础特征)=====
_here = os.path.dirname(os.path.abspath(__file__))
_loader = open(os.path.join(_here, "_tape_feature_combos.py")).read().split("def stat(")[0]
exec(_loader)  # noqa: 提供 rows / FEATS / paths / pk / rspr_med 等

# 追加锚特征(烟花解剖同源):okxspot / bybit
_anch = collections.defaultdict(lambda: collections.defaultdict(list))
with open(REC + "/fast_hot_mids.jsonl") as f:
    for line in f:
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if d.get("backlog_sym"):
            v = d.get("backlog_venue")
            if v in ("okxspot", "bybit"):
                for pt in d.get("backlog", []):
                    if isinstance(pt, (list, tuple)) and len(pt) >= 2:
                        _anch[v][d["backlog_sym"]].append((pt[0] / 1000.0, pt[1]))
            continue
        for v, m in (d.get("anchors") or {}).items():
            if v not in ("okxspot", "bybit"):
                continue
            for sym, val in m.items():
                if isinstance(val, (list, tuple)) and len(val) >= 2:
                    _anch[v][sym].append((val[1] / 1000.0, val[0]))
for v in _anch:
    for s in _anch[v]:
        _anch[v][s] = sorted(set(_anch[v][s]))


def _gap(venue, sym, px, t):
    arr = _anch[venue].get(sym)
    if not arr or not px:
        return None
    keys = [x[0] for x in arr]
    i = bisect.bisect_right(keys, t) - 1
    if i < 0 or t - arr[i][0] > 15:
        return None
    a = arr[i][1]
    return (px - a) / a * 1e4 if a > 0 else None


for e in rows:
    e["xgap"] = _gap("bybit", e["sym"], e["px"], e["t0"])
    e["spotgap"] = _gap("okxspot", e["sym"], e["px"], e["t0"])

# ===== 粒子清单(全部带对手模型注记;二值;交互项以"方向粒子×特征粒子"自然生成)=====
PARTICLES = collections.OrderedDict([
    # 方向粒子(火灾/烟花——交互项的载体)
    ("火灾(跟空)", lambda e: e["dir"] < 0),
    ("烟花(跟多)", lambda e: e["dir"] > 0),
    # 磁带/池塘
    ("磁带活跃", lambda e: e["base"] >= 50),
    ("非死磁带", lambda e: e["base"] >= 20),
    ("占市值比高", lambda e: e["oi"] and e["ntl"] / e["oi"] >= 0.0017),
    ("大额2kU", lambda e: e["ntl"] >= 2000),
    # 确认/结构
    ("方向一致", lambda e: bool(e.get("sm"))),
    ("簇爆", lambda e: e.get("symliq", 0) >= 2),
    ("首发", lambda e: e.get("prior30", 0) == 0),
    ("方向侧突破", lambda e: e.get("brk") is True),
    ("前动量强", lambda e: e.get("r60p") is not None and abs(e["r60p"]) >= 50),
    ("OI瓦解", lambda e: e["oi"] and e["oi60"] and (e["oi"] - e["oi60"]) / e["oi60"] <= -0.001),
    # 市场状态(慢变量)
    ("高燃料", lambda e: e["fuel"] >= 40),
    ("燃料上升期", lambda e: e.get("fuelrise") is True),
    # 跨所/现货锚
    ("跨所溢价高", lambda e: e.get("xgap") is not None and e["xgap"] >= 20),
    ("现货领先", lambda e: e.get("spotgap") is not None and e["spotgap"] <= 0),
    # 执行质量
    ("窄点差", lambda e: e["spread"] is not None and e["spread"] <= 2.3),
])


def metrics(sel):
    """用户口径:净中位/净PF/止错中性化胜率。"""
    v = [e["v"] for e in sel]
    net = [x - COST for x in v]
    wins = [x for x in v if x > SCRATCH]
    losses = [x for x in v if x < -SCRATCH]
    winrate = 100.0 * len(wins) / (len(wins) + len(losses)) if (wins or losses) else 0.0
    pw = sum(x for x in net if x > 0)
    pl = -sum(x for x in net if x < 0)
    pf = pw / pl if pl > 0 else (99.0 if pw > 0 else 0.0)
    return {
        "n": len(v), "net_med": statistics.median(net) if net else 0.0,
        "net_mean": sum(net) / len(net) if net else 0.0, "net_sum": sum(net),
        "win_neu": winrate, "scratch_n": len(v) - len(wins) - len(losses), "pf_net": pf,
    }


def dominated(sel):
    c = collections.Counter(e["sym"] for e in sel)
    top, n = c.most_common(1)[0]
    return n * 2 > len(sel), "%s:%d" % (top.replace("-USDT", ""), n)


def scan(evts, nmin=15):
    names = list(PARTICLES)
    out = []
    for depth in (1, 2, 3):
        for combo in itertools.combinations(names, depth):
            fs = [PARTICLES[c] for c in combo]
            if "火灾(跟空)" in combo and "烟花(跟多)" in combo:
                continue
            sel = [e for e in evts if all(f(e) for f in fs)]
            if len(sel) < nmin:
                continue
            dom, top = dominated(sel)
            if dom:
                continue
            m = metrics(sel)
            m.update(tag="×".join(combo), top=top, depth=depth)
            out.append(m)
    return out


def fmt(m):
    return "%-38s n=%-3d 净中位=%+7.1f 净均=%+7.1f 胜(中性化)=%3.0f%% 刮痕=%d 净PF=%5.2f %s" % (
        m["tag"], m["n"], m["net_med"], m["net_mean"], m["win_neu"], m["scratch_n"], m["pf_net"], m["top"])


# ===== 状态格子(章程⑪:盘中"选"的地基——每个 状态×组合 格子独立记账/独立判决)=====
# 状态=市场燃料(物理慢变量,时段的真身);先二态控预算,验出差异再谈三态。
STATES = collections.OrderedDict([
    ("死水(fuel<20)", lambda e: e["fuel"] < 20),
    ("有燃料(fuel>=20)", lambda e: e["fuel"] >= 20),
])


def state_lines(sel, indent="    "):
    out = []
    for sname, sf in STATES.items():
        sub = [e for e in sel if sf(e)]
        if len(sub) < 5:
            out.append("%s%-16s n=%d(不足)" % (indent, sname, len(sub)))
            continue
        m = metrics(sub)
        out.append("%s%-16s n=%-3d 净中位=%+7.1f 胜(中性化)=%3.0f%% 净PF=%5.2f" % (
            indent, sname, m["n"], m["net_med"], m["win_neu"], m["pf_net"]))
    return out


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "mine"
    now = datetime.utcnow()
    if mode == "mine":
        res = scan(rows)
        # 用户双正门:中性化胜率≥50% 且 净PF≥1.3;排序=净中位;奥卡姆:同分取浅
        ok = [m for m in res if m["win_neu"] >= 50 and m["pf_net"] >= 1.3]
        ok.sort(key=lambda m: (-m["net_med"], m["depth"]))
        print("挖掘窗=全部 %d 事件(截至 %sZ)| 过双正门组合=%d" % (len(rows), now.strftime("%H:%M"), len(ok)))
        print("\n== 过门组合 top10(净口径,主排序=净中位,奥卡姆同分取浅)==")
        names = list(PARTICLES)
        for m in ok[:10]:
            print("  " + fmt(m))
            fs = [PARTICLES[c] for c in m["tag"].split("×")]
            for ln in state_lines([e for e in rows if all(f(e) for f in fs)]):
                print(ln)
        print("\n== 未过门但净中位 top5(对照)==")
        rest = sorted([m for m in res if m not in ok], key=lambda m: -m["net_med"])
        for m in rest[:5]:
            print("  " + fmt(m))
        champs = ok[:2]
        if champs:
            json.dump({
                "frozen_at_utc": now.strftime("%Y-%m-%dT%H:%M:%S"),
                "frozen_at_ep": now.timestamp() // 1 ,
                "cost_bps": COST, "scratch_bps": SCRATCH,
                "verdict_need_n": 30,
                "gates": {"win_neu_min": 50, "pf_net_min": 1.3, "net_med_min": 0},
                "champions": [{"tag": m["tag"], "mine": {k: round(v, 2) if isinstance(v, float) else v
                                                          for k, v in m.items() if k != "tag"}} for m in champs],
            }, open(CHAMP, "w"), ensure_ascii=False, indent=1)
            print("\n已冻结冠军 %d 个 → %s(判决集=此刻之后新确认事件,n≥30 终审)" % (len(champs), os.path.basename(CHAMP)))
    else:  # verdict
        cfg = json.load(open(CHAMP))
        t_frozen = ep(cfg["frozen_at_utc"])
        fresh = [e for e in rows if e["t0"] >= t_frozen]
        print("冠军冻结于 %sZ | 判决集新事件=%d" % (cfg["frozen_at_utc"], len(fresh)))
        names = list(PARTICLES)
        for ch in cfg["champions"]:
            combo = ch["tag"].split("×")
            fs = [PARTICLES[c] for c in combo if c in PARTICLES]
            sel = [e for e in fresh if all(f(e) for f in fs)]
            m = metrics(sel)
            m.update(tag=ch["tag"], top=dominated(sel)[1] if sel else "-", depth=len(combo))
            need = cfg.get("verdict_need_n", 30)
            status = "【到线】" if m["n"] >= need else "%d/%d" % (m["n"], need)
            print("  %s %s" % (status, fmt(m)))
            for ln in state_lines(sel):
                print(ln)
            if m["n"] >= need:
                g = cfg["gates"]
                ok = m["win_neu"] >= g["win_neu_min"] and m["pf_net"] >= g["pf_net_min"] and m["net_med"] > g["net_med_min"]
                dom, _ = dominated(sel)
                print("    → 终审:%s%s" % ("过检(交用户裁探针)" if ok and not dom else "不过",
                                             "(单币主导作废)" if dom else ""))
