#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""爆仓→分拣器→粒子组合 流水线数据状态图(2026-07-06,用户"现在收集的数据情况怎么样了"问询)。
四面板:①全链漏斗(实数)②逐日样本积累 ③判决进度条(vs 需求量+死线)④判决集海况时间线(为什么开枪少)。
"""
import json
import os
import statistics
from datetime import datetime

import matplotlib
matplotlib.use("Agg")
from matplotlib import font_manager
import matplotlib.pyplot as plt
import matplotlib.dates as mdates

FP = "/usr/share/fonts/google-droid/DroidSansFallback.ttf"
font_manager.fontManager.addfont(FP)
plt.rcParams["font.family"] = font_manager.FontProperties(fname=FP).get_name()
plt.rcParams["axes.unicode_minus"] = False

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
NOW = datetime.utcnow()
FRESH = datetime(2026, 7, 5, 18, 0)      # 第二代判决集起点
DEADLINE = datetime(2026, 7, 8, 18, 0)   # 72h 死线


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


# --- 逐日计数:官方爆仓 / burst探测 / tape确认 / 过闸 ---
def daily(fn, filt=None, key="recorded_at"):
    d = {}
    with open(os.path.join(DIR, fn)) as f:
        for line in f:
            if filt and filt not in line:
                continue
            try:
                r = json.loads(line)
            except Exception:
                continue
            d[r[key][:10]] = d.get(r[key][:10], 0) + 1
    return d


liq_d = daily("fast_liquidation_events.jsonl")
burst_d = daily("fast_burst_detect.jsonl")

tape_d, pass_d = {}, {}
tape_total, pass_total, judge_rows = 0, 0, []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst":
            continue
        day = r["recorded_at"][:10]
        tape_d[day] = tape_d.get(day, 0) + 1
        tape_total += 1
        if not r.get("gate_skip"):
            pass_d[day] = pass_d.get(day, 0) + 1
            pass_total += 1

burst_total = sum(burst_d.values())
liq_total = sum(liq_d.values())

fig, axes = plt.subplots(2, 2, figsize=(15, 10))
fig.suptitle("爆仓 → 分拣器 → 粒子组合:数据资产与判决进度  ·  %s BJ" %
             (NOW.strftime("%m-%d %H:%M")), fontsize=15, fontweight="bold")

# ① 全链漏斗
ax = axes[0][0]
stages = ["官方爆仓推送(3.5天)", "burst 探测(耳朵)", "确认枪(burst×官方)", "过实枪资格闸(70%)",
          "第二代判决集(07-05 18Z后)", "分拣器放行开枪"]
vals = [liq_total, burst_total, tape_total, pass_total, 429, 4]
colors = ["#7a94ad", "#5b8ac0", "#3a7bd5", "#2f66b0", "#f2a63b", "#e0503c"]
yp = range(len(stages))[::-1]
ax.barh(list(yp), vals, color=colors)
for y, v in zip(yp, vals):
    ax.text(v * 1.02 + 40, y, format(v, ","), va="center", fontsize=10.5, fontweight="bold")
ax.set_yticks(list(yp))
ax.set_yticklabels(stages, fontsize=9.5)
ax.set_xscale("log")
ax.set_xlim(1, 90000)
ax.set_title("① 全链漏斗(对数轴):数据不缺,缺的是海况放行")

# ② 逐日样本积累
ax = axes[0][1]
days = sorted(set(list(liq_d) + list(burst_d) + list(tape_d)))
days = [d for d in days if d >= "2026-07-03"]
x = [datetime.strptime(d, "%Y-%m-%d") for d in days]
ax.plot(x, [liq_d.get(d, 0) for d in days], "o-", color="#7a94ad", label="官方爆仓推送")
ax.plot(x, [burst_d.get(d, 0) for d in days], "s-", color="#5b8ac0", label="burst 探测")
ax.plot(x, [tape_d.get(d, 0) for d in days], "^-", color="#3a7bd5", lw=2, label="确认枪(判决原料)")
ax.plot(x, [pass_d.get(d, 0) for d in days], "v-", color="#e0503c", lw=2, label="过资格闸")
for d, xx in zip(days, x):
    ax.text(xx, tape_d.get(d, 0) + 150, "%d" % tape_d.get(d, 0), ha="center", fontsize=9, color="#3a7bd5")
ax.set_yscale("log")
ax.xaxis.set_major_formatter(mdates.DateFormatter("%m-%d"))
ax.set_title("② 逐日积累(对数轴):确认枪 ~200-380/天,原料供给稳定\n(07-06 未过完;07-04 探测器部署当日起步)")
ax.legend(fontsize=9)

# ③ 判决进度 vs 死线
ax = axes[1][0]
items = [("冠军A 判决样本\n(需 n≥30 双正)", 3, 30, "#3a7bd5"),
         ("分拣器开枪样本\n(需 n≥30 双正)", 4, 30, "#e0503c"),
         ("大额桶复活候选\n(需 n≥30,现+30.7)", 11, 30, "#f2a63b"),
         ("判决窗时间消耗\n(72h 死线)", (NOW - FRESH).total_seconds() / 3600.0, 72, "#8899aa")]
for i, (lb, v, need, c) in enumerate(items):
    y = len(items) - 1 - i
    ax.barh(y, need, color="#eceff3", height=0.55)
    ax.barh(y, min(v, need), color=c, height=0.55)
    unit = "h" if "时间" in lb else ""
    ax.text(need + 0.5, y, "%.0f%s / %d%s" % (v, unit, need, unit), va="center", fontsize=10.5, fontweight="bold")
    ax.text(-1.2, y, lb, va="center", ha="right", fontsize=9.5)
ax.set_xlim(0, 90)
ax.set_yticks([])
ax.set_title("③ 判决进度:原料多但\"放行样本\"极稀\n死线 07-08 18:00Z,剩 %.0fh;n<30=既非证伪也非通过→三个数交你裁" %
             ((DEADLINE - NOW).total_seconds() / 3600.0))
ax.axvline(30, color="#333", ls="--", lw=0.8)

# ④ 判决集海况时间线(lag口径,复算)
ax = axes[1][1]
rows = []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") == "tape_burst":
            rows.append(tsu(r["recorded_at"]))
rows.sort()
# 近似海况读数:用 _secondgen_watch.log 的刹车占比按小时画确认枪密度 + 判决窗标记
hours = {}
for t in rows:
    if t >= datetime(2026, 7, 4, 12):
        k = t.replace(minute=0, second=0, microsecond=0)
        hours[k] = hours.get(k, 0) + 1
hx = sorted(hours)
ax.bar(hx, [hours[k] for k in hx], width=0.038, color="#5b8ac0", label="确认枪/小时")
ax.axvspan(FRESH, min(NOW, DEADLINE), color="#f2a63b", alpha=0.18, label="第二代判决窗")
ax.axvline(FRESH, color="#f2a63b", lw=1.5)
ax.text(FRESH, max(hours.values()) * 0.95, " 判决窗开(07-05 18Z)", fontsize=9, color="#a06010")
ax.xaxis.set_major_formatter(mdates.DateFormatter("%m-%d %H", tz=None))
ax.set_title("④ 确认枪流入节奏:判决窗内 429 枪原料充足\n海况刹车拦 344(80%)——不是没猎物,是海全程在暴风(净负期)")
ax.legend(fontsize=9, loc="upper left")
plt.setp(ax.get_xticklabels(), rotation=20, fontsize=8.5)

plt.tight_layout(rect=[0, 0, 1, 0.94])
out = os.path.join(HERE, "pipeline_status_%s.png" % NOW.strftime("%m%d_%H%M"))
plt.savefig(out, dpi=130)
print("saved:", out)
print("liq=%d burst=%d tape=%d pass=%d" % (liq_total, burst_total, tape_total, pass_total))
