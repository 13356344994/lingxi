#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""费率姿态顾问:把「现在各所实际费率 + 可行的降费杠杆 + 对策略盈亏平衡点的影响」一次性算清。

数据源(全部本地,只读):
  - fee_discount_audit.jsonl   feeprobe 每日实测各所 maker/taker 费率(交易所 API 口径,负=返)
  - fee_rebate.json            人工维护的返佣比例(okx_rebate_share,谈成后填入)
  - {venue}_directional/settlements.jsonl  近期真实成交量(估算各杠杆的月度价值)

输出:各所当前有效费率 → 每个可行杠杆(返佣/OKB/VIP/子账户节点)的预期费率与月省金额 → 行动清单。
"""
import glob
import json
import os
import time

HERE = os.path.dirname(os.path.abspath(__file__))


def jread(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return default


def latest_fee_probe():
    """fee_discount_audit.jsonl 每行一所一档;取每 venue 最新一行。"""
    out = {}
    p = os.path.join(HERE, "fee_discount_audit.jsonl")
    if not os.path.exists(p):
        return out
    with open(p) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            v = d.get("venue") or d.get("exchange")
            if not v:
                continue
            d = dict(d)
            # OKX API 符号约定:负数=实扣费用(不是返佣)。统一取绝对值作成本口径。
            for k in ("maker_fee", "taker_fee", "maker", "taker"):
                if d.get(k) is not None:
                    try:
                        d[k] = abs(float(d[k]))
                    except (TypeError, ValueError):
                        pass
            out[v.lower()] = d
    return out


def recent_volume_by_venue(days=7.0):
    """近 N 天方向结算的名义成交量(开+平双边),按所汇总 → 折算月度。"""
    cutoff = time.time() - days * 86400
    vol = {}
    for d in glob.glob(os.path.join(HERE, "*_directional")):
        venue = os.path.basename(d).replace("_directional", "")
        p = os.path.join(d, "settlements.jsonl")
        if not os.path.exists(p):
            continue
        total = 0.0
        with open(p) as f:
            for line in f:
                try:
                    r = json.loads(line)
                except Exception:
                    continue
                ts = r.get("time", "")
                try:
                    t = time.mktime(time.strptime(ts[:19], "%Y-%m-%dT%H:%M:%S"))
                except Exception:
                    continue
                if t < cutoff:
                    continue
                total += 2.0 * float(r.get("notional") or r.get("notional_usdt") or 0)  # 开+平双边
        if total > 0:
            vol[venue] = total / days * 30.0  # 月度折算
    return vol


def fnum(d, *keys):
    for k in keys:
        if k in d and d[k] is not None:
            try:
                return float(d[k])
            except (TypeError, ValueError):
                pass
    return None


def main():
    probe = latest_fee_probe()
    rebate = jread(os.path.join(HERE, "fee_rebate.json"), {}) or {}
    share = float(rebate.get("okx_rebate_share") or 0)
    vol = recent_volume_by_venue()

    print("===== 费率姿态顾问(%s UTC)=====" % time.strftime("%F %T", time.gmtime()))
    print("\n-- 当前实测费率(feeprobe;负=返佣)与月度成交量折算 --")
    rows = []
    for venue in ("okx", "bybit", "binance", "gate"):
        d = probe.get(venue, {})
        mk = fnum(d, "maker_fee", "maker")
        tk = fnum(d, "taker_fee", "taker")
        mv = vol.get(venue, 0.0)
        rows.append((venue, mk, tk, mv))
        mk_s = "%.4f%%" % (mk * 100) if mk is not None else "未探测"
        tk_s = "%.4f%%" % (tk * 100) if tk is not None else "未探测"
        print("%-8s maker=%-9s taker=%-9s 月成交(近7天折算)≈%.0f U" % (venue, mk_s, tk_s, mv))

    okx = dict(probe.get("okx", {}))
    mk = fnum(okx, "maker_fee", "maker") or 0.0005
    tk = fnum(okx, "taker_fee", "taker") or 0.001
    mvol = vol.get("okx", 0.0)

    print("\n-- OKX 降费杠杆(按月成交 %.0f U 估算)--" % mvol)

    def line(name, new_mk, new_tk, note=""):
        # 微鳞策略以 maker 为主,粗算 85% maker / 15% taker。
        cur = mvol * (0.85 * mk + 0.15 * tk)
        new = mvol * (0.85 * new_mk + 0.15 * new_tk)
        print("%-26s maker %.4f%%→%.4f%% taker %.4f%%→%.4f%% 月费 %.2f→%.2f U(省 %.2f)%s" % (
            name, mk * 100, new_mk * 100, tk * 100, new_tk * 100, cur, new, cur - new, " " + note if note else ""))

    if share > 0:
        line("返佣 %d%%(已填入)" % round(share * 100), mk * (1 - share), tk * (1 - share), "← 已生效口径")
    else:
        line("返佣 45%(节点换绑后)", mk * 0.55, tk * 0.55, "← 用户在谈;谈成后填 fee_rebate.json")
    line("OKB 持仓折扣(~5%)", mk * 0.95, tk * 0.95, "需持 OKB,承担币价波动")
    line("VIP1(月 500 万 U)", 0.00045, 0.0009, "现月量差距大,短期不可达")

    eff_mk = mk * (1 - share) if share > 0 else mk
    print("\n-- 对策略的意义 --")
    print("方向微鳞单边 maker 有效费率 %.2fbp → 往返 %.2fbp;" % (eff_mk * 1e4, 2 * eff_mk * 1e4))
    print("开仓成本地板(cost_floor_bps=40bp 口径)中费用占比 %.0f%%。" % (2 * eff_mk * 1e4 / 40 * 100))
    if share <= 0:
        print("最大单一杠杆仍是返佣:45%% 返佣把往返费用 %.1fbp→%.1fbp,等效每笔多 %.1fbp 期望。" % (
            2 * mk * 1e4, 2 * mk * 0.55 * 1e4, 2 * mk * 0.45 * 1e4))
    print("\n-- 行动清单 --")
    print("1. [用户在办] OKX 节点返佣 45%:谈成→填 records/fee_rebate.json→跑本脚本+_fast_readiness_report.py 复核")
    print("2. [自动] feeprobe 每日 08:35 UTC 实测费率落 fee_discount_audit.jsonl(lingxi-feeprobe.timer)")
    print("3. [观察] 返佣落地后重估 fast 线重启三闸(_fast_readiness_report.py 一键 GO/NO-GO)")


if __name__ == "__main__":
    main()
