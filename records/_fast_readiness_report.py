#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fast 实盘线(fast_scalper_v1)重启就绪报告:三道闸一键 GO/NO-GO。

背景:头两天 fast 实盘 3855 笔、学费 -30.3U,证明现成本结构下无正期望,已停
(config.yaml fast_scalper_v1_enabled: false)。重启必须同时过三道闸:

  闸1 成本:返佣落地(fee_rebate.json okx_rebate_share>0),往返 maker 成本 < 4bp;
  闸2 信号:结算瞬间方向性研究样本 n≥300 且 fwd_60s aligned mean > 2×往返成本、t>2;
  闸3 模拟:maker-maker 影子模拟(_fast_maker_maker_sim.py 口径)净期望为正。

用法:python3 _fast_readiness_report.py   (只读,输出三闸状态与 GO/NO-GO)
"""
import json
import math
import os
import time

HERE = os.path.dirname(os.path.abspath(__file__))


def gate1_cost():
    rebate = {}
    try:
        with open(os.path.join(HERE, "fee_rebate.json")) as f:
            rebate = json.load(f)
    except Exception:
        pass
    share = float(rebate.get("okx_rebate_share") or 0)
    maker = 0.0005  # OKX 现档 maker 5bp(feeprobe 口径;有实测则更好)
    try:
        with open(os.path.join(HERE, "fee_discount_audit.jsonl")) as f:
            for line in f:
                try:
                    d = json.loads(line)
                except Exception:
                    continue
                if (d.get("venue") or "").lower() == "okx" and d.get("maker_fee") is not None:
                    maker = abs(float(d["maker_fee"]))
        # 注意符号约定:OKX API 负数=收费(实扣),此处取绝对值作成本。
    except Exception:
        pass
    eff = maker * (1 - share)
    rt_bps = 2 * eff * 1e4
    ok = share > 0 and rt_bps < 4.0
    detail = "返佣 share=%.0f%% 有效 maker=%.2fbp 往返=%.2fbp(要求:share>0 且往返<4bp)" % (share * 100, eff * 1e4, rt_bps)
    return ok, rt_bps, detail


def gate2_signal(rt_bps):
    rows = []
    p = os.path.join(HERE, "funding_moment_events.jsonl")
    if os.path.exists(p):
        with open(p) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        rows.append(json.loads(line))
                    except Exception:
                        pass
    aligned = [-math.copysign(1, r["rate"]) * r["fwd_60s_bps"] for r in rows if "fwd_60s_bps" in r and r.get("rate")]
    n = len(aligned)
    if n == 0:
        return False, "n=0(等 lingxi-funding-study.timer 积累,每日4班)"
    mean = sum(aligned) / n
    sd = math.sqrt(sum((v - mean) ** 2 for v in aligned) / n) if n > 1 else 0
    t = mean / (sd / math.sqrt(n)) if sd > 0 else 0
    need = 2 * rt_bps
    ok = n >= 300 and mean > need and t > 2
    return ok, "n=%d mean=%+.2fbp t=%.2f(要求:n≥300 且 mean>2×往返=%.1fbp 且 t>2)" % (n, mean, t, need)


def gate3_sim():
    # 影子模拟结论文件由 _fast_maker_maker_sim.py 手动跑后落盘;无文件=未跑。
    p = os.path.join(HERE, "fast_mm_sim_summary.json")
    try:
        with open(p) as f:
            d = json.load(f)
        net = float(d.get("net_expectation_bps") or d.get("net_bps"))
        return net > 0, "净期望 %+.2fbp/笔(%s)" % (net, d.get("generated_at", "?"))
    except Exception:
        return False, "未找到 fast_mm_sim_summary.json(返佣落地后跑 _fast_maker_maker_sim.py 生成)"


def main():
    print("===== fast 实盘线重启就绪报告(%s UTC)=====" % time.strftime("%F %T", time.gmtime()))
    g1, rt_bps, d1 = gate1_cost()
    g2, d2 = gate2_signal(rt_bps if g1 else 10.0)
    g3, d3 = gate3_sim()
    for name, ok, detail in (("闸1 成本(返佣落地)", g1, d1), ("闸2 信号(结算瞬间)", g2, d2), ("闸3 模拟(影子净正)", g3, d3)):
        print("[%s] %s: %s" % ("PASS" if ok else "FAIL", name, detail))
    if g1 and g2 and g3:
        print("\n>>> GO:三闸全过。重启步骤:config.yaml fast_scalper_v1_enabled: true(灰度 venue=okx,名义≤5U)→ 部署重启 → 首日只看不加仓。")
    else:
        print("\n>>> NO-GO:任一闸未过前不要重启 fast 实盘(头两天 -30.3U 学费已经交过了)。")


if __name__ == "__main__":
    main()
