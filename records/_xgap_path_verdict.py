#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""㊳① E 路径信息集阅卷人(预注册口径,2026-07-08 02:00 BJ 登记)。

判决集 = particle_ledger.jsonl 中 src=rest 且 path_ok=true 且 t0 >= 封样点 的行
(Go 账本 rest 可执行口径 = ㊱铁律的判决货币;Python 只阅卷不重算)。

预注册分桶(登记在先,不许事后加桶):
  生龄三分位(判决集内动态三分位,报告标注切点) × 速度符号(vel15>+1 收敛 / <-1 扩大 / 其余横住)
  × 归因主导(|okxRet15|>=|anchorRet15| → okx驱动,否则锚驱动)。
门:任一桶 n>=30 且 净中位>0 且 净PF>=1.3 → 报"候选粒子",走冠军梯子(不直接上枪)。
总样本 n>=100 才出正式读数;不足只报进度。
"""
import json
import os
import sys

SEAL = "2026-07-07T17:50:00"  # 封样=路径字段部署时刻(此前无该字段,天然封样)
LEDGER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "okx_directional", "particle_ledger.jsonl")
MIN_TOTAL = 100
MIN_CELL = 30


def median(xs):
    xs = sorted(xs)
    n = len(xs)
    if n == 0:
        return 0.0
    return xs[n // 2] if n % 2 else (xs[n // 2 - 1] + xs[n // 2]) / 2.0


def pf(xs):
    gp = sum(x for x in xs if x > 0)
    gl = -sum(x for x in xs if x < 0)
    return gp / gl if gl > 0 else float("inf")


def main():
    rows = []
    try:
        with open(LEDGER) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    r = json.loads(line)
                except ValueError:
                    continue
                if r.get("src") != "rest" or not r.get("path_ok"):
                    continue
                if (r.get("t0") or "") < SEAL:
                    continue
                if r.get("exit") == "unfilled":
                    continue
                rows.append(r)
    except IOError as e:
        print("账本读取失败: %s" % e)
        return 1

    print("路径阅卷 判决集 since %s | rest+path_ok n=%d" % (SEAL, len(rows)))
    if len(rows) < MIN_TOTAL:
        print("进度 %d/%d(不足,只报进度)" % (len(rows), MIN_TOTAL))
        return 0

    ages = sorted(r["gap_age_sec"] for r in rows)
    t1, t2 = ages[len(ages) // 3], ages[2 * len(ages) // 3]
    print("生龄三分位切点: %.0fs / %.0fs" % (t1, t2))

    def age_bucket(r):
        a = r["gap_age_sec"]
        return "龄低" if a <= t1 else ("龄中" if a <= t2 else "龄高")

    def vel_bucket(r):
        v = r.get("gap_vel15_bps", 0.0)
        return "收敛" if v > 1 else ("扩大" if v < -1 else "横住")

    def attr_bucket(r):
        return "okx驱动" if abs(r.get("gap_okx_ret15_bps", 0.0)) >= abs(r.get("gap_anchor_ret15_bps", 0.0)) else "锚驱动"

    cells = {}
    for r in rows:
        key = (age_bucket(r), vel_bucket(r), attr_bucket(r))
        cells.setdefault(key, []).append(r["net_bps"])

    passed = []
    for key in sorted(cells):
        nets = cells[key]
        m, p = median(nets), pf(nets)
        mark = ""
        if len(nets) >= MIN_CELL and m > 0 and p >= 1.3:
            mark = " ✅ 过门(候选粒子,走冠军梯子)"
            passed.append(key)
        print("  %s×%s×%s n=%d 净中位=%+.1f 净PF=%.2f%s" % (key[0], key[1], key[2], len(nets), m, p, mark))

    # 单维参考(不判决,只帮读)
    for name, fn in (("速度", vel_bucket), ("归因", attr_bucket), ("生龄", age_bucket)):
        agg = {}
        for r in rows:
            agg.setdefault(fn(r), []).append(r["net_bps"])
        parts = ", ".join("%s n=%d 中位%+.1f PF%.2f" % (k, len(v), median(v), pf(v)) for k, v in sorted(agg.items()))
        print("单维·%s: %s" % (name, parts))

    if passed:
        print("【到线】过门桶 %d 个 → 按流水线章程预注册进冠军梯子" % len(passed))
    else:
        print("无过门桶(如实)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
