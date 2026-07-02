#!/usr/bin/env python3
# 方向探索档「成交后反馈」归因(阶段1)。只读 settlements.jsonl,仓位级聚合(按 position_id 合并分批+全平)。
# 回答 checkpoint 报告的 1~4 节;第 5 节(具体方案)由人写。
#
# 重要局限(诚实声明):当前 mfe/mae 是「整段持有期」的运行高/低水位,不是时间切片(无 30s/60s 快照),
#   且记录的 MAE 会漏掉触发平仓那一跳(快照新鲜度瑕疵)→ 真实最差点比记录更深。
#   因此「30s/60s 无反馈即平」无法被精确反事实模拟,只能用「整段从未浮盈」做下界近似(见第4节)。
#   精确模拟需在阶段2 加时间切片埋点(mfe@30s/60s/180s),本脚本会标注。
#
# 用法: python3 records/_directional_feedback_audit.py   (仓库根或 records/ 内均可)

import json, os, collections
from datetime import datetime, timezone, timedelta

NEWKEYS = ["hold_seconds", "open_via_taker", "close_via_taker", "entry_slip_bps",
           "exit_slip_bps", "fee_usdt", "mfe_account_pct", "mae_account_pct"]
VENUES = ["okx", "bybit", "binance", "gate"]
BJ = timezone(timedelta(hours=8))  # 北京时间


def parse_ts(s):
    """解析结算 ISO 时间(UTC,容忍纳秒/Z)为 aware datetime。失败返回 None。
    兼容老 Python(用 strptime,不依赖 fromisoformat)。"""
    if not s:
        return None
    s = s.strip().rstrip("Z")
    # 去掉时区偏移(结算时间均为 UTC)
    for sep in ("+", ):
        if sep in s[11:]:
            s = s[:11] + s[11:].split(sep, 1)[0]
    if "." in s:
        head, frac = s.split(".", 1)
        frac = "".join(ch for ch in frac if ch.isdigit())[:6]
        s = head + "." + frac
        fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    try:
        return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def open_bj(row):
    """仓位开仓时间(北京)。优先 position_id 的 UnixNano 后缀;否则用 平仓时间 − hold。"""
    pid = row.get("position_id", "")
    if pid and "-" in pid:
        tail = pid.rsplit("-", 1)[-1]
        if tail.isdigit() and len(tail) >= 18:  # UnixNano ~19 位
            try:
                return datetime.fromtimestamp(int(tail) / 1e9, tz=timezone.utc).astimezone(BJ)
            except (ValueError, OSError):
                pass
    ct = parse_ts(row.get("time"))
    if ct is None:
        return None
    hold = row.get("hold_seconds")
    if hold is None:
        hold = row.get("hold_minutes", 0) * 60
    return (ct - timedelta(seconds=hold)).astimezone(BJ)


def session_of(hour):
    if hour is None:
        return "unknown"
    if 0 <= hour <= 7:
        return "night(0-7)"
    if 8 <= hour <= 12:
        return "morning(8-12)"
    if 13 <= hour <= 18:
        return "afternoon(13-18)"
    return "evening(19-23)"


CAP50_BJ = datetime(2026, 6, 28, 19, 25, 0, tzinfo=BJ)  # OKX/Bybit 50U 切换(11:25 UTC)
# phase E = taker_fallback_venues="binance" 灰度部署。实证: 6-28 23:29:31 BJ = 6-28 15:29:31 UTC(该时刻后 okx/bybit 无 taker)。
E_START_BJ = datetime(2026, 6, 28, 23, 29, 31, tzinfo=BJ)
# analysis_window(≠phase E): 50U 切换后的当前版本宽窗口
POST50_BJ = CAP50_BJ


def regime_degrade_candidate(env):
    """C 候选 record-only 命中标记(纯派生统计字段;非生产逻辑、不改实盘)。
    命中 = BTC 过渡/震荡带 (0.1<=btc_trend_strength<0.2) 或 market_mode==broad_up。"""
    if not isinstance(env, dict):
        return False, None
    bt = env.get("btc_trend_strength")
    if bt is not None and 0.1 <= bt < 0.2:
        return True, "btc_trend_0.1-0.2"
    if env.get("candidate_market_mode") == "broad_up":
        return True, "broad_up"
    return False, None


def stage_of(p):
    # 严格口径:D=带 open_micro.env(v2);C=有 open_micro 无 env(v1);B=micro小名义;A=旧版大仓(大名义)
    if p.get("has_env"):
        return "D OpenMicro_v2"
    if p.get("has_micro"):
        return "C OpenMicro_v1"
    if (p.get("notional") or 0) <= 12:
        return "B micro_15U"
    return "A 旧版大仓"


def cap_tier(p):
    v, ob = p.get("venue"), p.get("open_bj")
    if v in ("okx", "bybit") and ob and ob >= CAP50_BJ:
        return "50U"
    if stage_of(p).startswith("A"):
        return "大仓"
    return "15U"



def _is_fast_row(r):
    """fast 层结算行判定:fast_scalper_v1 的 position_id 带 fast- 前缀 / reason 带 [fast] 标记。
    慢层(90s 方向微鳞)研究必须剔除 fast 行,否则 3855 笔 fast 学费会淹没慢层信号(口径污染)。"""
    pid = str(r.get("position_id") or "")
    return pid.startswith("fast-") or "[fast]" in str(r.get("reason") or "") or str(r.get("strategy") or "") == "fast_scalper_v1"

