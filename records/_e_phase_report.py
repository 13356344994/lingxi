#!/usr/bin/env python3
# E 阶段真实成交仓归因(只读)。稳健开仓时间口径 = position_id UnixNano后缀 或 平仓time-hold。
# 核对 GPT 三条 2B 依据(runup / same_side / btc_trend·market_mode) + 硬止损 headline + maker/taker。
import json, os, collections
from datetime import datetime, timezone

# phase E = taker_fallback_venues="binance" 灰度部署 = 2026-06-28 23:29:31 BJ = 2026-06-28 15:29:31 UTC
# (实证: 该时刻后 okx/bybit 再无 taker 开仓、仅 binance 保留 taker;E n≈208)
E_START = datetime(2026, 6, 28, 15, 29, 31, tzinfo=timezone.utc)      # phase E (taker_fallback 灰度)
# analysis_window(≠phase E): 50U 切换后的当前版本宽窗口
POST50_CUT = datetime(2026, 6, 28, 11, 25, 0, tzinfo=timezone.utc)   # analysis_window: post_50U_window
VEN = ["okx", "bybit", "binance"]


def regime_degrade_candidate(env):
    """C 候选 record-only 命中标记(纯派生,只读,不改任何交易行为)。
    命中 = BTC 过渡/震荡带 (0.1<=btc_trend_strength<0.2) 或 market_mode==broad_up。"""
    if not isinstance(env, dict):
        return False, None
    bt = env.get("btc_trend_strength")
    mm = env.get("candidate_market_mode")
    if bt is not None and 0.1 <= bt < 0.2:
        return True, "btc_trend_0.1-0.2"
    if mm == "broad_up":
        return True, "broad_up"
    return False, None


def parse_ts(s):
    if not s:
        return None
    s = s.strip().rstrip("Z")
    if "+" in s[11:]:
        s = s[:11] + s[11:].split("+", 1)[0]
    if "." in s:
        h, f = s.split(".", 1)
        s = h + "." + "".join(c for c in f if c.isdigit())[:6]
        fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    try:
        return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def open_time(r):
    pid = r.get("position_id", "")
    if pid and "-" in pid:
        t = pid.rsplit("-", 1)[-1]
        if t.isdigit() and len(t) >= 18:
            try:
                return datetime.fromtimestamp(int(t) / 1e9, tz=timezone.utc)
            except (ValueError, OSError):
                pass
    ct = parse_ts(r.get("time"))
    if ct is None:
        return None
    hold = r.get("hold_seconds")
    if hold is None:
        hold = r.get("hold_minutes", 0) * 60
    return ct - __import__("datetime").timedelta(seconds=hold)


def load_e(cut=E_START):
    agg = {}
    for v in VEN:
        f = f"records/{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for line in open(f):
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except json.JSONDecodeError:
                continue
            pid = r.get("position_id") or f'{r.get("symbol")}|{r.get("side")}|{r.get("entry",0):.8f}'
            p = agg.get(pid)
            if p is None:
                p = {"v": v, "sym": r.get("symbol"), "net": 0.0, "ot": open_time(r),
                     "taker": False, "hard": False, "reason": "", "env": None,
                     "spread": None, "depth": None, "runup": None, "mfe": 0.0, "mae": 0.0}
                agg[pid] = p
            p["net"] += r.get("realized_usdt", 0.0)
            p["taker"] = p["taker"] or bool(r.get("open_via_taker"))
            if "硬止损" in r.get("reason", ""):
                p["hard"] = True
            if r.get("reason"):
                p["reason"] = r.get("reason")
            p["mfe"] = max(p["mfe"], r.get("mfe_account_pct", 0.0))
            p["mae"] = min(p["mae"], r.get("mae_account_pct", 0.0))
            om = r.get("open_micro")
            if isinstance(om, dict):
                if p["spread"] is None:
                    p["spread"] = om.get("spread_bps")
                    p["depth"] = om.get("depth_cover_ratio_10bps")
                    p["runup"] = om.get("pre_open_runup_bps")
                if isinstance(om.get("env"), dict) and p["env"] is None:
                    p["env"] = om["env"]
    return [p for p in agg.values() if p["ot"] and p["ot"] >= cut]


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)


def line(label, g):
    if not g:
        print(f"    {label:<20} n=0"); return
    nets = [p["net"] for p in g]
    hard = sum(1 for p in g if p["hard"]) / len(g) * 100
    win = sum(1 for x in nets if x > 0) / len(g) * 100
    flag = "" if len(g) >= 30 else "  ⚠<30"
    print(f"    {label:<20} n={len(g):<3} PF={pf(nets):.2f} 净={sum(nets):+.2f}U 胜率={win:.0f}% 硬止损={hard:.0f}%{flag}")


