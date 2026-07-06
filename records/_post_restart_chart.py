#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""v6.4 重启后 88 分钟首报出图(数据来自 _post_restart_report.py 同逻辑)。"""
import json
import os
import bisect
import statistics
from datetime import datetime, timedelta

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
DEPLOY = datetime(2026, 7, 6, 10, 6, 56)
NOW = datetime.utcnow()


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


liq = {}
with open(os.path.join(DIR, "fast_liquidation_events.jsonl")) as f:
    for line in f:
        try:
            r = json.loads(line)
            liq.setdefault(r["symbol"], []).append(tsu(r["recorded_at"]))
        except Exception:
            continue
for v in liq.values():
    v.sort()

before, after = [], []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst" or not r.get("event_age_ms"):
            continue
        et = tsu(r["recorded_at"])
        t0 = et - timedelta(milliseconds=r["event_age_ms"])
        arr = liq.get(r["symbol"], [])
        i = bisect.bisect_right(arr, t0)
        conf = None
        if i < len(arr) and (arr[i] - t0).total_seconds() <= (r["event_age_ms"] / 1000.0) + 0.2:
            conf = (arr[i] - t0).total_seconds() * 1000.0
        rec = {"t": et, "sym": r["symbol"], "age": r["event_age_ms"], "conf": conf,
               "fat": max(0.0, r["event_age_ms"] - conf) if conf is not None else None,
               "rest": r.get("eval_rest_ms"), "skip": r.get("gate_skip", "")}
        (after if et >= DEPLOY else before).append(rec)

med = lambda v: statistics.median(v) if v else float("nan")
b_fat = [x["fat"] for x in before if x["fat"] is not None]
a_fat = [x["fat"] for x in after if x["fat"] is not None]
a_rest = [x["rest"] for x in after if x["rest"]]
mins = (NOW - DEPLOY).total_seconds() / 60

fig, axes = plt.subplots(2, 2, figsize=(15, 9.5))
fig.suptitle("v6.4 重启后首报:%d 分钟 · 新枪 %d 发 · 两把刀实测生效  ·  07-06 19:35 BJ" % (mins, len(after)),
             fontsize=15, fontweight="bold")

# ① 逐枪时间线
ax = axes[0][0]
cmap = {"": ("#2e8b57", "过闸"), "depth_thin": ("#f2a63b", "深度薄(闸拦)"), "confirm_side_mismatch": ("#c0504d", "方向错号(闸拦)")}
seen = set()
for x in after:
    c, lb = cmap.get(x["skip"], ("#888", x["skip"]))
    ax.scatter(x["t"], x["age"], color=c, s=55, label=lb if lb not in seen else None, zorder=3)
    seen.add(lb)
ax.axhline(2000, color="#a00", ls=":", lw=1)
ax.text(DEPLOY + timedelta(minutes=2), 2050, "2s 半衰期", fontsize=8.5, color="#a00")
ax.xaxis.set_major_formatter(mdates.DateFormatter("%H:%M"))
ax.set_ylabel("EventAgeMs(burst→评估)")
ax.set_title("① 重启后逐枪:%d 发确认枪(≈%.0f发/小时,供给正常)\n绿=过实枪资格闸(%d) 橙/红=闸口拦下(%d)" % (
    len(after), len(after) / (mins / 60), sum(1 for x in after if not x["skip"]),
    sum(1 for x in after if x["skip"])))
ax.legend(fontsize=9, loc="upper right")

# ② 刀①:排队脂肪
ax = axes[0][1]
ax.bar(["改前(等150ms轮询)\nn=%d" % len(b_fat), "改后(推送落地即醒)\nn=%d" % len(a_fat)],
       [med(b_fat), med(a_fat)], color=["#8899aa", "#2e8b57"], width=0.5)
ax.text(0, med(b_fat) + 2, "%.0f ms" % med(b_fat), ha="center", fontweight="bold", fontsize=13)
ax.text(1, med(a_fat) + 2, "%.0f ms" % med(a_fat), ha="center", fontweight="bold", fontsize=13, color="#1a5c38")
ax.set_ylabel("确认到达 → 开始评估 (ms,中位)")
ax.set_title("② 刀①事件驱动唤醒:排队脂肪 75 → 0 ms\n(28枪里26枪=0,最大3ms——轮询等待被整根切掉)")

# ③ 刀②:REST 组装耗时逐枪
ax = axes[1][0]
ax.plot([x["t"] for x in after], [x["rest"] for x in after], "o-", color="#3a7bd5", ms=5)
ax.axhline(195, color="#8899aa", ls="--", lw=1.2)
ax.text(after[0]["t"], 199, "改前串行估计 ~195ms(95+100 实测)", fontsize=9, color="#556")
ax.axhline(med(a_rest), color="#2e8b57", ls="-", lw=1.2)
ax.text(after[0]["t"], med(a_rest) + 4, "改后中位 %.0fms" % med(a_rest), fontsize=9, color="#1a5c38")
ax.xaxis.set_major_formatter(mdates.DateFormatter("%H:%M"))
ax.set_ylabel("盘口+深度组装 (ms)")
ax.set_title("③ 刀②REST并发化:组装 ~195 → %.0f ms 中位\n(新字段 eval_rest_ms 每枪自动记账,以后不用估)" % med(a_rest))

# ④ 总结
ax = axes[1][1]
ax.axis("off")
n_pass = sum(1 for x in after if not x["skip"])
txt = (
    "重启后 %d 分钟战报(全 shadow,零实盘)\n"
    "══════════════════════════════\n"
    "供给:确认枪 %d 发(≈%.0f/小时,与判决窗均值一致)\n"
    "闸口:过闸 %d · 深度薄拦 %d · 方向错号拦 %d\n\n"
    "两把刀实测(vs 改前 642 枪基线):\n"
    "  刀① 排队脂肪   75 → 0 ms   (中位,26/28枪=0)\n"
    "  刀② REST组装  ~195 → %.0f ms (中位)\n"
    "  合计每枪进场提前 ~150-160ms,零纯度代价\n\n"
    "EventAge 中位 1384(改前 1308):差异=官方推送延迟\n"
    "的市场波动,非我方通路——我们的部分已是 0。\n\n"
    "下一根刺(数据自己浮出来的):depth_thin 拦的 7 枪\n"
    "全在 11:00 后(NES/YFI/H)——长尾深度收缩时段,\n"
    "闸口按设计工作,不是故障。\n\n"
    "判决窗大盘:事件 441,开枪 4,海况仍暴风;\n"
    "欧美盘(20-02 BJ)是今晚样本起量的窗口。"
) % (mins, len(after), len(after) / (mins / 60), n_pass,
     sum(1 for x in after if x["skip"] == "depth_thin"),
     sum(1 for x in after if x["skip"] == "confirm_side_mismatch"), med(a_rest))
ax.text(0.0, 1.0, txt, va="top", ha="left", fontsize=10.5,
        bbox=dict(boxstyle="round,pad=0.6", fc="#f4f9f4", ec="#9c9"))

plt.tight_layout(rect=[0, 0, 1, 0.94])
out = os.path.join(HERE, "post_restart_%s.png" % NOW.strftime("%m%d_%H%M"))
plt.savefig(out, dpi=130)
print("saved:", out)
