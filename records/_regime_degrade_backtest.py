#!/usr/bin/env python3
# regime_degrade 降档区间回测(只读;反事实=用历史已平仓 env 找"亏损/硬止损主要集中区")。
# 1) 上线后 31 笔非命中拆 btc_trend/market_mode,看当前降档有没有漏掉主要亏损区;
# 2) 历史 env 已平仓数据按 btc_trend 桶(<0 / 0-0.1 / 0.1-0.2 / 0.2-0.4 / >0.4)+ market_mode 桶,
#    每桶: n / PF / 净 / hard_stop率 / 平均亏损 / 名义归一收益(sum(net)/sum(notional));
# 3) 判定: 当前 [0.1,0.2)+broad_up 是否覆盖主要亏损;若亏损主要在区外 → 当前配窄了。
import json, os
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
DEPLOY = datetime(2026, 6, 29, 18, 25, 0, tzinfo=timezone.utc)  # regime_degrade 上线(重启)


def ts(s):
    if not s:
        return None
    s = s.strip().rstrip("Z")
    if "+" in s[11:]:
        s = s[:11] + s[11:].split("+", 1)[0]
    if "." in s:
        h, f = s.split(".", 1); s = h + "." + "".join(c for c in f if c.isdigit())[:6]; fmt = "%Y-%m-%dT%H:%M:%S.%f"
    else:
        fmt = "%Y-%m-%dT%H:%M:%S"
    try:
        return datetime.strptime(s, fmt).replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def pf(xs):
    g = sum(x for x in xs if x > 0); l = sum(x for x in xs if x < 0)
    return (g / abs(l)) if l else (999.0 if g > 0 else 0.0)



def _is_fast_row(r):
    """fast 层结算行判定:fast_scalper_v1 的 position_id 带 fast- 前缀 / reason 带 [fast] 标记。
    慢层(90s 方向微鳞)研究必须剔除 fast 行,否则 3855 笔 fast 学费会淹没慢层信号(口径污染)。"""
    pid = str(r.get("position_id") or "")
    return pid.startswith("fast-") or "[fast]" in str(r.get("reason") or "") or str(r.get("strategy") or "") == "fast_scalper_v1"

def load_positions():
    """聚合到仓位级(按 position_id);带 env(btc_trend/market_mode)、net、hard_stop、notional、hit、平仓时间。"""
    agg = {}
    for v in VEN:
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
            if _is_fast_row(r):
                continue
            pid = r.get("position_id") or f'{r.get("symbol")}|{r.get("side")}|{r.get("entry",0):.8f}'
            p = agg.get(pid)
            if p is None:
                p = {"net": 0.0, "hard": False, "notional": 0.0, "hit": False,
                     "btc": None, "mode": None, "closed": ts(r.get("time")), "v": v, "reason_rec": None}
                agg[pid] = p
            p["net"] += r.get("realized_usdt", 0.0)
            if "硬止损" in r.get("reason", ""):
                p["hard"] = True
            p["notional"] = max(p["notional"], r.get("notional", 0.0))
            if r.get("regime_degrade_hit"):
                p["hit"] = True
            if r.get("regime_degrade_reason") and p["reason_rec"] is None:
                p["reason_rec"] = r.get("regime_degrade_reason")
            om = r.get("open_micro")
            if isinstance(om, dict) and isinstance(om.get("env"), dict):
                e = om["env"]
                if p["btc"] is None:
                    p["btc"] = e.get("btc_trend_strength")
                if p["mode"] is None:
                    p["mode"] = e.get("candidate_market_mode")
    return list(agg.values())


def row(label, g):
    if not g:
        print(f"    {label:<16} n=0"); return
    nets = [p["net"] for p in g]
    loss = [x for x in nets if x < 0]
    hs = sum(1 for p in g if p["hard"]) / len(g) * 100
    sumN = sum(p["notional"] for p in g) or 1
    norm = sum(nets) / sumN * 100  # 名义归一收益%(总净/总名义)
    avgloss = (sum(loss) / len(loss)) if loss else 0.0
    flag = "" if len(g) >= 30 else (" ⚠<30" if len(g) >= 10 else " ⚠极少")
    print(f"    {label:<16} n={len(g):<4} PF={pf(nets):<5.2f} 净={sum(nets):+7.3f}U 硬止损={hs:>3.0f}% 均亏={avgloss:+.4f}U 名义归一={norm:+.3f}%{flag}")


def reason_of(btc, mode):
    """复刻引擎离散 OR(优先级 transition_band>extreme_trend>broad_up);未命中返回 None。"""
    if btc is not None and 0.1 <= btc < 0.2:
        return "transition_band"
    if btc is not None and btc >= 0.4:
        return "extreme_trend"
    if mode == "broad_up":
        return "broad_up"
    return None


REASON_ORDER = ["transition_band", "extreme_trend", "broad_up"]