def decision_hit_counts():
    """决策级(open_decision_events,含 rejected)regime_degrade_candidate 命中率,post_50U_window。"""
    hit = collections.Counter()
    tot = 0
    for v in VEN:
        f = f"records/{v}_directional/open_decision_events.jsonl"
        if not os.path.exists(f):
            f = f"{v}_directional/open_decision_events.jsonl"
        if not os.path.exists(f):
            continue
        for line in open(f):
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except json.JSONDecodeError:
                continue
            rt = parse_ts(e.get("recorded_at"))
            if not (rt and rt >= POST50_CUT):
                continue
            tot += 1
            ok, why = regime_degrade_candidate(e.get("env"))
            if ok:
                hit[why] += 1
    return tot, hit


def regime_marker_section(P):
    """C 候选 record-only 命中标记统计(成交仓级)。纯只读、不改交易。"""
    print("\n=== [C record-only 命中标记] regime_degrade_candidate(纯派生/不改交易) ===")
    print("  规则: (0.1<=btc_trend<0.2) 或 market_mode==broad_up")
    cand = [p for p in P if regime_degrade_candidate(p["env"])[0]]
    non = [p for p in P if p["env"] is not None and not regime_degrade_candidate(p["env"])[0]]
    line("命中(candidate)", cand)
    line("非命中(env齐)", non)
    sub = collections.Counter()
    for p in cand:
        sub[regime_degrade_candidate(p["env"])[1]] += 1
    print("  命中细分(成交仓):", dict(sub))
    tot, dhit = decision_hit_counts()
    print(f"  决策级(含rejected,post_50U_window) 候选总数={tot} 命中={sum(dhit.values())} 细分={dict(dhit)}")


def main():
    P50 = load_e(POST50_CUT)
    print(f"=== analysis_window=post_50U_window (平仓>=6-28 19:25BJ) n={len(P50)} | phase=混合D/E capital=混合50U/15U ===")
    nets50 = [p["net"] for p in P50]
    print(f"总: PF={pf(nets50):.2f} 净={sum(nets50):+.2f}U 硬止损率={sum(1 for p in P50 if p['hard'])/len(P50)*100:.0f}%")
    h50 = [p for p in P50 if p["hard"]]; nh50 = [p for p in P50 if not p["hard"]]
    print(f"[headline] 硬止损 n={len(h50)} 净={sum(p['net'] for p in h50):+.2f}U | 非硬止损 n={len(nh50)} 净={sum(p['net'] for p in nh50):+.2f}U")
    regime_marker_section(P50)

    print("\n" + "=" * 60)
    E = load_e(E_START)
    print(f"=== phase=E (taker_fallback 灰度, 开仓>=6-29 15:29Z) 真实成交仓 n={len(E)} ===")
    nets = [p["net"] for p in E]
    print(f"总: PF={pf(nets):.2f} 净={sum(nets):+.2f}U 胜率={sum(1 for x in nets if x>0)/len(E)*100:.0f}% "
          f"硬止损率={sum(1 for p in E if p['hard'])/len(E)*100:.0f}%")
    hardp = [p for p in E if p["hard"]]; nonh = [p for p in E if not p["hard"]]
    print(f"\n[headline] 硬止损 n={len(hardp)} 净={sum(p['net'] for p in hardp):+.2f}U | "
          f"非硬止损 n={len(nonh)} 净={sum(p['net'] for p in nonh):+.2f}U")

    print("\n[maker/taker]")
    line("maker", [p for p in E if not p["taker"]])
    line("taker", [p for p in E if p["taker"]])

    print("\n[venue]")
    for v in VEN:
        line(v, [p for p in E if p["v"] == v])

    print("\n[A. runup = abs(pre_open_runup_bps)]")
    line("<2", [p for p in E if p["runup"] is not None and abs(p["runup"]) < 2])
    line("2-10", [p for p in E if p["runup"] is not None and 2 <= abs(p["runup"]) < 10])
    line(">=10", [p for p in E if p["runup"] is not None and abs(p["runup"]) >= 10])
    line("(无runup字段)", [p for p in E if p["runup"] is None])

    print("\n[B. same_side_position_count (env)]")
    def ss(p):
        return (p["env"] or {}).get("same_side_position_count")
    line("0-1", [p for p in E if ss(p) is not None and ss(p) <= 1])
    line("2", [p for p in E if ss(p) == 2])
    line(">=3", [p for p in E if ss(p) is not None and ss(p) >= 3])
    line("(无env)", [p for p in E if ss(p) is None])

    print("\n[C. btc_trend_strength + market_mode (env)]")
    def bt(p):
        return (p["env"] or {}).get("btc_trend_strength")
    line("btc<0.1", [p for p in E if bt(p) is not None and bt(p) < 0.1])
    line("btc0.1-0.2", [p for p in E if bt(p) is not None and 0.1 <= bt(p) < 0.2])
    line("btc>=0.2", [p for p in E if bt(p) is not None and bt(p) >= 0.2])
    def mm(p):
        return (p["env"] or {}).get("candidate_market_mode")
    for m in ["broad_up", "broad_down", "mixed", "range"]:
        line(f"mode={m}", [p for p in E if mm(p) == m])

    print("\n[capital_regime]")
    line("50U(okx/bybit)", [p for p in E if p["v"] in ("okx", "bybit")])
    line("15U(binance)", [p for p in E if p["v"] == "binance"])


if __name__ == "__main__":
    main()
