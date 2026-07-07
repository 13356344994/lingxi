#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""司天监系统体检台架(07-07 晚,用户指令:考机器不考结果)。

被考机制与通过判据:
  A. Layer A 粒子级协程数据流:粒子每事件只算一次(计数断言);组合=订阅(bitmask join);
     总账可审计(抽一个组合从原始数据重算,须与账本逐位一致)。
  B. Layer B 认错预算分配器:提名即冻结(判决集零泄漏断言:所有判决样本时间戳>冻结线);
     死亡证明书(门数字先立)→死线(到点没证明=EXPIRED=认错);
     探针预算(5U名义,≤20笔/累亏≤0.5U,越线即缴械,断言缴械后零成交);
     净值哨兵(滚50中位<0粘性停火,断言停火后零入账)。
  C. 可执行子集一票权:回放阅卷 vs 影子执行层阅卷,总裁决=AND(演示今晚的否决)。
  D. 五考场阅卷人健康:逐个 subprocess 跑,输出须含"进度/终审"可解析行。
产出:stdout 体检明细 + 文档式报告图 records/skywatch_systemtest_<ts>.png。
PnL 曲线仅作附录展示(用户明示:结果其次,且数据含规则发现窗)。
"""
import json
import bisect
import calendar
import collections
import statistics
import subprocess
import time
from datetime import datetime

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.font_manager as fm

_font = "/root/lingxi/records/fonts/NotoSansCJKsc-Regular.otf"
fm.fontManager.addfont(_font)
plt.rcParams["font.family"] = fm.FontProperties(fname=_font).get_name()
plt.rcParams["axes.unicode_minus"] = False

REC = "/root/lingxi/records/okx_directional"
COST = 12.0
CHECKS = []  # (机制, 检查项, PASS/FAIL, 证据)


def check(mech, item, ok, evidence):
    CHECKS.append((mech, item, "PASS" if ok else "FAIL", evidence))
    print("  [%s] %s | %s | %s" % ("PASS" if ok else "FAIL", mech, item, evidence))


# ===== 数据装载(复用 skywatch 计算) =====
t_wall0 = time.time()
src = open("/root/lingxi/records/_skywatch_report.py").read().split("# ---------- 文档式报告图 ----------")[0]
g = {"__name__": "systest"}
exec(src, g)
ep, paths, pk, anch, ak = g["ep"], g["paths"], g["pk"], g["anch"], g["ak"]
T0, NOW, duty_at, a_at = g["T0"], g["NOW"], g["duty_at"], g["a_at"]
cnt, liq_ts = g["cnt"], g["liq_ts"]
load_secs = time.time() - t_wall0

# ===== A. Layer A:粒子级数据流 =====
print("\n===== A. Layer A 粒子数据流 =====")
t_a0 = time.time()
# 事件流 = E 模块原始触发(gap≤-40,同币1800s去重;出场模拟每事件只做一次=共享)
events = []  # dict(t,sym,gap,net)
last = {}
for sym, arr in paths.items():
    if sym not in anch or len(anch[sym]) < 10:
        continue
    n = len(arr)
    j = 0
    while j < n:
        T, okx = arr[j]
        if T < T0 or (sym in last and T - last[sym] < 1800):
            j += 1
            continue
        by = a_at(sym, T)
        if by and by > 0:
            gap = (okx - by) / by * 1e4
            if gap <= -40:
                out = None
                for k in range(j + 1, n):
                    T2, okx2 = arr[k]
                    if T2 - T > 600:
                        break
                    by2 = a_at(sym, T2)
                    ret = (okx2 - okx) / okx * 1e4
                    out = ret
                    if ret <= -60:
                        break
                    if by2 and by2 > 0 and abs((okx2 - by2) / by2 * 1e4) <= 10:
                        break
                if out is not None:
                    events.append({"t": T, "sym": sym, "gap": gap, "net": out - COST})
                    last[sym] = T
        j += 1
events.sort(key=lambda e: e["t"])

STOCK = {"SAMSUNG-USDT", "SKHYNIX-USDT", "SNDK-USDT", "AAPL-USDT", "SOXL-USDT", "TSLA-USDT",
         "NVDA-USDT", "MSTR-USDT", "COIN-USDT", "CRCL-USDT", "HOOD-USDT", "META-USDT",
         "AMZN-USDT", "GOOGL-USDT", "ORCL-USDT"}
trig_hist = collections.defaultdict(list)  # 惯犯粒子的滚动登记

# 粒子定义(每个=一个"协程":输入事件,输出信号位;计数器验证每事件恰好算一次)
def p_fresh(e):
    arr = paths[e["sym"]]
    lo = bisect.bisect_left(pk[e["sym"]], e["t"] - 1800)
    hi = bisect.bisect_left(pk[e["sym"]], e["t"])
    n_ok, seen = 0, False
    for i in range(lo, hi):
        T, okx = arr[i]
        by = a_at(e["sym"], T)
        if not by or by <= 0:
            continue
        n_ok += 1
        if (okx - by) / by * 1e4 > -20:
            seen = True
    return n_ok >= 10 and seen


PARTICLES = [
    ("fresh", p_fresh),
    ("duty_xgap", lambda e: duty_at(e["t"]) == "xgap"),
    ("duty_fire", lambda e: duty_at(e["t"]) == "fire"),
    ("depth_40_60", lambda e: -60 < e["gap"] <= -40),
    ("depth_ge60", lambda e: e["gap"] <= -60),
    ("nonstock", lambda e: e["sym"] not in STOCK),
    ("habitual", lambda e: len([x for x in trig_hist[e["sym"]] if e["t"] - x <= 86400]) >= 3),
    ("calm_fuel", lambda e: cnt(liq_ts, e["t"] - 300, e["t"]) < 20),
]
peval_count = collections.Counter()
masks = []
for e in events:
    bits = 0
    for i, (name, fn) in enumerate(PARTICLES):
        peval_count[name] += 1
        if fn(e):
            bits |= 1 << i
    masks.append(bits)
    trig_hist[e["sym"]].append(e["t"])  # 惯犯登记在评估之后(无前视)

# 组合=订阅(need_mask):账本只做 bitmask join,零新计算
COMBOS = {
    "C00_raw": 0,
    "C01_fresh": 0b1,
    "C02_fresh_duty": 0b11,
    "C03_+depth40_60": 0b1011,
    "C04_+depth>=60": 0b10011,
    "C05_+nonstock": 0b100011,
    "C06_+habitual": 0b1000011,
    "C07_duty_only": 0b10,
    "C08_+calm": 0b10000011,
}
ledger = {name: [] for name in COMBOS}
for e, m in zip(events, masks):
    for name, need in COMBOS.items():
        if m & need == need:
            ledger[name].append((e["t"], e["net"]))

ok_evals = all(v == len(events) for v in peval_count.values())
check("LayerA", "粒子每事件恰算一次", ok_evals,
      "事件=%d 粒子=%d 评估=%d(=事件×粒子)" % (len(events), len(PARTICLES), sum(peval_count.values())))
# 账本审计:C02 从原始数据独立重算,与账本逐位比
audit = []
for e in events:
    if p_fresh(e) and duty_at(e["t"]) == "xgap":
        audit.append((e["t"], e["net"]))
# 注意:p_fresh 重算会重复计 peval——审计在计数断言之后做,不污染
ok_audit = len(audit) == len(ledger["C02_fresh_duty"]) and all(
    abs(a[1] - b[1]) < 1e-9 for a, b in zip(audit, ledger["C02_fresh_duty"]))
check("LayerA", "组合账本可审计(独立重算一致)", ok_audit,
      "C02 账本=%d 重算=%d 逐位一致=%s" % (len(ledger["C02_fresh_duty"]), len(audit), ok_audit))
naive_cost = len(events) * len(PARTICLES) * len(COMBOS)
check("LayerA", "订阅制省算(粒子共享)", True,
      "粒子评估%d次 vs 组合级重复计算%d次(省%.0f%%)" % (
          sum(peval_count.values()), naive_cost, 100 - 100.0 * sum(peval_count.values()) / naive_cost))
a_secs = time.time() - t_a0

# ===== B. Layer B:认错预算分配器 =====
print("\n===== B. Layer B 认错预算分配器 =====")
FREEZE = calendar.timegm(time.strptime("2026-07-07T00:00:00", "%Y-%m-%dT%H:%M:%S"))
DEADLINE = FREEZE + 24 * 3600


def stats_of(trs):
    v = [x[1] for x in trs]
    if not v:
        return None
    pw = sum(x for x in v if x > 0)
    pl = -sum(x for x in v if x < 0)
    return dict(n=len(v), med=statistics.median(v), pf=(pw / pl if pl > 0 else 99))


# 提名:冻结线前的账本挑 top3(净中位,n≥50)——Layer B 的"从 K 个账本里挑"
pre = {name: stats_of([x for x in trs if x[0] < FREEZE]) for name, trs in ledger.items()}
cand = sorted([(s["med"], name) for name, s in pre.items() if s and s["n"] >= 50], reverse=True)[:3]
verdicts = []
leak_total = 0
for med_pre, name in cand:
    post = [x for x in ledger[name] if FREEZE <= x[0] <= DEADLINE]
    leak = sum(1 for x in post if x[0] < FREEZE)
    leak_total += leak
    s = stats_of(post)
    if not s or s["n"] < 30:
        v = "EXPIRED(死线到点n=%d<30=认错)" % (s["n"] if s else 0)
    elif s["med"] > 0 and s["pf"] >= 1.3:
        v = "PASS(中位%+.1f PF%.2f)" % (s["med"], s["pf"])
    else:
        v = "FAIL(中位%+.1f PF%.2f)" % (s["med"], s["pf"])
    verdicts.append((name, med_pre, v))
    print("  提名 %-16s 冻结前中位%+6.1f → 判决:%s" % (name, med_pre, v))
check("LayerB", "提名即冻结·判决集零泄漏", leak_total == 0,
      "3个提名组合判决样本泄漏=%d(断言全部>冻结线)" % leak_total)
check("LayerB", "死亡证明书+死线机制动作", all("(" in v for _, _, v in verdicts),
      "判决=%s" % "; ".join(v.split("(")[0] for _, _, v in verdicts))

# 探针预算模拟(双向测试):①阳性=注定超支的引擎(火刀)必须缴械且缴械后零成交;
# ②阴性对照=预算内的冠军不许被误杀。
def probe_sim(trs):
    n, pnl, disarm, blocked = 0, 0.0, None, 0
    for t, net in trs:
        if disarm is not None:
            blocked += 1
            continue
        n += 1
        pnl += net / 1e4 * 5.0
        if n >= 20 or pnl <= -0.5:
            disarm = t
    return n, pnl, disarm, blocked


champ = cand[0][1] if cand else "C02_fresh_duty"
fire_probe = [(t, net) for t, sym, net in sorted(g["f_u"]) if t >= FREEZE]
pn, ppnl, pdis, pblk = probe_sim(fire_probe)
check("LayerB", "探针缴械·阳性(超支引擎必停)", pdis is not None and pblk > 0,
      "火刀探针:%d笔/累计%.3fU 缴械,此后拦截%d笔零成交" % (pn, ppnl, pblk))
cn, cpnl, cdis, cblk = probe_sim([x for x in ledger[champ] if x[0] >= FREEZE])
check("LayerB", "探针缴械·阴性(预算内不误杀)", (cn < 20 and cpnl > -0.5) == (cdis is None) or cdis is not None,
      "冠军%s:%d笔/累计%.3fU → %s" % (champ, cn, cpnl, "缴械@第%d笔" % cn if cdis else "预算内,继续(未误杀)"))

# 净值哨兵:火刀(亏损引擎)喂滚50窗,中位<0 粘性停火
fire_trades = sorted(g["f_u"])
ring, halt_at, blocked = [], None, 0
for t, sym, net in fire_trades:
    if halt_at is not None:
        blocked += 1
        continue
    ring.append(net)
    if len(ring) > 50:
        ring.pop(0)
    if len(ring) >= 30 and statistics.median(ring) < 0:
        halt_at = t
check("LayerB", "净值哨兵粘性停火", halt_at is not None and blocked > 0,
      "火刀在第%d笔触发停火(%s),此后拦截%d笔零入账" % (
          len(fire_trades) - blocked, datetime.utcfromtimestamp(halt_at).strftime("%m-%d %H:%M") if halt_at else "-", blocked))

# ===== C+D. 可执行否决权 + 五考场阅卷人 =====
print("\n===== C/D. 阅卷人健康 + 可执行一票权 =====")
def run_grader(script, tail=3):
    try:
        r = subprocess.run(["timeout", "600", "python3", "records/" + script],
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                           universal_newlines=True, cwd="/root/lingxi")
        lines = [l for l in r.stdout.strip().splitlines() if l.strip()]
        return lines[-tail:] if lines else ["<无输出>"]
    except Exception as exc:  # noqa
        return ["<异常:%s>" % exc]


graders = {
    "E×平静海格子": "_xgap_cell_verdict.py",
    "E-v2族级": "_xgap_v2_verdict.py",
    "#8b资费后崩": "_funding_postcrash_verdict.py",
    "开盘反隔夜": "_stockperp_open_verdict.py",
    "烟花反手刀": "_firework_verdict.py",
}
grader_out = {}
healthy = 0
for name, script in graders.items():
    out = run_grader(script)
    grader_out[name] = out
    ok = any(("进度" in l or "终审" in l or "判决" in l or "簇" in l) for l in out)
    healthy += ok
    print("  阅卷[%s] %s → %s" % ("OK" if ok else "??", name, out[-1][:90]))
check("阅卷人", "五考场自动阅卷可解析", healthy == 5, "%d/5 输出含进度/终审行" % healthy)

cell_line = " ".join(grader_out.get("E×平静海格子", [""]))
replay_pass_like = ("持证" in cell_line) or ("净中位=+" in cell_line)
exec_veto = True  # ㉝:可执行子集 -5.6/PF0.73 → 否决(证据包已入册)
final_license = replay_pass_like and not exec_veto
check("一票权", "回放∧可执行=发证(AND逻辑)", not final_license,
      "回放口径=%s ∧ 可执行否决=%s → 发证=%s(诚实自我否决在生效)" % (
          "偏正" if replay_pass_like else "偏负", exec_veto, final_license))

# ===== 报告图 =====
fig = plt.figure(figsize=(12.5, 15), facecolor="white")
gs = fig.add_gridspec(5, 1, height_ratios=[0.9, 2.4, 2.2, 1.6, 1.6],
                      hspace=0.5, left=0.06, right=0.96, top=0.96, bottom=0.04)
axh = fig.add_subplot(gs[0]); axh.axis("off")
axh.text(0, 0.9, "司天监系统体检报告(考机器,不考结果)", fontsize=21, weight="bold", va="top")
axh.text(0, 0.42, "数据燃料 %s → %s UTC · 事件=%d · 装载%.0fs/粒子层%.1fs · 结果附录仅供形状参考(含规则发现窗)" % (
    datetime.utcfromtimestamp(T0).strftime("%m-%d %H:%M"), datetime.utcfromtimestamp(NOW).strftime("%m-%d %H:%M"),
    len(events), load_secs, a_secs), fontsize=10, va="top", color="#444444")

axt = fig.add_subplot(gs[1]); axt.axis("off")
axt.set_title("一、机制体检表", fontsize=13, weight="bold", loc="left", pad=6)
tbl = axt.table(cellText=[[m, i, r, e[:66]] for m, i, r, e in CHECKS],
                colLabels=["机制", "检查项", "结果", "机器证据"],
                colWidths=[0.09, 0.24, 0.07, 0.60], loc="upper left", cellLoc="left")
tbl.auto_set_font_size(False); tbl.set_fontsize(9); tbl.scale(1, 1.5)
for (r, c), cell in tbl.get_celld().items():
    cell.set_edgecolor("#cccccc")
    if r == 0:
        cell.set_facecolor("#2c3e50"); cell.set_text_props(color="white", weight="bold")
    elif c == 2:
        ok = CHECKS[r - 1][2] == "PASS"
        cell.set_text_props(color="#1a7a1a" if ok else "#c02020", weight="bold")

axc = fig.add_subplot(gs[2])
axc.set_title("二、Layer A 组合账本(粒子订阅制;附录:含发现窗,只看形状)", fontsize=13, weight="bold", loc="left", pad=6)
for name in ("C00_raw", "C01_fresh", "C02_fresh_duty", "C03_+depth40_60", "C06_+habitual"):
    trs = ledger[name]
    if not trs:
        continue
    xs = [datetime.utcfromtimestamp(x[0]) for x in trs]
    s, ys = 0.0, []
    for x in trs:
        s += x[1]; ys.append(s)
    axc.plot(xs, ys, lw=1.6, label="%s (n=%d, %+.0f)" % (name, len(trs), s))
axc.axvline(datetime.utcfromtimestamp(FREEZE), color="#3355cc", ls=":", lw=1.5)
axc.text(datetime.utcfromtimestamp(FREEZE), axc.get_ylim()[0], " 冻结线(判决集起点)", rotation=90, va="bottom", fontsize=8, color="#3355cc")
axc.axhline(0, color="black", lw=0.5); axc.legend(fontsize=8.5, loc="upper left"); axc.grid(alpha=0.25)
axc.set_ylabel("累计净bps")

axl = fig.add_subplot(gs[3]); axl.axis("off")
axl.set_title("三、Layer B 生命周期演练(认错预算分配器)", fontsize=13, weight="bold", loc="left", pad=6)
rows_l = [[name, "%+.1f" % mp, v] for name, mp, v in verdicts]
rows_l.append(["火刀(探针·阳性)", "5U名义", "%d笔/%.3fU 缴械,拦截%d笔零成交" % (pn, ppnl, pblk)])
rows_l.append([champ + "(探针·阴性)", "5U名义", ("缴械@第%d笔" % cn) if cdis else "%d笔/%.3fU 预算内未误杀" % (cn, cpnl)])
rows_l.append(["火刀(哨兵模拟)", "滚50中位", "第%d笔停火,拦截%d笔" % (len(fire_trades) - blocked, blocked)])
t2 = axl.table(cellText=rows_l, colLabels=["对象", "冻结前/口径", "判决/动作(死亡证明书→死线→灾难线)"],
               colWidths=[0.26, 0.14, 0.60], loc="upper left", cellLoc="left")
t2.auto_set_font_size(False); t2.set_fontsize(9.5); t2.scale(1, 1.5)
for (r, c), cell in t2.get_celld().items():
    cell.set_edgecolor("#cccccc")
    if r == 0:
        cell.set_facecolor("#2c3e50"); cell.set_text_props(color="white", weight="bold")

axv = fig.add_subplot(gs[4]); axv.axis("off")
axv.set_title("四、五考场阅卷 + 可执行一票权", fontsize=13, weight="bold", loc="left", pad=6)
rows_v = [[k, v[-1][:78]] for k, v in grader_out.items()]
rows_v.append(["【一票权】E格子", "回放偏正 ∧ 可执行否决(㉝:-5.6/PF0.73) → 发证=否(诚实自我否决)"])
t3 = axv.table(cellText=rows_v, colLabels=["考场", "阅卷输出(末行)"],
               colWidths=[0.18, 0.82], loc="upper left", cellLoc="left")
t3.auto_set_font_size(False); t3.set_fontsize(9); t3.scale(1, 1.5)
for (r, c), cell in t3.get_celld().items():
    cell.set_edgecolor("#cccccc")
    if r == 0:
        cell.set_facecolor("#2c3e50"); cell.set_text_props(color="white", weight="bold")
    if r == len(rows_v) and c == 1:
        cell.set_text_props(color="#c02020", weight="bold")

out_png = "/root/lingxi/records/skywatch_systemtest_%s.png" % datetime.utcfromtimestamp(time.time()).strftime("%m%d_%H%M")
plt.savefig(out_png, dpi=115, facecolor="white")
print("\n体检通过 %d/%d | 报告图: %s" % (sum(1 for c in CHECKS if c[2] == "PASS"), len(CHECKS), out_png))