def btc_bucket(b):
    if b is None:
        return "(无env)"
    if b < 0:
        return "<0"
    if b < 0.1:
        return "0-0.1"
    if b < 0.2:
        return "0.1-0.2"
    if b < 0.4:
        return "0.2-0.4"
    return ">0.4"


BTC_ORDER = ["<0", "0-0.1", "0.1-0.2", "0.2-0.4", ">0.4", "(无env)"]
MODE_ORDER = ["broad_up", "broad_down", "mixed", "quiet", "range", "(无)"]


def main():
    P = load_positions()
    envp = [p for p in P if p["btc"] is not None]
    print("=" * 92)
    print("regime_degrade 降档区间回测(只读 / 历史已平仓 env;反事实找亏损集中区)")
    print(f"  全部已平仓={len(P)}  含env={len(envp)}  当前命中条件: btc_trend∈[0.1,0.2) 或 mode==broad_up")
    print("=" * 92)

    # ---- 任务1: 上线后 31 笔(非命中)拆解 ----
    post = [p for p in P if p["closed"] and p["closed"] >= DEPLOY]
    postnon = [p for p in post if not p["hit"]]
    posthit = [p for p in post if p["hit"]]
    print(f"\n【1. regime_degrade 上线后已平仓 {len(post)}(命中{len(posthit)}/非命中{len(postnon)})——看当前降档是否漏掉主要亏损区】")
    print("  非命中(7.5U)按 btc_trend 桶:")
    for b in BTC_ORDER:
        row(b, [p for p in postnon if btc_bucket(p["btc"]) == b])
    print("  非命中(7.5U)按 market_mode:")
    for m in MODE_ORDER:
        row(m, [p for p in postnon if (p["mode"] or "(无)") == m])

    # ---- 任务2: 历史 env 全样本回测 ----
    print(f"\n【2. 历史 env 已平仓回测(n={len(envp)})——候选降档区间扫描】")
    print("  [按 btc_trend_strength 桶]  (注: 该字段=效率比[0,1],<0 桶通常为空)")
    for b in BTC_ORDER:
        row(b, [p for p in envp if btc_bucket(p["btc"]) == b])
    print("  [按 market_mode]")
    for m in MODE_ORDER:
        row(m, [p for p in envp if (p["mode"] or "(无)") == m])
    print("  [新离散OR 按命中原因(派生历史;预期每个 reason 表现)]")
    for rs in REASON_ORDER:
        row(rs, [p for p in envp if reason_of(p["btc"], p["mode"]) == rs])
    row("未命中(区外)", [p for p in envp if reason_of(p["btc"], p["mode"]) is None])
    print("  [当前命中条件 vs 区外]")
    def is_cur(p):
        return reason_of(p["btc"], p["mode"]) is not None
    row("当前命中区(新OR)", [p for p in envp if is_cur(p)])
    row("当前区外", [p for p in envp if not is_cur(p)])

    # ---- 实盘 recorded reason(上线后,随样本累积)----
    print(f"\n【4. 实盘 recorded reason 对照(regime_degrade 上线后已平仓;hit 仓带 reason)】")
    rec = {}
    for p in post:
        rr = p.get("reason_rec")
        if p["hit"]:
            rec.setdefault(rr or "(hit无reason)", []).append(p)
    if not rec:
        print("    (上线后暂无命中平仓;随样本累积,这里按 transition_band/extreme_trend/broad_up 分别出 n/PF/硬止损/归一)")
    else:
        for rr, g in sorted(rec.items()):
            row(rr, g)
    row("上线后非命中(7.5U)", [p for p in post if not p["hit"]])

    # ---- 任务3: 判定 ----
    print("\n【3. 判定:当前 regime_degrade 是否配窄了】")
    tot_loss = sum(p["net"] for p in envp if p["net"] < 0)
    cur_loss = sum(p["net"] for p in envp if p["net"] < 0 and is_cur(p))
    out_loss = tot_loss - cur_loss
    cur_hard = sum(1 for p in envp if p["hard"] and is_cur(p))
    tot_hard = sum(1 for p in envp if p["hard"])
    print(f"  总亏损额={tot_loss:+.2f}U | 当前命中区贡献={cur_loss:+.2f}U ({cur_loss/tot_loss*100 if tot_loss else 0:.0f}%) | 区外={out_loss:+.2f}U ({out_loss/tot_loss*100 if tot_loss else 0:.0f}%)")
    print(f"  硬止损总数={tot_hard} | 当前命中区={cur_hard} ({cur_hard/tot_hard*100 if tot_hard else 0:.0f}%) | 区外={tot_hard-cur_hard} ({(tot_hard-cur_hard)/tot_hard*100 if tot_hard else 0:.0f}%)")
    print("  → 若区外亏损占比高(如 >50%),说明当前 [0.1,0.2)+broad_up 配窄、漏掉主要亏损区(供人决策是否扩窗,不自动改)。")


if __name__ == "__main__":
    main()