def load(include_fast=False):
    rows = []
    for v in VENUES:
        for f in (f"records/{v}_directional/settlements.jsonl", f"{v}_directional/settlements.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        r = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if not include_fast and _is_fast_row(r):
                        continue
                    r["_venue"] = v
                    rows.append(r)
                break
    return rows


def pf(vals):
    g = sum(x for x in vals if x > 0)
    l = sum(x for x in vals if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def avg(xs):
    return sum(xs) / len(xs) if xs else 0.0


def pctile(xs, p):
    if not xs:
        return 0.0
    xs = sorted(xs)
    return xs[max(0, min(len(xs) - 1, int(p / 100 * len(xs))))]


def build_all_positions(rows):
    """对全部结算行做仓位级聚合(含时间维度);用于时段统计,覆盖 instrumented 之前的老数据。"""
    agg = {}
    for r in rows:
        pid = r.get("position_id") or f'{r.get("symbol")}|{r.get("side")}|{r.get("entry",0):.8f}'
        p = agg.get(pid)
        if p is None:
            ob = open_bj(r)
            p = {"net": 0.0, "open_taker": False, "hard_stop": False, "mfe": 0.0, "mae": 0.0,
                 "spread": None, "depthcov": None, "runup": None, "has_ts": False, "has_micro": False,
                 "has_env": False, "env": None, "is_e": bool(ob and ob >= E_START_BJ),
                 "open_bj": ob, "hour": (ob.hour if ob else None), "notional": 0.0,
                 "session": session_of(ob.hour if ob else None), "venue": r.get("_venue")}
            agg[pid] = p
        p["notional"] = max(p["notional"], r.get("notional", 0.0))
        p["net"] += r.get("realized_usdt", 0.0)
        p["open_taker"] = p["open_taker"] or bool(r.get("open_via_taker"))
        if "硬止损" in r.get("reason", ""):
            p["hard_stop"] = True
        p["mfe"] = max(p["mfe"], r.get("mfe_account_pct", 0.0))
        p["mae"] = min(p["mae"], r.get("mae_account_pct", 0.0))
        if any(k in r for k in NEWKEYS):
            p["has_ts"] = True
        om = r.get("open_micro")
        if isinstance(om, dict):
            p["has_micro"] = True
            if isinstance(om.get("env"), dict):
                p["has_env"] = True
                if p["env"] is None:
                    p["env"] = om["env"]
            p["spread"] = om.get("spread_bps")
            p["depthcov"] = om.get("depth_cover_ratio_10bps")
            p["runup"] = om.get("pre_open_runup_bps")
    return list(agg.values())


def time_report(label, positions):
    print(f"\n—— 窗口: {label}  仓位={len(positions)} ——")
    if not positions:
        print("    (无样本)")
        return
    by = collections.defaultdict(list)
    for p in positions:
        by[p["session"]].append(p)
    for s in ["night(0-7)", "morning(8-12)", "afternoon(13-18)", "evening(19-23)", "unknown"]:
        g = by.get(s)
        if not g:
            continue
        nets = [p["net"] for p in g]
        wins = [x for x in nets if x > 0]
        loss = [x for x in nets if x < 0]
        hs = sum(1 for p in g if p["hard_stop"]) / len(g) * 100
        tk = sum(1 for p in g if p["open_taker"]) / len(g) * 100
        sps = [p["spread"] for p in g if p["spread"] is not None]
        dcs = [p["depthcov"] for p in g if p["depthcov"] is not None]
        rus = [p["runup"] for p in g if p["runup"] is not None]
        flag = "" if len(g) >= 30 else "  ⚠样本不足(仅倾向)"
        print(f"    {s:<17} n={len(g):<4} PF={pf(nets):<5.2f} 胜率={len(wins)/len(g)*100:<3.0f}% "
              f"均盈={avg(wins):+.4f}U 均亏={avg(loss):+.4f}U 净={sum(nets):+.3f}U 硬止损={hs:.0f}% taker={tk:.0f}%{flag}")
        extra = f"        MFE中位={pctile([p['mfe'] for p in g],50)*100:+.1f}% MAE中位={pctile([p['mae'] for p in g],50)*100:+.1f}%"
        if sps:
            extra += f" spread中位={pctile(sps,50):.1f}bps"
        if dcs:
            extra += f" depth中位={pctile(dcs,50):.0f}x"
        if rus:
            extra += f" runup中位={pctile(rus,50):+.1f}bps"
        print(extra)
    # 小时级 PF/仓数速览(仅展示有样本的小时)
    byh = collections.defaultdict(list)
    for p in positions:
        if p["hour"] is not None:
            byh[p["hour"]].append(p["net"])
    if byh:
        cells = []
        for h in range(24):
            if h in byh:
                cells.append(f"{h:02d}h:PF{pf(byh[h]):.1f}/n{len(byh[h])}")
        print("    小时级: " + "  ".join(cells))


def time_dimension_section(rows):
    print("\n【7. 时间维度(北京时间;只统计,不做交易过滤)】")
    print("  时段定义: night0-7 / morning8-12 / afternoon13-18 / evening19-23。<30仓仅标倾向。")
    allp = build_all_positions(rows)
    now_bj = datetime.now(BJ)
    today = [p for p in allp if p["open_bj"] and p["open_bj"].date() == now_bj.date()]
    micro = [p for p in allp if p["has_ts"]]
    openm = [p for p in allp if p["has_micro"]]
    time_report("全历史", allp)
    time_report("今日(北京)", today)
    time_report("micro-position埋点后", micro)
    time_report("OpenMicro部署后", openm)

    def session_venue(positions, label):
        print(f"\n  [时间×venue · {label}] session × 所 PF(n):")
        for s in ["night(0-7)", "morning(8-12)", "afternoon(13-18)", "evening(19-23)"]:
            parts = []
            for v in ["okx", "bybit", "binance"]:
                g = [p["net"] for p in positions if p["session"] == s and p["venue"] == v]
                if g:
                    parts.append(f"{v}:PF{pf(g):.2f}/n{len(g)}")
            if parts:
                print(f"    {s:<17} " + "  ".join(parts))

    def session_makertaker(positions, label):
        print(f"\n  [时间×maker/taker · {label}] session × 开仓方式 PF(n):")
        for s in ["night(0-7)", "morning(8-12)", "afternoon(13-18)", "evening(19-23)"]:
            mk = [p["net"] for p in positions if p["session"] == s and not p["open_taker"]]
            tk = [p["net"] for p in positions if p["session"] == s and p["open_taker"]]
            seg = []
            if mk:
                seg.append(f"maker:PF{pf(mk):.2f}/n{len(mk)}")
            if tk:
                seg.append(f"taker:PF{pf(tk):.2f}/n{len(tk)}")
            if seg:
                print(f"    {s:<17} " + "  ".join(seg))

    # 交叉分析在「今日」+「OpenMicro后」窗口做(避免旧版本污染);全历史已在上方时段表。
    for lbl, sub in [("今日", today), ("OpenMicro后", openm)]:
        if sub:
            session_venue(sub, lbl)
            session_makertaker(sub, lbl)
    print("  注:某时段持续 PF<1 且样本足 → 仅作「时段降档候选」(降并发/关taker/提min_conviction/延cooldown/降名义),不停机。")


def stage_dimension_section(rows):
    print("\n【10. 阶段窗口切分(避免混样本;只统计)】")
    print("  阶段: A旧版大仓 / B micro_15U / C OpenMicro_v1 / D OpenMicro_v2;档位 15U/50U(OKX/Bybit≥19:25BJ=50U,Binance全程15U)")
    P = build_all_positions(rows)
    byst = collections.defaultdict(list)
    for p in P:
        byst[stage_of(p)].append(p)
    print("  — 阶段汇总:")
    for s in sorted(byst):
        g = byst[s]
        nets = [x["net"] for x in g]
        w = sum(1 for x in nets if x > 0)
        hs = sum(1 for x in g if x["hard_stop"]) / len(g) * 100
        tk = sum(1 for x in g if x["open_taker"]) / len(g) * 100
        flag = "" if len(g) >= 30 else " ⚠不足"
        print(f"    {s:<16} n={len(g):<4} PF={pf(nets):.2f} 胜率={w/len(g)*100:.0f}% 净={sum(nets):+.2f}U 硬止损={hs:.0f}% taker={tk:.0f}%{flag}")
    night = [p for p in P if p["session"] == "night(0-7)"]
    print(f"\n  — night(0-7) × venue × 阶段 PF(n)  [night总{len(night)}仓]:")
    for s in sorted(set(stage_of(p) for p in night)):
        parts = []
        for v in ["okx", "bybit", "binance"]:
            nets = [p["net"] for p in night if stage_of(p) == s and p["venue"] == v]
            if nets:
                parts.append(f"{v}:PF{pf(nets):.2f}/n{len(nets)}")
        if parts:
            print(f"    {s:<16} " + "  ".join(parts))
    print("\n  — night × venue × 档位(15U/50U) PF(n)/硬止损/taker:")
    for v in ["okx", "bybit", "binance"]:
        for cap in ["大仓", "15U", "50U"]:
            g = [p for p in night if p["venue"] == v and cap_tier(p) == cap]
            if g:
                nets = [p["net"] for p in g]
                hs = sum(1 for p in g if p["hard_stop"]) / len(g) * 100
                tk = sum(1 for p in g if p["open_taker"]) / len(g) * 100
                flag = "" if len(g) >= 30 else " ⚠不足"
                print(f"    {v:<8} {cap:<4} PF={pf(nets):.2f}/n{len(g)} 硬止损={hs:.0f}% taker={tk:.0f}%{flag}")
    print("  注:night 各阶段/各档样本多数仍 <30 → 仅候选,绝不据此降档(纪律)。")


def regime_marker_section(rows):
    print("\n【12. regime_degrade_candidate 命中标记(record-only 派生字段;非生产逻辑/不改实盘)】")
    print("  规则: (0.1<=btc_trend_strength<0.2) 或 market_mode==broad_up。仅 §11.5 第一优先 record-only 候选,非 active 规则。")
    print("  ⚠️ phase E 与 analysis_window=post_50U_window 是两回事,分开展示,不得互相套用结论。")
    P = build_all_positions(rows)
    envp = [p for p in P if p["env"] is not None]
    if not envp:
        print("  (无 env 仓,尚未产生)"); return

    def cmp(label, g):
        if not g:
            print(f"  {label}: 无样本"); return
        cand = [p for p in g if regime_degrade_candidate(p["env"])[0]]
        non = [p for p in g if not regime_degrade_candidate(p["env"])[0]]
        print(f"  {label}  (env仓 n={len(g)})")
        for nm, sub in [("命中candidate", cand), ("非命中", non)]:
            if not sub:
                print(f"      {nm:<14} n=0"); continue
            nets = [p["net"] for p in sub]
            hs = sum(1 for p in sub if p["hard_stop"]) / len(sub) * 100
            w = sum(1 for x in nets if x > 0) / len(sub) * 100
            flag = "" if len(sub) >= 30 else " ⚠<30"
            print(f"      {nm:<14} n={len(sub):<4} PF={pf(nets):.2f} 净={sum(nets):+.2f}U 胜率={w:.0f}% 硬止损={hs:.0f}%{flag}")

    print("\n  — analysis_window 维度 —")
    cmp("[window=post_50U_window]", [p for p in envp if p["open_bj"] and p["open_bj"] >= POST50_BJ])
    print("\n  — phase 维度(E 单列,不并入上面 window) —")
    cmp("[phase=E taker灰度]", [p for p in envp if p["is_e"]])
    cmp("[phase=D 非E(E前的v2)]", [p for p in envp if not p["is_e"]])
    print("\n  — capital_regime 维度(post_50U_window 内) —")
    w = [p for p in envp if p["open_bj"] and p["open_bj"] >= POST50_BJ]
    cmp("[cap=50U okx/bybit]", [p for p in w if cap_tier(p) == "50U"])
    cmp("[cap=15U binance]", [p for p in w if cap_tier(p) == "15U"])
    sub = collections.Counter()
    for p in envp:
        ok, why = regime_degrade_candidate(p["env"])
        if ok:
            sub[why] += 1
    print(f"\n  命中细分(全 env 仓): {dict(sub)}")
    print("  注:broad_up 桶样本通常 <30,active 降档暂不上线;需跨 ≥2 BTC 行情期 + 各命中桶 env≥30 再议。")


def main():
    rows = load()
    ins = [r for r in rows if any(k in r for k in NEWKEYS)]

    # 仓位级聚合:按 position_id 合并(分批止盈 + 全平 = 同一 position_id)。
    pos = collections.defaultdict(lambda: {"net": 0.0, "open_taker": False, "mfe": 0.0,
                                            "mae": 0.0, "fee": 0.0, "venue": "", "hold": 0,
                                            "mfe30": 0.0, "mfe60": 0.0, "has_ts": False,
                                            "has_micro": False, "spread": None, "depthcov": None,
                                            "runup": None, "maker_rounds": None, "maker_rejected": False})
    for r in ins:
        pid = r.get("position_id") or f'{r["symbol"]}|{r["side"]}|{r.get("entry",0):.8f}'
        p = pos[pid]
        p["net"] += r.get("realized_usdt", 0.0)
        p["fee"] += r.get("fee_usdt", 0.0)
        p["open_taker"] = p["open_taker"] or bool(r.get("open_via_taker"))
        p["mfe"] = max(p["mfe"], r.get("mfe_account_pct", 0.0))
        p["mae"] = min(p["mae"], r.get("mae_account_pct", 0.0))
        p["hold"] = max(p["hold"], r.get("hold_seconds", 0))
        p["venue"] = r["_venue"]
        if "mfe_30s_pct" in r or "mfe_60s_pct" in r:
            p["has_ts"] = True
            p["mfe30"] = max(p["mfe30"], r.get("mfe_30s_pct", 0.0))
            p["mfe60"] = max(p["mfe60"], r.get("mfe_60s_pct", 0.0))
        # 阶段2A 开仓微结构(嵌套 open_micro;部署后才有)
        om = r.get("open_micro")
        if isinstance(om, dict):
            p["has_micro"] = True
            p["spread"] = om.get("spread_bps", 0.0)
            p["depthcov"] = om.get("depth_cover_ratio_10bps", 0.0)
            p["runup"] = om.get("pre_open_runup_bps", 0.0)
            p["maker_rounds"] = om.get("maker_rounds_used", 0)
            p["maker_rejected"] = bool(om.get("maker_rejected"))
            env = om.get("env")
            if isinstance(env, dict):
                p["env"] = env
    P = list(pos.values())
    n = len(P)

    print("=" * 64)
    print("方向探索档 成交后反馈归因(阶段1)")
    print("=" * 64)

    # ===== 1. 样本情况 =====
    print(f"\n【1. 样本情况】")
    print(f"  总结算行={len(rows)}  带新埋点结算行={len(ins)}  仓位级样本={n} 仓")
    if n < 5:
        print("  判定:样本极少(<5),不做任何判断。等更多平仓后重跑。")
        return
    elif n < 30:
        verdict = "样本<30 → 只能给【倾向性判断】,不具备上线依据,禁止据此改实盘。"
    elif n <= 50:
        verdict = "样本 30~50 → 可作【初步改动依据】,但阈值需保守、留观察期。"
    else:
        verdict = "样本>50 → 具备较可靠依据。"
    print(f"  判定:{verdict}")
    print(f"  仓位级PF={pf([p['net'] for p in P]):.2f}  净={sum(p['net'] for p in P):+.3f}U  "
          f"实扣fee合计={sum(p['fee'] for p in P):.3f}U  hold中位={pctile([p['hold'] for p in P],50)}s")

    # ===== 2. maker vs taker =====
    print(f"\n【2. maker vs taker 开仓(仓位级)】")
    for label, sel in [("maker开仓", lambda p: not p["open_taker"]),
                       ("taker开仓", lambda p: p["open_taker"])]:
        sub = [p for p in P if sel(p)]
        if not sub:
            print(f"  {label}: n=0")
            continue
        nets = [p["net"] for p in sub]
        wins = [x for x in nets if x > 0]
        loss = [x for x in nets if x < 0]
        print(f"  {label}: n={len(sub):<3} PF={pf(nets):.2f} 胜率={len(wins)/len(sub)*100:.0f}% "
              f"净={sum(nets):+.3f}U 均盈={avg(wins):+.4f} 均亏={avg(loss):+.4f}")
    tk = [p for p in P if p["open_taker"]]
    mk = [p for p in P if not p["open_taker"]]
    if tk and mk:
        if pf([p["net"] for p in tk]) < pf([p["net"] for p in mk]):
            print("  → taker 桶 PF 低于 maker 桶(倾向:taker fallback 在拖累;样本足够再议是否限制/关闭)。")
        else:
            print("  → taker 桶 PF 未明显差于 maker(暂无关闭依据)。")

    # ===== 3. MFE / MAE =====
    print(f"\n【3. MFE/MAE 分布(本金口径%)】")
    winners = [p for p in P if p["net"] > 0]
    losers = [p for p in P if p["net"] < 0]
    if winners:
        print(f"  盈仓 n={len(winners)}: MFE中位={pctile([p['mfe'] for p in winners],50)*100:+.1f}% "
              f"MFE-p25={pctile([p['mfe'] for p in winners],25)*100:+.1f}% "
              f"MAE中位={pctile([p['mae'] for p in winners],50)*100:+.1f}% "
              f"hold中位={pctile([p['hold'] for p in winners],50)}s")
    if losers:
        print(f"  亏仓 n={len(losers)}: MFE中位={pctile([p['mfe'] for p in losers],50)*100:+.1f}% "
              f"MFE-p75={pctile([p['mfe'] for p in losers],75)*100:+.1f}% "
              f"MAE中位={pctile([p['mae'] for p in losers],50)*100:+.1f}% "
              f"hold中位={pctile([p['hold'] for p in losers],50)}s")
        # MAE「救不回来」:跌到 ≤ -X% 后最终仍为负的比例(近似 P(loss | 已浮亏X))
        print("  MAE 救援率(跌到该浮亏档后最终仍亏的比例):")
        for x in (0.02, 0.03, 0.05):
            grp = [p for p in P if p["mae"] <= -x]
            if grp:
                stillneg = sum(1 for p in grp if p["net"] < 0)
                print(f"    MAE≤-{x*100:.0f}%: n={len(grp)} 最终仍亏={stillneg}/{len(grp)}={stillneg/len(grp)*100:.0f}%")

    # ===== 4. scratch exit 预期(近似 + 局限) =====
    print(f"\n【4. scratch exit 预期(下界近似)】")
    print("  ⚠ 局限:无时间切片,无法精确模拟「30s/60s 无反馈即平」;以下用「整段从未浮盈到X%」做下界。")
    if losers:
        for thr in (0.0, 0.01, 0.02):
            scratched = [p for p in P if p["mfe"] <= thr]          # 会被「无反馈」规则平掉的全部仓
            saved = [p for p in scratched if p["net"] < 0]          # 其中本就是亏仓(scratch 救回)
            killed = [p for p in scratched if p["net"] > 0]         # 误杀:本会盈利
            print(f"  阈值 MFE≤{thr*100:.0f}%: 命中{len(scratched)}仓 | 救回亏仓{len(saved)}(亏额{sum(p['net'] for p in saved):+.3f}U) "
                  f"| 误杀盈仓{len(killed)}(让出{sum(p['net'] for p in killed):+.3f}U)")
        print("  注:'救回亏额'是最终亏损上界(早平实际只承担当时MAE,可能更少);误杀是真实代价。净收益≈救回−误杀,需时间切片才精确。")

    # 4b. 精确版(用时间切片,仅部署后样本):成交后 30s/60s MFE≤阈值即平 的反事实。
    ts = [p for p in P if p["has_ts"]]
    print(f"\n【4b. scratch 精确模拟(时间切片样本 n={len(ts)})】")
    if len(ts) < 5:
        print("  时间切片样本不足(<5),等部署后积累再看。")
    else:
        for win, key in (("30s", "mfe30"), ("60s", "mfe60")):
            for thr in (0.0, 0.01, 0.02):
                hit = [p for p in ts if p[key] <= thr]   # 该窗口内未涨过阈值 → 会被 scratch
                saved = [p for p in hit if p["net"] < 0]
                killed = [p for p in hit if p["net"] > 0]
                print(f"  {win} MFE≤{thr*100:.0f}%: 命中{len(hit)} | 救回亏仓{len(saved)}({sum(p['net'] for p in saved):+.3f}U) "
                      f"| 误杀盈仓{len(killed)}({sum(p['net'] for p in killed):+.3f}U)")

    # ===== 附:出场原因 × 净额(事件级) =====
    print(f"\n【附. 出场原因 × 净额(事件级)】")
    by = collections.defaultdict(lambda: [0, 0.0])
    for r in ins:
        k = r.get("reason", "?").split("(")[0]
        by[k][0] += 1
        by[k][1] += r.get("realized_usdt", 0.0)
    for k, (c, nu) in sorted(by.items(), key=lambda x: x[1][1]):
        print(f"  {k:<18} n={c:<3} net={nu:+.3f}U")

    # ===== 5. 开仓微结构 × 仓位级 PF(阶段2A;仅 has_micro 样本)=====
    micros = [p for p in P if p["has_micro"]]
    print(f"\n【5. 开仓微结构 × PF(has_micro 样本 n={len(micros)})】")
    if len(micros) < 5:
        print("  微结构样本不足(<5),等埋点开启并积累后重跑。")
    else:
        def bucket_pf(label, keyfn, edges, fmt):
            print(f"  — 按 {label} 分桶:")
            groups = collections.defaultdict(list)
            for p in micros:
                v = keyfn(p)
                if v is None:
                    continue
                b = "?"
                for lo, hi in edges:
                    if (lo is None or v >= lo) and (hi is None or v < hi):
                        b = fmt(lo, hi)
                        break
                groups[b].append(p["net"])
            for b in groups:
                nets = groups[b]
                w = sum(1 for x in nets if x > 0)
                print(f"      {b:<14} n={len(nets):<3} PF={pf(nets):.2f} 胜率={w/len(nets)*100:.0f}% 净={sum(nets):+.3f}U")

        bucket_pf("open_spread_bps", lambda p: p["spread"],
                  [(None, 2), (2, 5), (5, 10), (10, None)], lambda lo, hi: f"spread{lo or 0}-{hi or '+'}bps")
        bucket_pf("depth_cover_ratio_10bps", lambda p: p["depthcov"],
                  [(None, 2), (2, 5), (5, 20), (20, None)], lambda lo, hi: f"cover{lo or 0}-{hi or '+'}x")
        bucket_pf("pre_open_runup_bps(追末端)", lambda p: p["runup"],
                  [(None, 0), (0, 10), (10, 30), (30, None)], lambda lo, hi: f"runup{lo if lo is not None else '-'}-{hi or '+'}bps")
        bucket_pf("maker_rounds_used", lambda p: p["maker_rounds"],
                  [(None, 1), (1, 2), (2, 3), (3, None)], lambda lo, hi: f"rounds{lo or 0}-{hi or '+'}")
        # maker_rejected / open_taker 二分桶
        for label, sel in [("maker_rejected=True", lambda p: p["maker_rejected"]),
                           ("maker_rejected=False", lambda p: not p["maker_rejected"]),
                           ("open_via_taker=True", lambda p: p["open_taker"]),
                           ("open_via_taker=False", lambda p: not p["open_taker"])]:
            sub = [p["net"] for p in micros if sel(p)]
            if sub:
                w = sum(1 for x in sub if x > 0)
                print(f"  {label:<22} n={len(sub):<3} PF={pf(sub):.2f} 胜率={w/len(sub)*100:.0f}%")

    # ===== 6. open_decision_events:被拒/未成交 vs 成交 候选差异 =====
    evs = []
    for v in VENUES:
        for f in (f"records/{v}_directional/open_decision_events.jsonl", f"{v}_directional/open_decision_events.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if line:
                        try:
                            evs.append(json.loads(line))
                        except json.JSONDecodeError:
                            pass
                break
    print(f"\n【6. open_decision_events(候选决策,n={len(evs)})】")
    if not evs:
        print("  无决策事件(埋点未开启或尚无候选)。")
    else:
        byres = collections.Counter(e.get("decision_result", "?") for e in evs)
        print("  决策结局分布:", dict(byres))
        def micro_summary(sel, name):
            g = [e for e in evs if sel(e)]
            if not g:
                return
            sp = [e.get("open_spread_bps", 0) for e in g if e.get("open_spread_bps")]
            cv = [e.get("depth_cover_ratio_10bps", 0) for e in g if e.get("depth_cover_ratio_10bps")]
            ru = [e.get("pre_open_runup_bps", 0) for e in g if "pre_open_runup_bps" in e]
            print(f"  {name:<16} n={len(g):<3} spread中位={pctile(sp,50):.1f}bps cover中位={pctile(cv,50):.1f}x runup中位={pctile(ru,50):.1f}bps")
        micro_summary(lambda e: e.get("decision_result") in ("opened", "taker_filled"), "成交候选")
        micro_summary(lambda e: e.get("decision_result") == "maker_not_filled", "maker未成交")
        micro_summary(lambda e: e.get("decision_result") == "rejected", "被闸拒绝")
        print("  判读:对比成交 vs 被拒/未成交 的 spread/cover/runup,看哪个维度区分好坏入场。")

    # ===== 7. 时间维度(北京时间 hour/session × 4 窗口)=====
    time_dimension_section(rows)

    # ===== 10. 阶段窗口切分(A/B/C/D × 档位 × night×venue)=====
    stage_dimension_section(rows)

    # ===== 12. regime_degrade_candidate 命中标记(record-only 派生;按 phase/cap/window)=====
    regime_marker_section(rows)

    # ===== 8. v2 环境因子分桶(OpenMicro v2 部署后才有 env)=====
    envp = [p for p in P if isinstance(p.get("env"), dict)]
    print(f"\n【8. 环境因子 × 仓位级PF(env 样本 n={len(envp)})】")
    if len(envp) < 5:
        print("  env 样本不足(<5):OpenMicro v2 未部署或尚无样本。部署后自动填充。")
    else:
        def env_cat_pf(label, keyfn):
            groups = collections.defaultdict(list)
            for p in envp:
                k = keyfn(p["env"])
                if k is None:
                    continue
                groups[str(k)].append(p["net"])
            print(f"  — {label}:")
            for k in sorted(groups):
                nets = groups[k]
                w = sum(1 for x in nets if x > 0)
                flag = "" if len(nets) >= 30 else " ⚠不足"
                print(f"      {k:<16} n={len(nets):<3} PF={pf(nets):.2f} 胜率={w/len(nets)*100:.0f}% 净={sum(nets):+.3f}U{flag}")

        def env_num_bucket(label, key, edges, fmt):
            def kf(env):
                v = env.get(key)
                if v is None:
                    return None
                for lo, hi in edges:
                    if (lo is None or v >= lo) and (hi is None or v < hi):
                        return fmt(lo, hi)
                return "?"
            env_cat_pf(label, kf)

        env_cat_pf("candidate_market_mode(BTC单边/震荡)", lambda e: e.get("candidate_market_mode"))
        env_cat_pf("entry_side_vs_btc", lambda e: e.get("entry_side_vs_btc"))
        env_cat_pf("funding_bucket", lambda e: e.get("funding_bucket"))
        env_cat_pf("range_break_state", lambda e: e.get("range_break_state"))
        env_num_bucket("btc_trend_strength", "btc_trend_strength",
                       [(None, 0.3), (0.3, 0.5), (0.5, None)], lambda lo, hi: f"trend{lo or 0}-{hi or '+'}")
        env_num_bucket("atr_pct_rank", "atr_pct_rank",
                       [(None, 0.33), (0.33, 0.66), (0.66, None)], lambda lo, hi: f"atrRank{lo or 0}-{hi or '+'}")
        env_num_bucket("same_side_position_count(拥挤度)", "same_side_position_count",
                       [(None, 3), (3, 6), (6, None)], lambda lo, hi: f"same{lo or 0}-{hi or '+'}")
        # spread/depth/runup × BTC mode 交叉
        print("  — spread中位 × BTC mode:")
        bym = collections.defaultdict(list)
        for p in envp:
            if p.get("spread") is not None:
                bym[p["env"].get("candidate_market_mode", "?")].append((p["spread"], p["net"]))
        for m in sorted(bym):
            sps = [x[0] for x in bym[m]]
            nets = [x[1] for x in bym[m]]
            print(f"      {m:<14} n={len(sps):<3} spread中位={pctile(sps,50):.1f}bps PF={pf(nets):.2f}")

    # 8b. 决策事件 env:成交 vs 被拒/未成交 的环境差异(治幸存者偏差;被拒候选无 PnL,故比分布不比 PF)
    devs = []
    for v in VENUES:
        for f in (f"records/{v}_directional/open_decision_events.jsonl", f"{v}_directional/open_decision_events.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if line:
                        try:
                            devs.append(json.loads(line))
                        except json.JSONDecodeError:
                            pass
                break
    deve = [e for e in devs if isinstance(e.get("env"), dict)]
    print(f"\n  [8b. 决策事件 env:成交 vs 被拒/未成交 环境差异] n={len(deve)}")
    if len(deve) < 5:
        print("    决策事件 env 样本不足(<5):部署 OpenMicro v2 后积累。")
    else:
        def grp(e):
            r = e.get("decision_result")
            if r in ("opened", "taker_filled"):
                return "成交"
            if r == "maker_not_filled":
                return "maker未成交"
            if r == "rejected":
                return "被拒"
            return r or "?"
        groups = collections.defaultdict(list)
        for e in deve:
            groups[grp(e)].append(e["env"])
        numeric = ["btc_trend_strength", "candidate_breadth_5m", "candidate_dispersion",
                   "funding_rank_pct", "atr_pct_rank", "recent_vol_expansion", "same_side_position_count"]
        for g in ["成交", "maker未成交", "被拒"]:
            es = groups.get(g)
            if not es:
                continue
            parts = []
            for k in numeric:
                vals = [x.get(k) for x in es if x.get(k) is not None]
                if vals:
                    parts.append(f"{k}={pctile(vals,50):.2f}")
            cats = collections.Counter(x.get("candidate_market_mode", "?") for x in es)
            print(f"    {g:<11} n={len(es):<4} 中位: " + " ".join(parts))
            print(f"                market_mode={dict(cats)} entry_vs_btc={dict(collections.Counter(x.get('entry_side_vs_btc','?') for x in es))}")
        print("    判读:若被拒/未成交候选的 env(broad单边/funding extreme/拥挤度高/breadth极端)系统性不同于成交,即阶段2过滤候选。")

    # ===== 9. 环境因子六类归因(结构化:每类回答 4 问)=====
    print("\n【9. 环境因子六类归因(每类:Q1盈/亏区分 · Q2成交/被拒区分 · Q3样本 · Q4潜在动作)】")
    print("  类1 时间因子见第7节(hour/session、time×venue、time×maker-taker、time×硬止损)。以下为类2-6。")
    win9 = [p for p in envp if p["net"] > 0]
    loss9 = [p for p in envp if p["net"] < 0]
    filled9 = [e for e in deve if e.get("decision_result") in ("opened", "taker_filled")]
    rej9 = [e for e in deve if e.get("decision_result") == "rejected"]

    def m9(items, getfn):
        xs = [getfn(x) for x in items]
        xs = [v for v in xs if isinstance(v, (int, float)) and not isinstance(v, bool)]
        return round(pctile(xs, 50), 3) if xs else None

    def d9(items, getfn):
        return dict(collections.Counter(getfn(x) for x in items if getfn(x) is not None))

    def report_cat(title, fields, action_hint):
        print(f"\n  ── 类 {title} ──")
        for label, kind, pget, eget in fields:
            if kind == "num":
                print(f"    {label:<26} Q1 盈={m9(win9,pget)} 亏={m9(loss9,pget)} | Q2 成交={m9(filled9,eget)} 被拒={m9(rej9,eget)}")
            else:
                print(f"    {label:<26} Q1 盈={d9(win9,pget)} 亏={d9(loss9,pget)} | Q2 成交={d9(filled9,eget)} 被拒={d9(rej9,eget)}")
        wl_ok = len(win9) >= 10 and len(loss9) >= 10
        fr_ok = len(filled9) >= 10
        suff = "(样本足)" if (wl_ok and fr_ok) else "(样本不足→Q4仅继续观察)"
        print(f"    → Q3 样本: 盈{len(win9)}/亏{len(loss9)}仓, 成交{len(filled9)}/被拒{len(rej9)}事件 {suff}")
        print(f"    → Q4 潜在动作(仅当样本≥30且稳健才考虑,当前不动): {action_hint}")

    envget = lambda k: (lambda x: (x.get("env") or {}).get(k))
    report_cat("2 BTC/大盘", [
        ("btc_trend_strength", "num", envget("btc_trend_strength"), envget("btc_trend_strength")),
        ("btc_impulse", "num", envget("btc_impulse"), envget("btc_impulse")),
        ("entry_side_vs_btc", "cat", envget("entry_side_vs_btc"), envget("entry_side_vs_btc")),
        ("symbol_btc_corr_30m", "num", envget("symbol_btc_corr_30m"), envget("symbol_btc_corr_30m")),
        ("symbol_btc_beta_30m", "num", envget("symbol_btc_beta_30m"), envget("symbol_btc_beta_30m")),
        ("candidate_market_mode", "cat", envget("candidate_market_mode"), envget("candidate_market_mode")),
        ("candidate_breadth_5m", "num", envget("candidate_breadth_5m"), envget("candidate_breadth_5m")),
        ("candidate_dispersion", "num", envget("candidate_dispersion"), envget("candidate_dispersion")),
    ], "BTC单边/高corr/broad单边 → 降档(降并发·降名义),不停机")
    report_cat("3 Funding", [
        ("funding_bucket", "cat", envget("funding_bucket"), envget("funding_bucket")),
        ("funding_rank_pct", "num", envget("funding_rank_pct"), envget("funding_rank_pct")),
    ], "extreme funding 集中亏 → 提高门槛/降档;minutes_to_funding 待低成本接入(第二步)")
    report_cat("4 波动", [
        ("atr_pct_rank", "num", envget("atr_pct_rank"), envget("atr_pct_rank")),
        ("recent_vol_expansion", "num", envget("recent_vol_expansion"), envget("recent_vol_expansion")),
        ("spike_count_15m", "num", envget("spike_count_15m"), envget("spike_count_15m")),
        ("range_break_state", "cat", envget("range_break_state"), envget("range_break_state")),
    ], "高ATR/破区间/插针 → 提高门槛或降档")
    report_cat("5 盘口/执行", [
        ("spread_bps", "num", lambda p: p.get("spread"), lambda e: e.get("open_spread_bps")),
        ("depth_cover_10bps", "num", lambda p: p.get("depthcov"), lambda e: e.get("depth_cover_ratio_10bps")),
        ("pre_open_runup_bps", "num", lambda p: p.get("runup"), lambda e: e.get("pre_open_runup_bps")),
        ("maker_rounds", "num", lambda p: p.get("maker_rounds"), lambda e: e.get("maker_rounds_used")),
        ("maker_rejected", "cat", lambda p: p.get("maker_rejected"), lambda e: e.get("maker_rejected")),
        ("open_via_taker", "cat", lambda p: p.get("open_taker"), lambda e: e.get("open_via_taker")),
    ], "taker桶PF差 → 禁/限 taker;宽spread/薄depth → 提门槛")
    report_cat("6 拥挤", [
        ("same_side_position_count", "num", envget("same_side_position_count"), envget("same_side_position_count")),
        ("same_symbol_cross_venue_count", "num", envget("same_symbol_cross_venue_count"), envget("same_symbol_cross_venue_count")),
        ("btc_aligned_position_count", "num", envget("btc_aligned_position_count"), envget("btc_aligned_position_count")),
        ("total_directional_notional", "num", envget("total_directional_notional"), envget("total_directional_notional")),
    ], "同向/BTC对齐 拥挤高 → 降并发/降名义")

    # ===== 11. 不可交易预过滤 + fast-layer 影子快信号 =====
    print("\n【11. tradability 预过滤 + 影子快信号(record-only)】")
    # 11a. tradability 拒绝数(来自 open_decision_events stage=tradability)
    tcnt = collections.Counter()
    for v in VENUES:
        for f in (f"records/{v}_directional/open_decision_events.jsonl", f"{v}_directional/open_decision_events.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        e = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if e.get("decision_stage") == "tradability":
                        tcnt[v] += 1
                break
    print("  tradability 拒绝(不可交易币被预过滤):", dict(tcnt) if tcnt else "0(尚未产生或埋点未部署)")
    # 11b. 影子快信号触发计数
    sev = []
    for v in VENUES:
        for f in (f"records/{v}_directional/shadow_fast_signal_events.jsonl", f"{v}_directional/shadow_fast_signal_events.jsonl"):
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if line:
                        try:
                            sev.append(json.loads(line))
                        except json.JSONDecodeError:
                            pass
                break
    print(f"  影子事件总数={len(sev)}")
    if sev:
        sig_names = ["narrow_spread", "deep_book", "low_runup", "opposite_btc", "normal_range", "non_extreme_funding", "good_micro_combo"]
        print("  各影子信号触发率:")
        for s in sig_names:
            t = sum(1 for e in sev if (e.get("signals") or {}).get(s))
            print(f"    {s:<20} 触发={t}/{len(sev)} ({t/len(sev)*100:.0f}%)")
        # 11c. 前向收益(需网络;SHADOW_FWD=1 才跑)
        if os.environ.get("SHADOW_FWD"):
            import urllib.request, time as _t
            cache = {}
            def _candles(sym):
                if sym in cache:
                    return cache[sym]
                try:
                    req = urllib.request.Request("https://www.okx.com/api/v5/market/candles?instId=%s-SWAP&bar=1m&limit=300" % sym, headers={"User-Agent": "Mozilla/5.0"})
                    d = json.load(urllib.request.urlopen(req, timeout=15))["data"]
                    cache[sym] = sorted([(int(r[0]), float(r[4])) for r in d])
                except Exception:
                    cache[sym] = []
                _t.sleep(0.04)
                return cache[sym]
            def _ts(s):
                s = s.rstrip("Z"); s = s.split(".")[0] if "." in s else s
                try:
                    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
                except ValueError:
                    return None
            def _fwd(sym, ts, mins, side):
                arr = _candles(sym)
                if not arr or ts is None:
                    return None
                t0 = ts.timestamp() * 1000; entry = None; em = None
                for ms, c in arr:
                    if entry is None and ms >= t0:
                        entry = c; em = ms; continue
                    if entry is not None and ms >= em + mins * 60000:
                        r = (c - entry) / entry
                        return r if side == "long" else -r
                return None
            COST = 0.001
            print("  影子信号前向收益(signed,需网络;扣往返~0.1%价格;反事实非实盘):")
            for s in sig_names:
                g = [e for e in sev if (e.get("signals") or {}).get(s)]
                if not g:
                    continue
                cells = []
                for h in (1, 3, 5, 15):
                    xs = [_fwd(e.get("symbol"), _ts(e.get("recorded_at", "")), h, e.get("side")) for e in g]
                    xs = [x for x in xs if x is not None]
                    if xs:
                        m = sum(xs) / len(xs); pos = sum(1 for x in xs if x > 0) / len(xs) * 100
                        cells.append(f"+{h}m:{m*100:+.2f}%({pos:.0f}%)")
                x5 = [_fwd(e.get("symbol"), _ts(e.get("recorded_at", "")), 5, e.get("side")) for e in g]
                x5 = [x for x in x5 if x is not None]
                net5 = (sum(x5) / len(x5) - COST) * 100 if x5 else 0
                edge = "潜在edge" if net5 > 0.05 else ("中性" if net5 > -0.05 else "无edge")
                print(f"    {s:<20} n={len(g):<4} {' '.join(cells)} | +5m净={net5:+.3f}% {edge}")
        else:
            print("  (前向收益分析需网络:用 SHADOW_FWD=1 python3 records/_directional_feedback_audit.py 运行)")

    # 执行成本
    eslip = [r["entry_slip_bps"] for r in ins if "entry_slip_bps" in r]
    xslip = [r["exit_slip_bps"] for r in ins if "exit_slip_bps" in r]
    if eslip or xslip:
        print(f"\n【附. 执行成本(事件级)】 入场滑点中位={pctile(eslip,50):+.1f}bps p90={pctile(eslip,90):+.1f} | "
              f"出场滑点中位={pctile(xslip,50):+.1f}bps p90={pctile(xslip,90):+.1f}")


if __name__ == "__main__":
    main()
